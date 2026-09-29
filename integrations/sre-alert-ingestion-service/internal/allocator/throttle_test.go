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

package allocator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"sre-alert-ingestion-service/internal/cassandra"
	"sre-alert-ingestion-service/internal/model"
)

func fastJitter(t *testing.T) {
	old := throttleJitter
	throttleJitter = time.Millisecond
	t.Cleanup(func() { throttleJitter = old })
}

func loggedAllocator(t *testing.T, store *fakeStore, n FailureNotifier, cfg Config) (*Allocator, func() string) {
	t.Helper()
	var buf bytes.Buffer
	var mu sync.Mutex
	a := New(slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &mu}, nil)), store, n, nil, cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.Close(ctx)
	})
	return a, func() string { mu.Lock(); defer mu.Unlock(); return buf.String() }
}

func TestThrottledWrite_StoredOnSameID(t *testing.T) {
	fastJitter(t)
	store := newFakeStore()
	store.throttleInserts = 20
	notifier := &recordingNotifier{}
	a := newTestAllocator(t, store, notifier, nil, testConfig())

	ids, err := a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", "u")})
	if err != nil {
		t.Fatalf("err = %v, want stored after throttling", err)
	}
	if n := store.inserts[ids[0]]; n != 21 {
		t.Errorf("insert calls = %d, want 20 throttled + 1 stored, all on the same id", n)
	}
	if row := store.rows[ids[0]]; strings.HasPrefix(row.alert, fillerPrefix) {
		t.Errorf("row = %q, want the real alert", row.alert)
	}
	if len(notifier.failures) != 0 {
		t.Errorf("failures = %+v, want none", notifier.failures)
	}
}

func TestThrottlingDoesNotUseInsertAttempts(t *testing.T) {
	fastJitter(t)
	store := newFakeStore()
	store.throttleInserts = 5
	store.failRealInsert = func(string) bool { return true }
	a := newTestAllocator(t, store, &recordingNotifier{}, nil, testConfig())

	ids, err := a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", "u")})
	if !errors.Is(err, ErrStoreFailed) {
		t.Fatalf("err = %v, want ErrStoreFailed", err)
	}
	if n := store.inserts[ids[0]]; n != 5+3+1 {
		t.Errorf("insert calls = %d, want 5 throttled + 3 failed attempts + 1 filler", n)
	}
}

func TestThrottlingPastDeadline_WritesFiller(t *testing.T) {
	fastJitter(t)
	store := newFakeStore()
	store.throttleAllInserts = true
	notifier := &recordingNotifier{}
	cfg := testConfig()
	cfg.WriteDeadline = 300 * time.Millisecond
	cfg.QueryTimeout = 50 * time.Millisecond
	a := newTestAllocator(t, store, notifier, nil, cfg)

	start := time.Now()
	ids, err := a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", "u")})
	if !errors.Is(err, ErrStoreFailed) {
		t.Fatalf("err = %v, want ErrStoreFailed", err)
	}
	if took := time.Since(start); took > cfg.WriteDeadline {
		t.Errorf("took %v, want inserts to stop 2 x query_timeout before the %v deadline", took, cfg.WriteDeadline)
	}
	row := store.rows[ids[0]]
	if !strings.HasPrefix(row.alert, fillerPrefix) || !strings.Contains(row.alert, "write deadline passed while Cosmos DB was throttling") {
		t.Errorf("row = %q, want the deadline filler", row.alert)
	}
	if len(notifier.failures) != 1 || !notifier.failures[0].FillerWritten {
		t.Errorf("failures = %+v, want one with FillerWritten", notifier.failures)
	}
	before := store.inserts[ids[0]]
	time.Sleep(50 * time.Millisecond)
	if after := store.inserts[ids[0]]; after != before {
		t.Errorf("inserts kept going after the deadline: %d -> %d", before, after)
	}
}

