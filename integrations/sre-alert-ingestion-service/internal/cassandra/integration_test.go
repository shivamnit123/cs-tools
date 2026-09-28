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

//go:build integration

// Integration tests against a local Cassandra container, never Cosmos DB:
//
//	docker run -d --name ingestion-cassandra -p 19042:9042 cassandra:4.1
//	CASSANDRA_TEST_PORT=19042 go test -race -tags integration ./internal/cassandra/
//
// Each run creates its own throwaway keyspace and drops it afterwards.
package cassandra_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gocql/gocql"

	"sre-alert-ingestion-service/internal/allocator"
	"sre-alert-ingestion-service/internal/cassandra"
	"sre-alert-ingestion-service/internal/model"
)

// schema matches sre-alert-core-service's schema.cql for the two tables this service writes,
// without the cosmosdb_* table options a plain Cassandra rejects.
var schema = []string{
	`CREATE TABLE IF NOT EXISTS alerts (id text PRIMARY KEY, vendor text, alert text, created_at timestamp)`,
	`CREATE TABLE IF NOT EXISTS alert_seq (name text PRIMARY KEY, seq bigint)`,
}

func testStore(t *testing.T) *cassandra.Store {
	t.Helper()
	host := envOr("CASSANDRA_TEST_HOST", "127.0.0.1")
	port, _ := strconv.Atoi(envOr("CASSANDRA_TEST_PORT", "9042"))
	keyspace := fmt.Sprintf("ingestion_it_%d", rand.Uint32())
	if keyspace == "alertintegration" {
		t.Fatal("refusing to use the production keyspace")
	}

	admin := gocql.NewCluster(host)
	admin.Port = port
	admin.Timeout = 30 * time.Second
	admin.ConnectTimeout = 30 * time.Second
	adminSession, err := admin.CreateSession()
	if err != nil {
		t.Skipf("no local Cassandra at %s:%d: %v", host, port, err)
	}
	if err := adminSession.Query(fmt.Sprintf(
		`CREATE KEYSPACE %s WITH replication = {'class':'SimpleStrategy','replication_factor':1}`, keyspace)).Exec(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = adminSession.Query(`DROP KEYSPACE IF EXISTS ` + keyspace).Exec()
		adminSession.Close()
	})

	session, err := cassandra.Connect(cassandra.Config{
		ContactPoint: host, Port: port, Keyspace: keyspace, DisableTLS: true,
	}, 30*time.Second, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	for _, stmt := range schema {
		if err := session.Query(stmt).Exec(); err != nil {
			t.Fatal(err)
		}
	}
	return cassandra.NewStore(session, 10*time.Second, 10*time.Second)
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func TestIntegration_SeedCompareAndSetInsertReadBack(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.SeedSeq(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedSeq(ctx); err != nil { // idempotent, like alerts-core's
		t.Fatal(err)
	}
	if seq, err := s.ReadSeq(ctx); err != nil || seq != 0 {
		t.Fatalf("ReadSeq = %d, %v; want 0", seq, err)
	}

	applied, cur, err := s.CompareAndSet(ctx, 0, 5)
	if err != nil || !applied || cur != 5 {
		t.Fatalf("CAS 0->5 = %v, %d, %v", applied, cur, err)
	}
	applied, cur, err = s.CompareAndSet(ctx, 0, 3)
	if err != nil || applied || cur != 5 {
		t.Fatalf("stale CAS 0->3 = %v, %d, %v; want rejected with current 5", applied, cur, err)
	}

	id := cassandra.FormatID(1)
	if ok, err := s.Exists(ctx, id); err != nil || ok {
		t.Fatalf("Exists before insert = %v, %v", ok, err)
	}
	if err := s.Insert(ctx, id, "aws", `{"service":"svc"}`); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Exists(ctx, id); err != nil || !ok {
		t.Fatalf("Exists after insert = %v, %v", ok, err)
	}
}

func TestIntegration_InsertFillerNeverOverwrites(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	stored := cassandra.FormatID(1)
	if err := s.Insert(ctx, stored, "aws", `{"service":"svc"}`); err != nil {
		t.Fatal(err)
	}
	applied, existing, err := s.InsertFiller(ctx, stored, "aws", "VOID: test")
	if err != nil || applied || existing != `{"service":"svc"}` {
		t.Fatalf("filler over a stored alert = %v, %q, %v; want not applied, alert kept", applied, existing, err)
	}
	applied, _, err = s.InsertFiller(ctx, cassandra.FormatID(2), "aws", "VOID: test")
	if err != nil || !applied {
		t.Fatalf("filler on an empty id = %v, %v; want applied", applied, err)
	}
}

func TestIntegration_AllocatorWritesConsecutiveReadableRows(t *testing.T) {
	s := testStore(t)
	if err := s.SeedSeq(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := allocator.New(slog.New(slog.NewTextHandler(io.Discard, nil)), s, nil, nil, allocator.Config{
		QueueSize: 1000, MaxBatch: 200, WriteConcurrency: 32, ClaimMaxAttempts: 20,
		InsertAttempts: 3, InsertBaseDelay: 100 * time.Millisecond, ClaimJitter: 10 * time.Millisecond,
	})

	const total = 200
	ids := make([]string, total)
	var wg sync.WaitGroup
	for i := range total {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := a.Submit(context.Background(), "prometheus", "req", []model.Alert{{
				Service: "svc", MetricName: "m" + strconv.Itoa(i), Severity: "critical", Source: "Prometheus",
			}})
			if err != nil {
				t.Errorf("Submit: %v", err)
				return
			}
			ids[i] = got[0]
		}()
	}
	wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	for n := int64(1); n <= total; n++ {
		id := cassandra.FormatID(n)
		if !seen[id] {
			t.Fatalf("id %s never issued", id)
		}
		if ok, err := s.Exists(context.Background(), id); err != nil || !ok {
			t.Fatalf("row %s missing: %v", id, err)
		}
	}
	if seq, err := s.ReadSeq(context.Background()); err != nil || seq != total {
		t.Errorf("alert_seq = %d, %v; want %d", seq, err, total)
	}
}

// TestIntegration_RowParsesAsAlertsCoreAlert reads a row back the way alerts-core does.
func TestIntegration_RowParsesAsAlertsCoreAlert(t *testing.T) {
	s := testStore(t)
	if err := s.SeedSeq(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := allocator.New(slog.New(slog.NewTextHandler(io.Discard, nil)), s, nil, nil, allocator.Config{
		QueueSize: 10, MaxBatch: 10, WriteConcurrency: 4, ClaimMaxAttempts: 5,
		InsertAttempts: 3, InsertBaseDelay: 100 * time.Millisecond,
	})
	defer a.Close(context.Background())

	in := model.Alert{Service: "svc", MetricName: "HighCPU", Severity: "Critical", Category: "service_interruption",
		Environment: "production", Source: "Datadog", UniqueIdentifier: "148502937", Description: "{}"}
	ids, err := a.Submit(context.Background(), "datadog", "req", []model.Alert{in})
	if err != nil {
		t.Fatal(err)
	}
	var raw, vendor string
	if err := testSession(t, s).Query(`SELECT alert, vendor FROM alerts WHERE id = ?`, ids[0]).Scan(&raw, &vendor); err != nil {
		t.Fatal(err)
	}
	var out model.Alert
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("row doesn't parse: %v", err)
	}
	if out != in || vendor != "datadog" {
		t.Errorf("row = %+v (vendor %s), want %+v", out, vendor, in)
	}
}

func testSession(t *testing.T, s *cassandra.Store) *gocql.Session {
	t.Helper()
	return cassandra.SessionForTest(s)
}
