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

// Package config loads deployment tunables from config.toml and endpoints/secrets from the
// environment, validating every value before returning it. The config.toml handling mirrors
// sre-alert-core-service: built-in defaults, overridden field by field by the file if present.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/caarlos0/env/v11"
)

// DefaultPath is used when CONFIG_PATH is unset; expected at the working directory root.
const DefaultPath = "config.toml"

// MaxWriteDeadline is alerts-core's gap_timeout. A claimed id still being retried when
// alerts-core gives up on it would be skipped, so write_deadline must stay under it.
const MaxWriteDeadline = Duration(10 * time.Minute)

// WriteMargin keeps request_wait under write_timeout, so a slow store answers 503 instead of
// the connection being cut.
const WriteMargin = Duration(time.Second)

// Config groups every deployment tunable by the subsystem it configures.
type Config struct {
	Server    ServerConfig    `toml:"server"`
	Auth      AuthConfig      `toml:"auth"`
	Allocator AllocatorConfig `toml:"allocator"`
	Store     StoreConfig     `toml:"store"`
	Cassandra CassandraConfig `toml:"cassandra"`
	Wake      WakeConfig      `toml:"wake"`
	Reject    RejectConfig    `toml:"reject"`
	Fallback  FallbackConfig  `toml:"fallback"`
}

// ServerConfig tunes the HTTP server: shutdown drain window, read/write timeouts, and the
// request body limit beyond which a webhook is answered 413.
type ServerConfig struct {
	ShutdownGrace  Duration `toml:"shutdown_grace"`
	DrainDelay     Duration `toml:"drain_delay"`
	RequestWait    Duration `toml:"request_wait"`
	AllocatorDrain Duration `toml:"allocator_drain"`
	ReadTimeout    Duration `toml:"read_timeout"`
	WriteTimeout   Duration `toml:"write_timeout"`
	IdleTimeout    Duration `toml:"idle_timeout"`
	MaxBodyBytes   int64    `toml:"max_body_bytes"`
}

// AuthConfig selects the auth hook implementation. Only "none" exists today.
type AuthConfig struct {
	Mode string `toml:"mode"`
}

// AllocatorConfig tunes the id allocator: queue depth before 503, alerts claimed
// per compare-and-set, parallel row writers, and compare-and-set attempts before giving up.
type AllocatorConfig struct {
	QueueSize        int   `toml:"queue_size"`
	QueueMaxBytes    int64 `toml:"queue_max_bytes"`
	MaxBatch         int   `toml:"max_batch"`
	WriteConcurrency int   `toml:"write_concurrency"`
	ClaimMaxAttempts int   `toml:"claim_max_attempts"`
}

// StoreConfig tunes row writes: attempts on the same id, the base of the doubling backoff
// between them, and the per-query timeout.
type StoreConfig struct {
	InsertAttempts  int      `toml:"insert_attempts"`
	InsertBaseDelay Duration `toml:"insert_base_delay"`
	QueryTimeout    Duration `toml:"query_timeout"`
	ClaimTimeout    Duration `toml:"claim_timeout"`
	WriteDeadline   Duration `toml:"write_deadline"`
}

// CassandraConfig tunes startup connection retry, matching sre-alert-core-service.
type CassandraConfig struct {
	ConnectMaxAttempts int      `toml:"connect_max_attempts"`
	ConnectBaseDelay   Duration `toml:"connect_base_delay"`
	ConnectTimeout     Duration `toml:"connect_timeout"`
}

// WakeConfig bounds the fire-and-forget POST /alert to alerts-core.
type WakeConfig struct {
	Timeout Duration `toml:"timeout"`
}

// RejectConfig tunes the rejected-webhook Chat card: the per vendor+error
// rate-limit window and how much of the body the card previews.
type RejectConfig struct {
	Window           Duration `toml:"window"`
	BodyPreviewChars int      `toml:"body_preview_chars"`
}