func TestThrottledCAS_RetriedWithoutGapWarning(t *testing.T) {
	fastJitter(t)
	store := newFakeStore()
	store.throttleCAS = 3
	a, logs := loggedAllocator(t, store, nil, testConfig())

	if _, err := a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", "u")}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if n := store.casCalls.Load(); n != 4 {
		t.Errorf("compare-and-sets = %d, want 3 throttled + 1 applied", n)
	}
	out := logs()
	if strings.Contains(out, "may have applied") {
		t.Errorf("a throttled compare-and-set must not warn about gaps:\n%s", out)
	}
	if !strings.Contains(out, "claim_attempts=1") {
		t.Errorf("throttled compare-and-sets must not count as claim attempts:\n%s", out)
	}
}

func TestClaimer_ClaimsOnlyForFreeWriterSlots(t *testing.T) {
	store := newFakeStore()
	store.insertGate = make(chan struct{})
	cfg := testConfig()
	cfg.WriteConcurrency = 4
	a := newTestAllocator(t, store, nil, nil, cfg)

	const burst = 10
	var wg sync.WaitGroup
	errs := make(chan error, burst)
	for i := range burst {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", string(rune('a'+i)))})
			errs <- err
		}()
	}
	waitFor(t, func() bool { store.mu.Lock(); defer store.mu.Unlock(); return store.seq == 4 })
	time.Sleep(50 * time.Millisecond)
	store.mu.Lock()
	claimed := store.seq
	store.mu.Unlock()
	if claimed != int64(cfg.WriteConcurrency) {
		t.Fatalf("claimed %d ids with %d writer slots busy, want at most %d", claimed, cfg.WriteConcurrency, cfg.WriteConcurrency)
	}

	close(store.insertGate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("submit: %v", err)
		}
	}
	for s := int64(1); s <= burst; s++ {
		if row, ok := store.rows[cassandra.FormatID(s)]; !ok || strings.HasPrefix(row.alert, fillerPrefix) {
			t.Errorf("%s has no real row", cassandra.FormatID(s))
		}
	}
	if b := a.QueueBytes(); b != 0 {
		t.Errorf("queue bytes = %d after the burst, want 0", b)
	}
}

func TestOversizeSubmission_ClaimedAloneAndStored(t *testing.T) {
	store := newFakeStore()
	cfg := testConfig()
	cfg.WriteConcurrency = 2
	a := newTestAllocator(t, store, nil, nil, cfg)

	batch := make([]model.Alert, 5)
	for i := range batch {
		batch[i] = alert("svc", string(rune('a'+i)))
	}
	ids, err := a.Submit(context.Background(), "prometheus", "req", batch)
	if err != nil || len(ids) != 5 {
		t.Fatalf("ids = %v, err = %v", ids, err)
	}
	for i, id := range ids {
		if seqOf(t, id) != int64(i+1) {
			t.Errorf("ids = %v, want consecutive from 1", ids)
		}
	}
}

func TestQueueBytes_LimitAndRelease(t *testing.T) {
	big := alert("svc", "u")
	big.Description = strings.Repeat("x", 200)

	t.Run("over the limit", func(t *testing.T) {
		store := newFakeStore()
		cfg := testConfig()
		cfg.QueueMaxBytes = 100
		a := newTestAllocator(t, store, nil, nil, cfg)
		if _, err := a.Submit(context.Background(), "aws", "req", []model.Alert{big}); !errors.Is(err, ErrQueueBytesFull) {
			t.Fatalf("err = %v, want ErrQueueBytesFull", err)
		}
		if store.casCalls.Load() != 0 || a.QueueBytes() != 0 {
			t.Errorf("claimed %d times, %d bytes held; want nothing", store.casCalls.Load(), a.QueueBytes())
		}
	})

	cases := map[string]func(*fakeStore){
		"stored":        func(*fakeStore) {},
		"filler path":   func(s *fakeStore) { s.failRealInsert = func(string) bool { return true } },
		"claim failure": func(s *fakeStore) { s.rejectAll = true },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			setup(store)
			a := newTestAllocator(t, store, &recordingNotifier{}, nil, testConfig())
			_, _ = a.Submit(context.Background(), "aws", "req", []model.Alert{big})
			if b := a.QueueBytes(); b != 0 {
				t.Errorf("queue bytes = %d, want 0", b)
			}
		})
	}

	t.Run("shutdown", func(t *testing.T) {
		store := newFakeStore()
		store.insertGate = make(chan struct{})
		a := New(slog.New(slog.NewTextHandler(io.Discard, nil)), store, nil, nil, testConfig())
		done := make(chan struct{})
		go func() {
			_, _ = a.Submit(context.Background(), "aws", "req", []model.Alert{big})
			close(done)
		}()
		waitFor(t, func() bool { return a.QueueBytes() > 0 })
		closed := make(chan error, 1)
		go func() { closed <- a.Close(context.Background()) }()
		close(store.insertGate)
		<-done
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
		if b := a.QueueBytes(); b != 0 {
			t.Errorf("queue bytes = %d after shutdown, want 0", b)
		}
		if _, err := a.Submit(context.Background(), "aws", "req", []model.Alert{big}); !errors.Is(err, ErrShuttingDown) || a.QueueBytes() != 0 {
			t.Errorf("submit after close: err = %v, bytes = %d", err, a.QueueBytes())
		}
	})
}

