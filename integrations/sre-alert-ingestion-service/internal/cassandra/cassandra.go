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

// Package cassandra connects to Cosmos DB for Apache Cassandra and implements the two writes
// this service owns: claiming ids on alert_seq and writing alerts rows. Connection settings
// and id formatting match sre-alert-core-service exactly; it is the reader of both tables.
// This package never touches alert_cursor, incidents_processed, incidents_pending or
// processor_lease.
package cassandra

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/gocql/gocql"
)

const (
	// SeqTable and SeqName: alerts-core keys the alert_seq row by the table's own name.
	SeqTable = "alert_seq"
	SeqName  = "alert_seq"
	// IDPrefix and IDWidth must match alerts-core's poller, which rebuilds ids from sequence
	// numbers: an id in any other format is never read.
	IDPrefix = "ALT"
	IDWidth  = 9
)

// FormatID renders a sequence number as an alert id, e.g. 123 -> "ALT000000123".
func FormatID(seq int64) string {
	return fmt.Sprintf("%s%0*d", IDPrefix, IDWidth, seq)
}

// Config holds connection settings from CASSANDRA_* env vars, identical to alerts-core's.
type Config struct {
	ContactPoint string `env:"CASSANDRA_CONTACT_POINT,notEmpty"`
	Port         int    `env:"CASSANDRA_PORT" envDefault:"10350"`
	Keyspace     string `env:"CASSANDRA_KEYSPACE,notEmpty"`
	Username     string `env:"CASSANDRA_USERNAME"`
	Key          string `env:"CASSANDRA_KEY,notEmpty"`
	// DisableTLS is for local integration tests against a plain Cassandra container only;
	// Cosmos DB always requires TLS. Not read from the environment.
	DisableTLS bool `env:"-"`
}

// ConfigFromEnv defaults Username to the contact point's leading DNS label, matching Cosmos's
// account-name convention.
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
	if !cfg.DisableTLS {
		cluster.SslOpts = &gocql.SslOptions{Config: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.ContactPoint}}
	}
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

// Store implements the allocator's storage operations against one session. alert_seq calls
// are bounded by claimTimeout, row calls by queryTimeout, on top of the caller's context.
type Store struct {
	session      *gocql.Session
	queryTimeout time.Duration
	claimTimeout time.Duration
}

// NewStore returns a Store over session.
func NewStore(session *gocql.Session, queryTimeout, claimTimeout time.Duration) *Store {
	return &Store{session: session, queryTimeout: queryTimeout, claimTimeout: claimTimeout}
}

// SeedSeq creates the alert_seq row at 0 if it doesn't exist, with the same idempotent
// statement alerts-core runs at startup, so either service can start first.
func (s *Store) SeedSeq(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.claimTimeout)
	defer cancel()
	if _, err := s.session.Query(
		fmt.Sprintf(`INSERT INTO %s (name, seq) VALUES (?, 0) IF NOT EXISTS`, SeqTable), SeqName,
	).WithContext(ctx).MapScanCAS(map[string]any{}); err != nil {
		return fmt.Errorf("seed %s: %w", SeqTable, err)
	}
	return nil
}

// ReadSeq returns alert_seq's current value.
func (s *Store) ReadSeq(ctx context.Context) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, s.claimTimeout)
	defer cancel()
	var seq int64
	iter := s.session.Query(
		fmt.Sprintf(`SELECT seq FROM %s WHERE name = ?`, SeqTable), SeqName,
	).WithContext(ctx).Iter()
	recordRU(ctx, iter)
	found := iter.Scan(&seq)
	if err := iter.Close(); err != nil {
		return 0, fmt.Errorf("read %s: %w", SeqTable, err)
	}
	if !found {
		return 0, fmt.Errorf("read %s: %w", SeqTable, gocql.ErrNotFound)
	}
	return seq, nil
}

// ErrSeqMissing means a compare-and-set was rejected because the alert_seq row doesn't exist.
var ErrSeqMissing = errors.New("alert_seq row missing")

// CompareAndSet moves alert_seq from `from` to `to`. When it's rejected, current is the value
// the row actually holds, returned with the rejection so the caller can retry without
// another read.
func (s *Store) CompareAndSet(ctx context.Context, from, to int64) (applied bool, current int64, err error) {
	ctx, cancel := context.WithTimeout(ctx, s.claimTimeout)
	defer cancel()
	prev := map[string]any{}
	applied, err = s.session.Query(
		fmt.Sprintf(`UPDATE %s SET seq = ? WHERE name = ? IF seq = ?`, SeqTable), to, SeqName, from,
	).WithContext(ctx).MapScanCAS(prev)
	if err != nil {
		return false, 0, fmt.Errorf("advance %s %d -> %d: %w", SeqTable, from, to, err)
	}
	if applied {
		return true, to, nil
	}
	seq, ok := prev["seq"].(int64)
	if !ok {
		return false, 0, ErrSeqMissing
	}
	return false, seq, nil
}

// Insert writes one alerts row. created_at is informational; alerts-core orders by id.
func (s *Store) Insert(ctx context.Context, id, vendor, alert string) error {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	iter := s.session.Query(
		`INSERT INTO alerts (id, vendor, alert, created_at) VALUES (?, ?, ?, ?)`,
		id, vendor, alert, time.Now().UTC(),
	).WithContext(ctx).Iter()
	recordRU(ctx, iter)
	if err := iter.Close(); err != nil {
		return fmt.Errorf("insert %s: %w", id, err)
	}
	return nil
}

// InsertFiller writes a filler row with IF NOT EXISTS, so it never overwrites an alert that
// did land. When not applied, existing is the row's alert column.
func (s *Store) InsertFiller(ctx context.Context, id, vendor, filler string) (applied bool, existing string, err error) {
	ctx, cancel := context.WithTimeout(ctx, s.claimTimeout)
	defer cancel()
	prev := map[string]any{}
	applied, err = s.session.Query(
		`INSERT INTO alerts (id, vendor, alert, created_at) VALUES (?, ?, ?, ?) IF NOT EXISTS`,
		id, vendor, filler, time.Now().UTC(),
	).WithContext(ctx).MapScanCAS(prev)
	if err != nil {
		return false, "", fmt.Errorf("insert filler %s: %w", id, err)
	}
	existing, _ = prev["alert"].(string)
	return applied, existing, nil
}

// Exists reports whether the alerts row for id is readable. Cosmos DB can briefly hide a
// fresh write from a read, so a row isn't reported stored until this sees it.
func (s *Store) Exists(ctx context.Context, id string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	var got string
	iter := s.session.Query(`SELECT id FROM alerts WHERE id = ?`, id).WithContext(ctx).Iter()
	recordRU(ctx, iter)
	found := iter.Scan(&got)
	if err := iter.Close(); err != nil {
		return false, fmt.Errorf("read back %s: %w", id, err)
	}
	return found, nil
}
