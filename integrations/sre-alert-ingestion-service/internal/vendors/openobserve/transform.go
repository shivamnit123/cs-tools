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

// Package openobserve transforms an inbound OpenObserve alert webhook
// payload into the canonical alert JSON that the core component accepts.
// It is a Go port of the ServiceNow OpenObserveIncidentUtils Script
// Include.
//
// Unlike every other vendor, OpenObserve's webhook template resolves its
// own {placeholders} before sending, so short_description/description/
// correlation_id already carry real alert values by the time this runs --
// and its output carries extra ServiceNow Incident-native fields
// (urgency, impact, caller_id, correlation_id, short_description)
// alongside the usual canonical ones, because its severity comes from
// ServiceNow's own urgency/impact scale rather than a vendor severity.
package openobserve

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
const source = "OpenObserve"

// Tier-3 hardcoded defaults, used when a field is absent from both the
// payload and the operator-supplied config. Keyed by canonical field name.
// SHORT_DESCRIPTION and DESCRIPTION are deliberately absent: the reference
// script defines them but never actually applies them -- short_description
// and description are read straight from the payload with no fallback.
var defaults = map[string]string{
	"URGENCY":     "3",
	"IMPACT":      "3",
	"CALLER_ID":   "openobserve",
	"CATEGORY":    "service_interruption",
	"SERVICE":     "managed_services",
	"ENVIRONMENT": "production",
}

// prioritySeverityMap maps ServiceNow's native urgency/impact scale
// (1=High ... 3=Low) to the canonical severity labels the core component
// expects. Unlike every other vendor's numeric map, this only has three
// entries; an unmapped value resolves to "" rather than passing through.
var prioritySeverityMap = map[string]string{
	"1": "Critical",
	"2": "Major",
	"3": "Minor",
}

// ErrInvalidStructure is returned when the payload is not a recognisable
// OpenObserve alert (missing both short_description and correlation_id).
var ErrInvalidStructure = errors.New("INVALID OPENOBSERVE ALERT PAYLOAD STRUCTURE")

// Alert is the canonical alert model handed to the core component, extended
// with the ServiceNow Incident-native fields this vendor's reference script
// also populates.
type Alert struct {
	Service          string `json:"service"`
	MetricName       string `json:"metric_name"`
	Severity         string `json:"severity"`
	Category         string `json:"category"`
	Environment      string `json:"environment"`
	Source           string `json:"source"`
	UniqueIdentifier string `json:"unique_identifier"`
	ShortDescription string `json:"short_description"`
	Description      string `json:"description"`
	Urgency          string `json:"urgency"`
	Impact           string `json:"impact"`
	CorrelationID    string `json:"correlation_id"`
	CallerID         string `json:"caller_id"`
}

// Config holds Tier-2 operator overrides (the analog of the ServiceNow
// "edge.api.openobserve.alert.config" system property). Any field left
// empty falls back to the Tier-3 default.
type Config map[string]string

// LoadConfig reads Tier-2 overrides from the OPENOBSERVE_ALERT_CONFIG env
// var (a JSON object).
func LoadConfig() (Config, error) {
	raw := os.Getenv("OPENOBSERVE_ALERT_CONFIG")
	if strings.TrimSpace(raw) == "" {
		return Config{}, nil
	}
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("invalid OPENOBSERVE_ALERT_CONFIG: %w", err)
	}
	return cfg, nil
}

// Transform parses a raw inbound OpenObserve webhook and produces the
// canonical Alert. cfg supplies Tier-2 overrides.
func Transform(raw []byte, cfg Config) (Alert, error) {
	var payload map[string]any
	if err := jsonnum.Unmarshal(raw, &payload); err != nil {
		return Alert{}, fmt.Errorf("%w: %v", ErrInvalidStructure, err)
	}

	// Validation: a genuine OpenObserve alert carries either its resolved
	// short_description or a correlation_id.
	if vendorutil.Str(payload, "short_description") == "" && vendorutil.Str(payload, "correlation_id") == "" {
		return Alert{}, ErrInvalidStructure
	}

	shortDescription := vendorutil.Str(payload, "short_description")
	description := vendorutil.Str(payload, "description")
	correlationID := vendorutil.Str(payload, "correlation_id")

	rawUrgency := configValue(cfg, "URGENCY", vendorutil.Str(payload, "urgency"))
	rawImpact := configValue(cfg, "IMPACT", vendorutil.Str(payload, "impact"))
	callerID := configValue(cfg, "CALLER_ID", vendorutil.Str(payload, "caller_id"))

	// metric_name has no config/default fallback in the reference script --
	// it's built directly from the payload, or left empty.
	metricName := buildMetricName(shortDescription, correlationID)

	// Severity maps off urgency first, falling back to impact if urgency is
	// missing or unrecognized. Neither has a further fallback: an unmapped
	// pair resolves to an empty severity, faithfully, not a smoothed-over
	// default.
	severity := mapSeverity(rawUrgency)
	if severity == "" {
		severity = mapSeverity(rawImpact)
	}

	// Description keeps the raw payload, like every other vendor; the readable text goes
	// first because the Chat fallback card shows only the first 500 characters.
	desc := "Raw payload: " + vendorutil.CompactJSON(raw)
	if description != "" {
		desc = description + "\n\n" + desc
	}

	alert := Alert{
		Service:          configValue(cfg, "SERVICE", vendorutil.Str(payload, "service")),
		MetricName:       metricName,
		Severity:         severity,
		Category:         configValue(cfg, "CATEGORY", vendorutil.Str(payload, "category")),
		Environment:      configValue(cfg, "ENVIRONMENT", vendorutil.Str(payload, "environment")),
		Source:           source,
		UniqueIdentifier: correlationID,
		ShortDescription: vendorutil.FirstNonEmpty(shortDescription, metricName),
		Description:      desc,
		Urgency:          rawUrgency,
		Impact:           rawImpact,
		CorrelationID:    correlationID,
		CallerID:         callerID,
	}
	return alert, nil
}

// buildMetricName mirrors the reference script: short_description wins
// outright, otherwise a name is built from correlation_id, otherwise empty.
func buildMetricName(shortDescription, correlationID string) string {
	if shortDescription != "" {
		return shortDescription
	}
	if correlationID != "" {
		return "OpenObserve Alert: " + correlationID
	}
	return ""
}

// mapSeverity maps a ServiceNow urgency/impact value (1-3) to a canonical
// severity label. An empty or unmapped value resolves to "".
func mapSeverity(rawValue string) string {
	if rawValue == "" {
		return ""
	}
	return prioritySeverityMap[rawValue]
}

// configValue applies the 3-tier resolution: payload value, then operator
// config, then the hardcoded default for the field.
func configValue(cfg Config, field, payloadValue string) string {
	return vendorutil.FirstNonEmpty(payloadValue, cfg[field], defaults[field])
}
