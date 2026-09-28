// Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

// Package datadog transforms an inbound Datadog monitor webhook payload into
// the canonical alert JSON that the core component accepts. It is a Go port
// of the ServiceNow DatadogIncidentUtils Script Include.
package datadog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"sre-alert-ingestion-service/internal/vendors/jsonnum"
	"sre-alert-ingestion-service/internal/vendors/vendorutil"
)

// source identifies alerts produced by this adapter.
const source = "Datadog"

// Alert-state values, derived from $ALERT_TRANSITION (or an explicit "state"
// payload field) via transitionStateMap.
const (
	stateActive    = "ACTIVE"
	stateCompleted = "COMPLETED"
	stateError     = "ERROR"
)

// Tier-4 hardcoded defaults, used when a field is absent from the payload,
// every tag alias, and the operator-supplied config. Keyed by canonical
// field name.
var defaults = map[string]string{
	"METRIC_NAME": "DatadogAlert",
	"SERVICE":     "managed_services",
	"CATEGORY":    "service_interruption",
	"ENVIRONMENT": "production",
	"SEVERITY":    "1",
}

// numericSeverityMap maps Datadog's numeric monitor tag severity to the
// canonical severity labels the core component expects.
var numericSeverityMap = map[string]string{
	"1": "Critical",
	"2": "Major",
	"3": "Minor",
	"4": "Warning",
	"5": "OK",
}

// transitionStateMap maps Datadog's $ALERT_TRANSITION webhook variable to an
// internal lifecycle state. This is what lets one generic webhook replace a
// per-transition webhook pair.
var transitionStateMap = map[string]string{
	"Triggered":    stateActive,
	"Re-Triggered": stateActive,
	"Renotify":     stateActive,
	"Escalated":    stateActive,
	"Warn":         stateActive,
	"Recovered":    stateCompleted,
	"No Data":      stateError,
}

// tagFieldMap maps a tag key (key:value entries parsed out of Datadog's
// $TAGS variable) to the canonical field it can supply. Lookup is
// case-insensitive; parseTags lowercases every key it stores.
var tagFieldMap = map[string]string{
	"service":     "SERVICE",
	"svc":         "SERVICE",
	"env":         "ENVIRONMENT",
	"environment": "ENVIRONMENT",
	"category":    "CATEGORY",
	"severity":    "SEVERITY",
	"sev":         "SEVERITY",
	"metric":      "METRIC_NAME",
	"metric_name": "METRIC_NAME",
}

// ErrInvalidStructure is returned when the payload is not a recognisable
// Datadog monitor webhook (missing event/monitor id and event/monitor name).
var ErrInvalidStructure = errors.New("INVALID DATADOG ALERT PAYLOAD STRUCTURE")

// Alert is the canonical alert model handed to the core component.
type Alert struct {
	Service          string `json:"service"`
	MetricName       string `json:"metric_name"`
	Severity         string `json:"severity"`
	Category         string `json:"category"`
	Environment      string `json:"environment"`
	Source           string `json:"source"`
	UniqueIdentifier string `json:"unique_identifier"`
	Description      string `json:"description"`
}

// Config holds Tier-3 operator overrides (the analog of the ServiceNow
// "edge.api.datadog.alert.config" system property). Any field left empty
// falls back to the Tier-4 default.
type Config map[string]string

// LoadConfig reads Tier-3 overrides from the DATADOG_ALERT_CONFIG env var
// (a JSON object).
func LoadConfig() (Config, error) {
	raw := os.Getenv("DATADOG_ALERT_CONFIG")
	if strings.TrimSpace(raw) == "" {
		return Config{}, nil
	}
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("invalid DATADOG_ALERT_CONFIG: %w", err)
	}
	return cfg, nil
}

