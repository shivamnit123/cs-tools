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

// Package gcp transforms an inbound Google Cloud Monitoring alerting
// notification payload into the canonical alert JSON that the core
// component accepts. It is a Go port of the ServiceNow GCPAlertProcessor
// Script Include.
//
// Unlike every other vendor here, the reference script performs no
// structural validation at all beyond a body being present -- every
// nested object degrades gracefully via an empty-object fallback, and
// processAlert always succeeds. This is faithfully reproduced: Transform
// only errors on a genuinely empty body.
package gcp

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
const source = "GCP"

// Tier-3 hardcoded defaults, used when a field is absent from both the
// payload and the operator-supplied config. Keyed by canonical field
// name.
var defaults = map[string]string{
	"METRIC_NAME": "GCPCloudAlert",
	"SERVICE":     "Managed Services",
	"CATEGORY":    "Service Interruption",
	"ENVIRONMENT": "Unknown",
	"SEVERITY":    "critical",
}

// severityMap maps GCP Monitoring's condition severity to the canonical
// severity labels the core component expects. Note "info" maps to
// "Informational", not "OK" like every other vendor's lowest tier.
var severityMap = map[string]string{
	"critical": "Critical",
	"error":    "Major",
	"warning":  "Minor",
	"info":     "Informational",
}

// ErrMissingBody is returned when the webhook is called with no body at all.
var ErrMissingBody = errors.New("MISSING REQUEST BODY DATA")

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

// Config holds Tier-2 operator overrides (the analog of the ServiceNow
// "edge.api.gcp.alert.config" system property). Any field left empty
// falls back to the Tier-3 default.
//
// Config keys mostly match every other field's uppercase convention
// (SERVICE, CATEGORY, ENVIRONMENT), but "severity" is looked up
// lowercase -- a faithfully-reproduced inconsistency in the reference
// script, not a Go-port bug. An operator overriding severity via config
// must use the lowercase key.
type Config map[string]string

// LoadConfig reads Tier-2 overrides from the GCP_ALERT_CONFIG env var (a
// JSON object).
func LoadConfig() (Config, error) {
	raw := os.Getenv("GCP_ALERT_CONFIG")
	if strings.TrimSpace(raw) == "" {
		return Config{}, nil
	}
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("invalid GCP_ALERT_CONFIG: %w", err)
	}
	return cfg, nil
}

// Transform parses a raw inbound GCP Monitoring notification and produces
// the canonical Alert. cfg supplies Tier-2 overrides.
func Transform(raw []byte, cfg Config) (Alert, error) {
	if strings.TrimSpace(string(raw)) == "" {
		return Alert{}, ErrMissingBody
	}

	var payload map[string]any
	if err := jsonnum.Unmarshal(raw, &payload); err != nil {
		return Alert{}, fmt.Errorf("%w: %v", ErrMissingBody, err)
	}

	// GCP's notification channel webhook wraps the actual alert under
	// "incident"; every nested object below degrades to an empty map
	// rather than erroring, matching the reference script's total lack
	// of structural validation.
	incident, _ := payload["incident"].(map[string]any)
	resource, _ := incident["resource"].(map[string]any)
	labels, _ := resource["labels"].(map[string]any)
	userLabels, _ := incident["policy_user_labels"].(map[string]any)

	state := vendorutil.FirstNonEmpty(vendorutil.Str(incident, "state"), "open")
	rawSeverity := vendorutil.FirstNonEmpty(vendorutil.Str(incident, "severity"), cfg["severity"], defaults["SEVERITY"])

	var severity string
	if state == "closed" {
		severity = "OK"
	} else {
		severity = vendorutil.FirstNonEmpty(severityMap[strings.ToLower(rawSeverity)], rawSeverity)
	}

	alert := Alert{
		Service:          configValue(cfg, "SERVICE", vendorutil.FirstNonEmpty(vendorutil.Str(labels, "service"), vendorutil.Str(userLabels, "service"))),
		MetricName:       vendorutil.FirstNonEmpty(vendorutil.Str(incident, "policy_name"), vendorutil.Str(incident, "condition_name"), defaults["METRIC_NAME"]),
		Severity:         severity,
		Category:         configValue(cfg, "CATEGORY", vendorutil.FirstNonEmpty(vendorutil.Str(labels, "category"), vendorutil.Str(userLabels, "category"))),
		Environment:      configValue(cfg, "ENVIRONMENT", vendorutil.FirstNonEmpty(vendorutil.Str(labels, "environment"), vendorutil.Str(userLabels, "environment"))),
		Source:           source,
		UniqueIdentifier: vendorutil.Str(incident, "incident_id"),
		Description:      vendorutil.CompactJSON(raw),
	}
	return alert, nil
}

// configValue applies the 3-tier resolution: payload value, then operator
// config, then the hardcoded default for the field.
func configValue(cfg Config, field, payloadValue string) string {
	return vendorutil.FirstNonEmpty(payloadValue, cfg[field], defaults[field])
}
