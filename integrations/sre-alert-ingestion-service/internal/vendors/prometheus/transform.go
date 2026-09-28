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

// Package prometheus transforms an inbound Prometheus Alertmanager (or
// Grafana-compatible) webhook payload into a slice of canonical alerts. It
// is a Go port of the ServiceNow PrometheusAlertProcessor Script Include.
//
// Unlike every other vendor, Alertmanager batches multiple alerts into one
// webhook call ("alerts": [...]), so Transform returns a slice -- the
// shared adapter (internal/adapter) detects this and persists/isolates
// failures per alert, matching the reference script's per-alert try/catch.
package prometheus

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"sre-alert-ingestion-service/internal/vendors/jsonnum"
	"sre-alert-ingestion-service/internal/vendors/vendorutil"
)

// source identifies alerts produced by this adapter when the sender isn't
// detected as Grafana.
const source = "Prometheus"

// grafanaSource is used instead of source when the alert is detected as
// coming through Grafana rather than raw Alertmanager.
const grafanaSource = "Grafana"

// grafanaReceiver is the Alertmanager receiver name Grafana's own default
// contact point uses; matching it (with no grafana_folder label) also
// counts as a Grafana-sourced alert.
const grafanaReceiver = "grafana-default-email"

// grafanaAlertPrefix prefixes a Grafana alert's metric name.
const grafanaAlertPrefix = "Grafana Alert: "

// Tier-3 hardcoded defaults, used when a field is absent from both the
// payload and the operator-supplied config. Keyed by canonical field name.
var defaults = map[string]string{
	"METRIC_NAME": "PrometheusAlert",
	"SERVICE":     "Managed Services",
	"CATEGORY":    "Software",
	"ENVIRONMENT": "Production",
	"SEVERITY":    "Critical",
}

// severityMap maps Prometheus/Grafana's lowercase severity label to the
// canonical severity labels the core component expects. Lookup is
// case-insensitive; an unmapped value passes through as-is.
var severityMap = map[string]string{
	"critical":      "Critical",
	"high":          "Major",
	"low":           "Minor",
	"warning":       "Warning",
	"informational": "OK",
}

// ErrInvalidStructure is returned when the payload has no non-empty
// "alerts" array (the Alertmanager webhook's defining shape).
var ErrInvalidStructure = errors.New("INVALID PROMETHEUS ALERT PAYLOAD STRUCTURE")

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
// "edge.api.prometheus.alert.config" system property). Any field left
// empty falls back to the Tier-3 default.
//
// Config keys mostly match every other field's uppercase convention
// (METRIC_NAME, ENVIRONMENT, SERVICE, CATEGORY), but "severity" and
// "source" are looked up lowercase -- a faithfully-reproduced
// inconsistency in the reference script, not a Go-port bug. An operator
// overriding severity or source via config must use the lowercase key.
type Config map[string]string

// LoadConfig reads Tier-2 overrides from the PROMETHEUS_ALERT_CONFIG env
// var (a JSON object).
func LoadConfig() (Config, error) {
	raw := os.Getenv("PROMETHEUS_ALERT_CONFIG")
	if strings.TrimSpace(raw) == "" {
		return Config{}, nil
	}
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("invalid PROMETHEUS_ALERT_CONFIG: %w", err)
	}
	return cfg, nil
}

// Transform parses a raw inbound Alertmanager-shaped webhook and produces
// one canonical Alert per entry in its "alerts" array. cfg supplies
// Tier-2 overrides.
func Transform(raw []byte, cfg Config) ([]Alert, error) {
	var payload map[string]any
	if err := jsonnum.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidStructure, err)
	}

	rawAlerts, ok := payload["alerts"].([]any)
	if !ok || len(rawAlerts) == 0 {
		return nil, ErrInvalidStructure
	}

	description := vendorutil.CompactJSON(raw)
	receiver := vendorutil.Str(payload, "receiver")
	// Fallback for alerts without their own status.
	batchStatus := vendorutil.Str(payload, "status")

	alerts := make([]Alert, 0, len(rawAlerts))
	for _, rawAlert := range rawAlerts {
		alertMap, ok := rawAlert.(map[string]any)
		if !ok {
			continue
		}
		alerts = append(alerts, transformOne(alertMap, cfg, receiver, batchStatus, description))
	}
	return alerts, nil
}

func transformOne(alert map[string]any, cfg Config, receiver, batchStatus, description string) Alert {
	labels, _ := alert["labels"].(map[string]any)
	annotations, _ := alert["annotations"].(map[string]any)

	isGrafana := vendorutil.Str(labels, "grafana_folder") != "" || receiver == grafanaReceiver
	alertSource := vendorutil.FirstNonEmpty(cfg["source"], source)
	if isGrafana {
		alertSource = grafanaSource
	}

	// Alertmanager sends each alert's status as a string, so a mixed batch resolves per
	// alert; the object form ({"state": ...}) is what the ServiceNow script read.
	alertStatus := vendorutil.Str(alert, "status")
	if statusObj, ok := alert["status"].(map[string]any); ok {
		alertStatus = vendorutil.Str(statusObj, "state")
	}
	alertStatus = vendorutil.FirstNonEmpty(alertStatus, batchStatus)

	rawSeverity := vendorutil.FirstNonEmpty(vendorutil.Str(labels, "severity"), cfg["severity"], defaults["SEVERITY"])

	var severity string
	if alertStatus == "resolved" {
		severity = "OK"
	} else {
		severity = severityMap[strings.ToLower(rawSeverity)]
		if severity == "" {
			severity = rawSeverity
		}
	}

	return Alert{
		Service:          configValue(cfg, "SERVICE", vendorutil.FirstNonEmpty(vendorutil.Str(labels, "service"), vendorutil.Str(labels, "component"), vendorutil.Str(labels, "job"))),
		MetricName:       configValue(cfg, "METRIC_NAME", buildMetricName(isGrafana, labels, annotations)),
		Severity:         severity,
		Category:         configValue(cfg, "CATEGORY", vendorutil.Str(labels, "category")),
		Environment:      configValue(cfg, "ENVIRONMENT", vendorutil.FirstNonEmpty(vendorutil.Str(labels, "environment"), vendorutil.Str(labels, "cluster"))),
		Source:           alertSource,
		UniqueIdentifier: vendorutil.Str(alert, "fingerprint"),
		Description:      description,
	}
}

// buildMetricName mirrors the reference script: a Grafana alert (detected
// via grafana_folder) with an annotations.title uses that, prefixed;
// otherwise the metric name is the alert's labels.alertname.
func buildMetricName(isGrafana bool, labels, annotations map[string]any) string {
	if vendorutil.Str(labels, "grafana_folder") != "" {
		if title := vendorutil.Str(annotations, "title"); title != "" {
			return grafanaAlertPrefix + title
		}
	}
	return vendorutil.Str(labels, "alertname")
}

// configValue applies the 3-tier resolution: payload value, then operator
// config, then the hardcoded default for the field.
func configValue(cfg Config, field, payloadValue string) string {
	return vendorutil.FirstNonEmpty(payloadValue, cfg[field], defaults[field])
}
