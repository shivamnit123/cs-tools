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

// Package azure transforms an inbound Azure Monitor Common Alert Schema
// webhook payload into the canonical alert JSON that the core component
// accepts. It is a Go port of the ServiceNow "Azure Alert API" Scripted
// REST Resource.
package azure

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
const source = "Azure"

// Tier-3 hardcoded defaults, used when a field is absent from both the
// payload/customProperties and the operator-supplied config. Keyed by the
// same lowercase field names the real ServiceNow system property
// ("edge.api.azure.alert.config") uses, so an operator can paste that
// property's JSON straight into AZURE_ALERT_CONFIG.
var defaults = map[string]string{
	"metric_name": "AzureCloudAlert",
	"service":     "Managed Services",
	"category":    "Service Interruption",
	"environment": "Unknown",
	"severity":    "Critical",
}

// severityMap maps Azure Monitor's Sev0-Sev4 severity to the canonical
// severity labels the core component expects.
var severityMap = map[string]string{
	"Sev0": "Critical",
	"Sev1": "Major",
	"Sev2": "Minor",
	"Sev3": "Warning",
	"Sev4": "OK",
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
// "edge.api.azure.alert.config" system property). Keyed the same way as
// defaults. Any field left empty falls back to the Tier-3 default.
type Config map[string]string

// LoadConfig reads Tier-2 overrides from the AZURE_ALERT_CONFIG env var
// (a JSON object).
func LoadConfig() (Config, error) {
	raw := os.Getenv("AZURE_ALERT_CONFIG")
	if strings.TrimSpace(raw) == "" {
		return Config{}, nil
	}
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("invalid AZURE_ALERT_CONFIG: %w", err)
	}
	return cfg, nil
}

// Transform parses a raw inbound Azure Monitor Common Alert Schema webhook
// and produces the canonical Alert. cfg supplies Tier-2 overrides.
//
// Unlike the other vendor adapters, Azure carries the alert one level
// nested (payload.data.essentials / payload.data.customProperties /
// payload.data.alertContext) rather than as flat top-level fields, and the
// real ServiceNow script performs no structural validation beyond "a body
// was sent" -- so, faithfully, neither does this port.
func Transform(raw []byte, cfg Config) (Alert, error) {
	if strings.TrimSpace(string(raw)) == "" {
		return Alert{}, ErrMissingBody
	}

	var envelope map[string]any
	if err := jsonnum.Unmarshal(raw, &envelope); err != nil {
		return Alert{}, fmt.Errorf("%w: %v", ErrMissingBody, err)
	}

	data, _ := envelope["data"].(map[string]any)
	essentials, _ := data["essentials"].(map[string]any)
	alertContext, _ := data["alertContext"].(map[string]any)
	// Cost alerts don't include a customProperties field, so default to an
	// empty object rather than treating the whole alert as malformed.
	props, _ := data["customProperties"].(map[string]any)

	rawSeverity := vendorutil.FirstNonEmpty(vendorutil.Str(essentials, "severity"), cfg["severity"], defaults["severity"])
	monitorCondition := vendorutil.Str(essentials, "monitorCondition")

	var mappedSeverity string
	if monitorCondition == "Resolved" {
		mappedSeverity = severityMap["Sev4"] // "OK"
	} else {
		mappedSeverity = severityMap[rawSeverity]
	}

	alert := Alert{
		Service:          configValue(cfg, props, "service", ""),
		MetricName:       configValue(cfg, props, "metric_name", resolveMetricName(essentials, alertContext)),
		Severity:         mappedSeverity,
		Category:         configValue(cfg, props, "category", ""),
		Environment:      configValue(cfg, props, "environment", ""),
		Source:           vendorutil.FirstNonEmpty(cfg["source"], source),
		UniqueIdentifier: vendorutil.Str(essentials, "alertId"),
		Description:      vendorutil.CompactJSON(raw),
	}
	return alert, nil
}

// resolveMetricName mirrors the reference script's cost-alert special case:
// a Cost Management alert has no useful alertRule, so the budget name is
// used instead. Every other monitoring service falls back to essentials.alertRule.
func resolveMetricName(essentials, alertContext map[string]any) string {
	if vendorutil.Str(essentials, "monitoringService") == "CostAlerts" {
		alertData, _ := alertContext["AlertData"].(map[string]any)
		if budgetName := vendorutil.Str(alertData, "BudgetName"); budgetName != "" {
			return "Cost Alert: " + budgetName
		}
	}
	return vendorutil.Str(essentials, "alertRule")
}

// configValue applies the 3-tier resolution: a direct value (used only for
// metric_name's cost-alert/alertRule resolution), else customProperties,
// then operator config, then the hardcoded default for the field.
func configValue(cfg Config, props map[string]any, field, directValue string) string {
	payloadValue := directValue
	if payloadValue == "" {
		payloadValue = vendorutil.Str(props, field)
	}
	return vendorutil.FirstNonEmpty(payloadValue, cfg[field], defaults[field])
}