// FallbackConfig rate-limits the DB-failure Chat card.
type FallbackConfig struct {
	CardsPerMinute int `toml:"cards_per_minute"`
}

// Duration wraps time.Duration so TOML values like "30s" decode via time.ParseDuration.
type Duration time.Duration

// UnmarshalText implements encoding.TextUnmarshaler for quoted duration strings.
func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", text, err)
	}
	*d = Duration(parsed)
	return nil
}

// Duration unwraps to a plain time.Duration.
func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

// Defaults returns every tunable's production value, matching config.toml.example.
func Defaults() Config {
	return Config{
		Server: ServerConfig{
			ShutdownGrace:  Duration(25 * time.Second),
			DrainDelay:     Duration(5 * time.Second),
			RequestWait:    Duration(10 * time.Second),
			AllocatorDrain: Duration(10 * time.Second),
			ReadTimeout:    Duration(10 * time.Second),
			WriteTimeout:   Duration(30 * time.Second),
			IdleTimeout:    Duration(60 * time.Second),
			MaxBodyBytes:   1 << 20,
		},
		Auth: AuthConfig{Mode: "none"},
		Allocator: AllocatorConfig{
			QueueSize:        5000,
			QueueMaxBytes:    256 << 20,
			MaxBatch:         200,
			WriteConcurrency: 16,
			ClaimMaxAttempts: 20,
		},
		Store: StoreConfig{
			InsertAttempts:  5,
			InsertBaseDelay: Duration(250 * time.Millisecond),
			QueryTimeout:    Duration(1500 * time.Millisecond),
			ClaimTimeout:    Duration(5 * time.Second),
			WriteDeadline:   Duration(5 * time.Minute),
		},
		Cassandra: CassandraConfig{
			ConnectMaxAttempts: 5,
			ConnectBaseDelay:   Duration(2 * time.Second),
			ConnectTimeout:     Duration(10 * time.Second),
		},
		Wake:     WakeConfig{Timeout: Duration(2 * time.Second)},
		Reject:   RejectConfig{Window: Duration(15 * time.Minute), BodyPreviewChars: 500},
		Fallback: FallbackConfig{CardsPerMinute: 5},
	}
}

// Load falls back to CONFIG_PATH, then DefaultPath, when path is empty. Values start at
// Defaults and a present file overrides them field by field; a missing file is not an error.
func Load(path string) (Config, error) {
	if path == "" {
		path = os.Getenv("CONFIG_PATH")
	}
	if path == "" {
		path = DefaultPath
	}

	cfg := Defaults()
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		if !os.IsNotExist(err) {
			return Config{}, fmt.Errorf("load config %s: %w", path, err)
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %s: %w", path, err)
	}
	return cfg, nil
}

