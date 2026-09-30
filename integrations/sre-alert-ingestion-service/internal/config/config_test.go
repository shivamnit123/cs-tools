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

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad_MissingFileUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg != Defaults() {
		t.Errorf("cfg = %+v, want defaults", cfg)
	}
}

func TestLoad_ExampleMatchesDefaults(t *testing.T) {
	cfg, err := Load("../../config.toml.example")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg != Defaults() {
		t.Errorf("config.toml.example = %+v, want it to match Defaults() %+v", cfg, Defaults())
	}
}

func TestLoad_PartialFileOverridesFieldByField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[allocator]\nmax_batch = 50\n[store]\nquery_timeout = \"3s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Allocator.MaxBatch != 50 {
		t.Errorf("MaxBatch = %d, want 50", cfg.Allocator.MaxBatch)
	}
	if cfg.Store.QueryTimeout.Duration() != 3*time.Second {
		t.Errorf("QueryTimeout = %v, want 3s", cfg.Store.QueryTimeout.Duration())
	}
	if cfg.Allocator.QueueSize != Defaults().Allocator.QueueSize {
		t.Errorf("QueueSize = %d, want untouched default", cfg.Allocator.QueueSize)
	}
}

func TestLoad_RejectsInvalidValues(t *testing.T) {
	cases := map[string]string{
		"zero batch":        "[allocator]\nmax_batch = 0\n",
		"negative timeout":  "[store]\nquery_timeout = \"-1s\"\n",
		"empty auth mode":   "[auth]\nmode = \"\"\n",
		"bad duration":      "[wake]\ntimeout = \"soon\"\n",
		"zero idle":         "[server]\nidle_timeout = \"0s\"\n",
		"zero drain delay":  "[server]\ndrain_delay = \"0s\"\n",
		"budget over grace": "[server]\nshutdown_grace = \"10s\"\n",
		"steps over grace":  "[server]\nrequest_wait = \"20s\"\n",
		"wait near write":   "[server]\nwrite_timeout = \"10500ms\"\n",
		"deadline at gap":   "[store]\nwrite_deadline = \"10m\"\n",
		"zero deadline":     "[store]\nwrite_deadline = \"0s\"\n",
		"queue bytes small": "[allocator]\nqueue_max_bytes = 1048576\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Error("Load should fail")
			}
		})
	}
}

func TestLoadEnv(t *testing.T) {
	t.Setenv("PORT", "")
	t.Setenv("ALERT_CORE_WAKE_URL", " http://core/alertz ")
	t.Setenv("FALLBACK_CHAT_WEBHOOK_URLS", "https://a, ,https://b ")
	e, err := LoadEnv()
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	if e.Port != "8080" {
		t.Errorf("Port = %q, want 8080 default", e.Port)
	}
	if e.WakeURL != "http://core/alertz" {
		t.Errorf("WakeURL = %q", e.WakeURL)
	}
	if len(e.ChatWebhookURLs) != 2 || e.ChatWebhookURLs[0] != "https://a" || e.ChatWebhookURLs[1] != "https://b" {
		t.Errorf("ChatWebhookURLs = %q", e.ChatWebhookURLs)
	}
}