// Transform parses a raw inbound Datadog webhook and produces the canonical
// Alert. cfg supplies Tier-3 overrides.
func Transform(raw []byte, cfg Config) (Alert, error) {
	var payload map[string]any
	if err := jsonnum.Unmarshal(raw, &payload); err != nil {
		return Alert{}, fmt.Errorf("%w: %v", ErrInvalidStructure, err)
	}

	// Field-name tolerance: monitor_id/monitor_name are the legacy webhook
	// fields; event_id/event_name are the current ones. Either pair proves
	// this is a genuine Datadog payload.
	eventID := vendorutil.FirstNonEmpty(vendorutil.Str(payload, "monitor_id"), vendorutil.Str(payload, "event_id"))
	eventName := vendorutil.FirstNonEmpty(vendorutil.Str(payload, "monitor_name"), vendorutil.Str(payload, "event_name"))
	if eventID == "" && eventName == "" {
		return Alert{}, ErrInvalidStructure
	}

	triggerName := vendorutil.Str(payload, "trigger_name")
	alertID := vendorutil.Str(payload, "alert_id")
	tags := parseTags(vendorutil.Str(payload, "tags"))
	state := resolveState(payload)

	metricNameValue := vendorutil.FirstNonEmpty(vendorutil.Str(payload, "metric_name"), buildMetricName(eventName, triggerName))
	uniqueIdentifier := vendorutil.FirstNonEmpty(vendorutil.Str(payload, "unique_identifier"), alertID)

	alert := Alert{
		Service:          configValue(cfg, tags, "SERVICE", vendorutil.Str(payload, "service")),
		MetricName:       configValue(cfg, tags, "METRIC_NAME", metricNameValue),
		Severity:         resolveSeverity(state, configValue(cfg, tags, "SEVERITY", vendorutil.Str(payload, "severity"))),
		Category:         configValue(cfg, tags, "CATEGORY", vendorutil.Str(payload, "category")),
		Environment:      configValue(cfg, tags, "ENVIRONMENT", vendorutil.Str(payload, "environment")),
		Source:           source,
		UniqueIdentifier: uniqueIdentifier,
		Description:      vendorutil.CompactJSON(raw),
	}
	return alert, nil
}

// resolveState derives the alert's internal lifecycle state. An explicit
// "state" payload field wins outright (back-compat with the older
// hardcoded-per-webhook approach); otherwise the state is derived from
// $ALERT_TRANSITION via transitionStateMap, defaulting to ACTIVE.
func resolveState(payload map[string]any) string {
	if s := vendorutil.Str(payload, "state"); s != "" {
		return s
	}
	transition := vendorutil.FirstNonEmpty(vendorutil.Str(payload, "transition"), vendorutil.Str(payload, "alert_transition"))
	if state, ok := transitionStateMap[transition]; ok {
		return state
	}
	return stateActive
}

// resolveSeverity maps the lifecycle state and the tier-resolved raw
// severity to a canonical label. A COMPLETED state (Recovered) always
// resolves to OK, and an ERROR state (No Data) always resolves to Critical,
// regardless of any reported severity. Otherwise the numeric severity is
// mapped.
func resolveSeverity(state, rawSeverity string) string {
	switch state {
	case stateCompleted:
		return "OK"
	case stateError:
		return "Critical"
	}
	if rawSeverity == "" {
		rawSeverity = defaults["SEVERITY"]
	}
	if mapped, ok := numericSeverityMap[rawSeverity]; ok {
		return mapped
	}
	return rawSeverity
}

// parseTags splits Datadog's comma-separated "key:value" $TAGS string into a
// map, keyed by lowercased tag key. A bare entry with no ":" is a flag-style
// tag and is skipped.
func parseTags(tags string) map[string]string {
	result := make(map[string]string)
	for _, tag := range strings.Split(tags, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(tag), ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			result[key] = value
		}
	}
	return result
}

// tagValue returns the tag value for a canonical field (e.g. "SERVICE"),
// scanning every tag-key alias that maps to that field.
func tagValue(tags map[string]string, field string) string {
	for tagKey, mappedField := range tagFieldMap {
		if mappedField == field {
			if v, ok := tags[tagKey]; ok && v != "" {
				return v
			}
		}
	}
	return ""
}

// buildMetricName joins the event and trigger names into a human-readable
// metric name, or returns "" when neither is present (letting the tag,
// config, or default tier win).
func buildMetricName(eventName, triggerName string) string {
	parts := make([]string, 0, 2)
	for _, p := range []string{eventName, triggerName} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Datadog Alert: " + strings.Join(parts, " / ")
}

// configValue applies the 4-tier resolution: payload value, then tag alias,
// then operator config, then the hardcoded default for the field.
func configValue(cfg Config, tags map[string]string, field, payloadValue string) string {
	return vendorutil.FirstNonEmpty(payloadValue, tagValue(tags, field), cfg[field], defaults[field])
}
