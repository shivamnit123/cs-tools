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

// Package site24x7 transforms an inbound Site24x7 monitor alert payload
// into the canonical alert JSON that the core component accepts. It is a
// Go port of the ServiceNow Site24x7 Scripted REST Resource, which -
// unlike every other vendor here - has no separate Script Include and no
// hardcoded field defaults at all: every fallback comes from the operator
// config or is left empty.
package site24x7

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"sre-alert-ingestion-service/internal/vendors/jsonnum"
	"sre-alert-ingestion-service/internal/vendors/vendorutil"
)

// source identifies alerts produced by this adapter. Unlike every other
// vendor, this is not overridable via config -- the reference script
// hardcodes it with no config tier at all.
const source = "Site24x7"

// severityMap maps Site24x7's monitor status to the canonical severity
// labels the core component expects.
var severityMap = map[string]string{
	"DOWN":     "Critical",
	"CRITICAL": "Major",
	"TROUBLE":  "Minor",
	"UP":       "OK",
}

// ErrMissingBody is returned when the webhook is called with no body at all.
var ErrMissingBody = errors.New("MISSING REQUEST BODY DATA")

// ErrMissingStatus is returned when neither the payload's STATUS field nor
// a configured Defaults.Severity is present to resolve one.
var ErrMissingStatus = errors.New("MISSING REQUIRED FIELD: STATUS")

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

// Config is the operator-supplied "edge.api.site24x7.alert.config" system
// property, reproduced exactly:
//
//   - TagList maps a canonical field name ("Service", "Category",
//     "Environment") to the tag prefix Site24x7 sends it under (e.g.
//     {"Service": "svc"} extracts the value from a "svc:my-app" tag).
//     There is no hardcoded set of prefixes -- an unconfigured TagList
//     means no tags are ever extracted.
//   - Defaults supplies a fallback value per canonical field name,
//     including "Severity" (used both as the STATUS fallback and as the
//     final severity fallback when a status doesn't map).
//
// There is no further, hardcoded fallback beyond these two tiers -- unlike
// every other vendor, an unconfigured field simply resolves to "".
type Config struct {
	TagList  map[string]string `json:"TagList"`
	Defaults map[string]string `json:"Defaults"`
}

// LoadConfig reads the Config from the SITE24X7_ALERT_CONFIG env var (a
// JSON object shaped like the real system property:
// {"TagList":{...},"Defaults":{...}}).
func LoadConfig() (Config, error) {
	raw := os.Getenv("SITE24X7_ALERT_CONFIG")
	if strings.TrimSpace(raw) == "" {
		return Config{}, nil
	}
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return Config{}, fmt.Errorf("invalid SITE24X7_ALERT_CONFIG: %w", err)
	}
	return cfg, nil
}

// Transform parses a raw inbound Site24x7 alert and produces the canonical
// Alert. cfg supplies the tag-prefix mapping and defaults.
func Transform(raw []byte, cfg Config) (Alert, error) {
	if strings.TrimSpace(string(raw)) == "" {
		return Alert{}, ErrMissingBody
	}

	var payload map[string]any
	if err := jsonnum.Unmarshal(raw, &payload); err != nil {
		return Alert{}, fmt.Errorf("%w: %v", ErrMissingBody, err)
	}

	status := vendorutil.FirstNonEmpty(vendorutil.Str(payload, "STATUS"), cfg.Defaults["Severity"])
	if status == "" {
		return Alert{}, ErrMissingStatus
	}

	extracted := extractTags(strSlice(payload, "TAGS"), cfg.TagList)

	alert := Alert{
		Service:          vendorutil.FirstNonEmpty(extracted["Service"], cfg.Defaults["Service"]),
		MetricName:       vendorutil.Str(payload, "MONITORNAME"),
		Severity:         mapSeverity(status, cfg.Defaults["Severity"]),
		Category:         vendorutil.FirstNonEmpty(extracted["Category"], cfg.Defaults["Category"]),
		Environment:      vendorutil.FirstNonEmpty(extracted["Environment"], cfg.Defaults["Environment"]),
		Source:           source,
		UniqueIdentifier: vendorutil.Str(payload, "MONITOR_ID"),
		Description:      vendorutil.CompactJSON(raw),
	}
	return alert, nil
}

// extractTags scans each incoming tag string for a configured prefix and
// pulls the value that follows it. Faithfully reproduces a real quirk in
// the reference script: it splits each tag on every ":" and always takes
// index 1, so a tag with more than one colon (e.g. "svc:my:service")
// silently loses everything after the second segment ("my", not
// "my:service").
func extractTags(tags []string, tagList map[string]string) map[string]string {
	result := make(map[string]string)
	for _, tag := range tags {
		for field, prefix := range tagList {
			p := prefix + ":"
			if !strings.HasPrefix(tag, p) {
				continue
			}
			parts := strings.Split(tag, ":")
			if len(parts) > 1 {
				result[field] = strings.TrimSpace(parts[1])
			} else {
				result[field] = ""
			}
		}
	}
	return result
}

// mapSeverity maps a Site24x7 monitor status to a canonical severity. An
// unrecognized status falls back to the configured default severity
// rather than passing the raw status value through, unlike most other
// vendors.
func mapSeverity(status, defaultSeverity string) string {
	return vendorutil.FirstNonEmpty(severityMap[status], defaultSeverity)
}

// strSlice reads a payload field expected to be a JSON array of strings.
func strSlice(payload map[string]any, key string) []string {
	raw, ok := payload[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