func TestSizeOf_SharedDescriptionCountedOnce(t *testing.T) {
	desc := strings.Repeat("d", 1000)
	one := alert("svc", "u")
	one.Description = desc
	batch := []model.Alert{one, one, one}
	fields := sizeOf([]model.Alert{one}) - int64(len(desc))
	if got, want := sizeOf(batch), 3*fields+int64(len(desc)); got != want {
		t.Errorf("sizeOf = %d, want %d (description counted once)", got, want)
	}
}

func TestShutdownDrain_StopsThrottledRetriesAndWritesFiller(t *testing.T) {
	store := newFakeStore()
	store.throttleAllInserts = true
	notifier := &recordingNotifier{}
	cfg := testConfig()
	cfg.WriteDeadline = time.Hour
	cfg.QueryTimeout = 50 * time.Millisecond
	a := New(slog.New(slog.NewTextHandler(io.Discard, nil)), store, notifier, nil, cfg)

	type result struct {
		ids []string
		err error
	}
	done := make(chan result, 1)
	go func() {
		ids, err := a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", "u")})
		done <- result{ids, err}
	}()
	waitFor(t, func() bool { store.mu.Lock(); defer store.mu.Unlock(); return store.inserts[cassandra.FormatID(1)] > 2 })

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := a.Close(ctx); err != nil {
		t.Fatalf("Close = %v, want the throttled writer to finish before the drain deadline", err)
	}
	r := <-done
	if !errors.Is(r.err, ErrStoreFailed) {
		t.Fatalf("err = %v, want ErrStoreFailed", r.err)
	}
	row := store.rows[r.ids[0]]
	if !strings.HasPrefix(row.alert, fillerPrefix) || !strings.Contains(row.alert, "shutdown") {
		t.Errorf("row = %q, want a shutdown filler", row.alert)
	}
	if len(notifier.failures) != 1 || !notifier.failures[0].FillerWritten {
		t.Errorf("failures = %+v, want one with FillerWritten", notifier.failures)
	}
}

func TestShutdownDrain_OneFillerAttempt(t *testing.T) {
	store := newFakeStore()
	store.throttleAllInserts = true
	store.failFillers = 100
	cfg := testConfig()
	cfg.WriteDeadline = time.Hour
	cfg.QueryTimeout = 50 * time.Millisecond
	cfg.InsertAttempts = 5
	a := New(slog.New(slog.NewTextHandler(io.Discard, nil)), store, &recordingNotifier{}, nil, cfg)

	done := make(chan struct{})
	go func() {
		_, _ = a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", "u")})
		close(done)
	}()
	waitFor(t, func() bool { store.mu.Lock(); defer store.mu.Unlock(); return store.inserts[cassandra.FormatID(1)] > 2 })
	store.mu.Lock()
	before := store.failFillers
	store.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = a.Close(ctx)
	<-done
	store.mu.Lock()
	defer store.mu.Unlock()
	if used := before - store.failFillers; used != 1 {
		t.Errorf("filler attempts during the drain = %d, want 1", used)
	}
}