// Validate rejects zero/negative tunables that would silently disable limits, retries or timeouts.
func (c Config) Validate() error {
	switch {
	case c.Server.ShutdownGrace <= 0:
		return fmt.Errorf("server.shutdown_grace must be positive")
	case c.Server.DrainDelay <= 0:
		return fmt.Errorf("server.drain_delay must be positive")
	case c.Server.RequestWait <= 0:
		return fmt.Errorf("server.request_wait must be positive")
	case c.Server.AllocatorDrain <= 0:
		return fmt.Errorf("server.allocator_drain must be positive")
	case c.Server.DrainDelay+c.Server.RequestWait+c.Server.AllocatorDrain > c.Server.ShutdownGrace:
		return fmt.Errorf("server.drain_delay + request_wait + allocator_drain must not exceed shutdown_grace")
	case c.Server.ReadTimeout <= 0:
		return fmt.Errorf("server.read_timeout must be positive")
	case c.Server.WriteTimeout <= 0:
		return fmt.Errorf("server.write_timeout must be positive")
	case c.Server.RequestWait+WriteMargin > c.Server.WriteTimeout:
		return fmt.Errorf("server.request_wait must be at least %v below write_timeout", WriteMargin.Duration())
	case c.Server.IdleTimeout <= 0:
		return fmt.Errorf("server.idle_timeout must be positive")
	case c.Server.MaxBodyBytes <= 0:
		return fmt.Errorf("server.max_body_bytes must be positive")
	case c.Auth.Mode == "":
		return fmt.Errorf("auth.mode must be set")
	case c.Allocator.QueueSize <= 0:
		return fmt.Errorf("allocator.queue_size must be positive")
	case c.Allocator.QueueMaxBytes <= 0:
		return fmt.Errorf("allocator.queue_max_bytes must be positive")
	case c.Allocator.QueueMaxBytes < 2*c.Server.MaxBodyBytes:
		return fmt.Errorf("allocator.queue_max_bytes must be at least 2 x server.max_body_bytes")
	case c.Allocator.MaxBatch <= 0:
		return fmt.Errorf("allocator.max_batch must be positive")
	case c.Allocator.WriteConcurrency <= 0:
		return fmt.Errorf("allocator.write_concurrency must be positive")
	case c.Allocator.ClaimMaxAttempts <= 0:
		return fmt.Errorf("allocator.claim_max_attempts must be positive")
	case c.Store.InsertAttempts <= 0:
		return fmt.Errorf("store.insert_attempts must be positive")
	case c.Store.InsertBaseDelay <= 0:
		return fmt.Errorf("store.insert_base_delay must be positive")
	case c.Store.QueryTimeout <= 0:
		return fmt.Errorf("store.query_timeout must be positive")
	case c.Store.ClaimTimeout <= 0:
		return fmt.Errorf("store.claim_timeout must be positive")
	case c.Store.WriteDeadline <= 0 || c.Store.WriteDeadline >= MaxWriteDeadline:
		return fmt.Errorf("store.write_deadline must be positive and under %v (alerts-core's gap_timeout)", MaxWriteDeadline.Duration())
	case c.Cassandra.ConnectMaxAttempts <= 0:
		return fmt.Errorf("cassandra.connect_max_attempts must be positive")
	case c.Cassandra.ConnectBaseDelay <= 0:
		return fmt.Errorf("cassandra.connect_base_delay must be positive")
	case c.Cassandra.ConnectTimeout <= 0:
		return fmt.Errorf("cassandra.connect_timeout must be positive")
	case c.Wake.Timeout <= 0:
		return fmt.Errorf("wake.timeout must be positive")
	case c.Reject.Window <= 0:
		return fmt.Errorf("reject.window must be positive")
	case c.Reject.BodyPreviewChars <= 0:
		return fmt.Errorf("reject.body_preview_chars must be positive")
	case c.Fallback.CardsPerMinute <= 0:
		return fmt.Errorf("fallback.cards_per_minute must be positive")
	}
	return nil
}

// Env holds the endpoints read from the environment. CASSANDRA_* and the
// per-vendor <VENDOR>_ALERT_CONFIG variables are read by their own packages.
type Env struct {
	Port string `env:"PORT" envDefault:"8080"`
	// WakeURL is alerts-core's POST /alert. Empty disables the wake-up (local dev); the
	// 10-second poll on alerts-core still picks the alerts up.
	WakeURL string `env:"ALERT_CORE_WAKE_URL"`
	// ChatWebhookURLs are Google Chat incoming webhooks for rejected-webhook and DB-failure
	// cards. Empty disables the cards (local dev); rejections and failures are still logged.
	ChatWebhookURLs []string `env:"FALLBACK_CHAT_WEBHOOK_URLS" envSeparator:","`
}

// LoadEnv parses Env, trimming blanks out of the comma-separated Chat webhook list.
func LoadEnv() (Env, error) {
	var e Env
	if err := env.Parse(&e); err != nil {
		return Env{}, fmt.Errorf("env config: %w", err)
	}
	e.WakeURL = strings.TrimSpace(e.WakeURL)
	urls := e.ChatWebhookURLs[:0]
	for _, u := range e.ChatWebhookURLs {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	e.ChatWebhookURLs = urls
	return e, nil
}
