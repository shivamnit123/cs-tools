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

// Package cassandra handles Cosmos DB for Apache Cassandra connection setup, TLS sessions, and sequence-id counter helpers.
package cassandra

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/gocql/gocql"
)

// Config holds Cassandra connection settings read from CASSANDRA_* env vars at startup.
type Config struct {
	ContactPoint string `env:"CASSANDRA_CONTACT_POINT,notEmpty"`
	Port         int    `env:"CASSANDRA_PORT" envDefault:"10350"`
	Keyspace     string `env:"CASSANDRA_KEYSPACE,notEmpty"`
	Username     string `env:"CASSANDRA_USERNAME"`
	Key          string `env:"CASSANDRA_KEY,notEmpty"`
}

// ConfigFromEnv defaults Username to the contact point's leading DNS label, matching Cosmos's account-name convention.
func ConfigFromEnv() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("cassandra config: %w", err)
	}

	if cfg.Username == "" {
		cfg.Username = strings.SplitN(cfg.ContactPoint, ".", 2)[0]
	}

	return cfg, nil
}

// Connect disables peer/token-ring discovery since Cosmos DB is a single proxy endpoint.
func Connect(cfg Config, connectTimeout, queryTimeout time.Duration) (*gocql.Session, error) {
	cluster := gocql.NewCluster(cfg.ContactPoint)
	cluster.Port = cfg.Port
	cluster.Keyspace = cfg.Keyspace
	cluster.Authenticator = gocql.PasswordAuthenticator{Username: cfg.Username, Password: cfg.Key}
	cluster.SslOpts = &gocql.SslOptions{Config: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.ContactPoint}}
	cluster.DisableInitialHostLookup = true
	cluster.Consistency = gocql.LocalQuorum
	cluster.ConnectTimeout = connectTimeout
	cluster.Timeout = queryTimeout

	session, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return session, nil
}

// SeedSeq uses IF NOT EXISTS so repeated calls on every startup stay idempotent.
func SeedSeq(ctx context.Context, session *gocql.Session, seqTable string) error {
	_, err := session.Query(
		fmt.Sprintf(`INSERT INTO %s (name, seq) VALUES (?, 0) IF NOT EXISTS`, seqTable),
		seqTable,
	).WithContext(ctx).MapScanCAS(map[string]any{})
	if err != nil {
		return fmt.Errorf("seed %s: %w", seqTable, err)
	}
	return nil
}

// formatSeq renders prefix plus a zero-padded, width-digit number, e.g. formatSeq("ALT",9,123) -> "ALT000000123".
func formatSeq(prefix string, width int, n int64) string {
	return fmt.Sprintf("%s%0*d", prefix, width, n)
}

// FormatSeq exposes formatSeq for callers needing to derive an id string without allocating a new sequence.
func FormatSeq(prefix string, width int, n int64) string {
	return formatSeq(prefix, width, n)
}

// ReadSeq reads a counter's current value for inspection only; it never advances the sequence.
func ReadSeq(ctx context.Context, session *gocql.Session, seqTable string) (int64, error) {
	var seq int64
	if err := session.Query(
		fmt.Sprintf(`SELECT seq FROM %s WHERE name = ?`, seqTable), seqTable,
	).WithContext(ctx).Scan(&seq); err != nil {
		return 0, fmt.Errorf("read %s: %w", seqTable, err)
	}
	return seq, nil
}

// AdvanceSeqTo CAS-moves a counter from an expected value to a target; the bool reports whether this call won the race.
func AdvanceSeqTo(ctx context.Context, session *gocql.Session, seqTable string, from, to int64) (bool, error) {
	applied, err := session.Query(
		fmt.Sprintf(`UPDATE %s SET seq = ? WHERE name = ? IF seq = ?`, seqTable),
		to, seqTable, from,
	).WithContext(ctx).MapScanCAS(map[string]any{})
	if err != nil {
		return false, fmt.Errorf("advance %s to %d: %w", seqTable, to, err)
	}
	return applied, nil
}
