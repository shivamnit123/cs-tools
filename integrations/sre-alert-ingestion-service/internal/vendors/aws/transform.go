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

// Package aws transforms an inbound AWS SNS notification (wrapping a
// CloudWatch alarm) into the canonical alert JSON that the core component
// accepts. It is a Go port of the ServiceNow AWSAlertUtilsV2 Script
// Include's alert-processing path.
//
// AWS's philosophy differs from every other vendor here: a malformed
// message never gets rejected with an error -- it gets converted into a
// diagnostic alert instead (metric_name "SNS Message Parse Error"), so
// something always reaches the core rather than silently failing. This is
// reproduced faithfully: Transform only ever returns an error when the
// raw webhook body itself is empty or not valid JSON at all -- everything
// past that point always produces an Alert.
//
// This package covers only the alert-notification path
// (AWSAlertUtilsV2.init). It deliberately does not cover SNS subscription
// confirmation (AWSSNSNotificationUtils.handleSubscription), which is a
// separate operational concern -- auto-confirming a new SNS subscription
// and emailing an admin -- with no equivalent in this platform yet.
package aws

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"sre-alert-ingestion-service/internal/vendors/jsonnum"
	"sre-alert-ingestion-service/internal/vendors/vendorutil"
)

// Tier-2 hardcoded defaults, used when the operator config doesn't supply
// a value. Keyed the same lowercase way the real
// "edge.api.aws.alert.config" system property is, so its JSON can be
// pasted into AWS_ALERT_CONFIG unchanged. "service" is deliberately ""
// -- unlike every other vendor, there is no non-empty hardcoded service
// default in the reference script.
var defaults = map[string]string{
	"source":      "AWS",
	"environment": "Unknown",
	"service":     "",
	"category":    "service_interruption",
	"severity":    "critical",
	"metric_name": "Unknown Metric",
}

// ErrMissingBody is returned when the webhook is called with no body, or a
// body that isn't valid JSON at all.
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

// Config holds operator overrides (the analog of the ServiceNow
// "edge.api.aws.alert.config" system property). Keyed lowercase, matching
// the real property exactly. Any field left empty falls back to the
// hardcoded default.
type Config map[string]string

// LoadConfig reads Config from the AWS_ALERT_CONFIG env var (a JSON
// object).
func LoadConfig() (Config, error) {
	raw := os.Getenv("AWS_ALERT_CONFIG")
	if strings.TrimSpace(raw) == "" {
		return Config{}, nil
	}
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("invalid AWS_ALERT_CONFIG: %w", err)
	}
	return cfg, nil
}

// Transform parses a raw inbound SNS notification and produces the
// canonical Alert. cfg supplies operator overrides.
//
// The real payload is an SNS envelope whose "Message" field is itself a
// JSON string -- the actual CloudWatch alarm data. That inner alarm's
// AlarmDescription is, in turn, expected to optionally carry a JSON
// object of field overrides (service/category/environment/severity),
// since CloudWatch alarms have nowhere else to attach structured data.
func Transform(raw []byte, cfg Config) (Alert, error) {
	if strings.TrimSpace(string(raw)) == "" {
		return Alert{}, ErrMissingBody
	}

	var envelope map[string]any
	if err := jsonnum.Unmarshal(raw, &envelope); err != nil {
		return Alert{}, fmt.Errorf("%w: %v", ErrMissingBody, err)
	}

	base := Alert{
		Service:     configValue(cfg, "service"),
		MetricName:  configValue(cfg, "metric_name"),
		Severity:    configValue(cfg, "severity"),
		Category:    configValue(cfg, "category"),
		Environment: configValue(cfg, "environment"),
		Source:      configValue(cfg, "source"),
	}

	messageRaw := vendorutil.Str(envelope, "Message")
	var messageObj map[string]any
	if err := jsonnum.Unmarshal([]byte(messageRaw), &messageObj); err != nil {
		// SNS Message isn't valid JSON -- faithfully still produces a real
		// alert (not a rejected request), matching the reference script's
		// own fallback exactly.
		base.MetricName = "SNS Message Parse Error"
		base.Description = "Raw Payload Message: " + prettyJSON(raw)
		return base, nil
	}

	// AlarmDescription optionally carries a JSON object of field
	// overrides. Invalid JSON there degrades to no overrides at all
	// (warn-and-continue in the reference script), not an error.
	alarmDesc := map[string]any{}
	if adRaw := strings.TrimSpace(vendorutil.Str(messageObj, "AlarmDescription")); adRaw != "" {
		var parsed map[string]any
		if err := jsonnum.Unmarshal([]byte(adRaw), &parsed); err == nil {
			alarmDesc = parsed
		}
	}

	// A recovered alarm (NewStateValue "OK") always forces severity "ok",
	// overriding whatever AlarmDescription's own severity override said.
	if vendorutil.Str(messageObj, "NewStateValue") == "OK" {
		alarmDesc["severity"] = "ok"
	}

	alert := Alert{
		Service:          vendorutil.FirstNonEmpty(vendorutil.Str(alarmDesc, "service"), base.Service),
		MetricName:       vendorutil.FirstNonEmpty(vendorutil.Str(messageObj, "AlarmName"), base.MetricName),
		Severity:         vendorutil.FirstNonEmpty(vendorutil.Str(alarmDesc, "severity"), base.Severity),
		Category:         vendorutil.FirstNonEmpty(vendorutil.Str(alarmDesc, "category"), base.Category),
		Environment:      vendorutil.FirstNonEmpty(vendorutil.Str(alarmDesc, "environment"), base.Environment),
		Source:           base.Source,
		UniqueIdentifier: vendorutil.Str(messageObj, "AlarmArn"),
		Description:      prettyJSON([]byte(messageRaw)),
	}
	return alert, nil
}

// configValue applies the 2-tier resolution: operator config, then the
// hardcoded default for the field. There is no payload tier at this
// stage -- payload-level overrides come from the alarm description JSON,
// resolved separately in Transform.
func configValue(cfg Config, field string) string {
	return vendorutil.FirstNonEmpty(cfg[field], defaults[field])
}

// prettyJSON re-indents raw JSON bytes with a 2-space indent, matching the
// reference script's JSON.stringify(x, null, 2) formatting. Operating on
// the raw bytes (rather than unmarshal-then-remarshal through a Go map)
// preserves the original field order, since Go maps have none.
func prettyJSON(raw []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}
