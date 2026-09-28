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

// Package allocator assigns alert ids and writes the rows alerts-core reads.
//
// Claiming one id per alert makes every request compete for the single alert_seq row, which
// turns a burst into mostly failed compare-and-sets. Instead, handlers queue their alerts and
// one claimer goroutine per replica takes everything waiting (up to MaxBatch), claims the
// whole range N+1..N+n with a single compare-and-set, and hands the range to parallel writers
// without waiting for them. Replicas only contend on the claim itself, which takes
// milliseconds; they never wait on each other's inserts.
//
// alerts-core reads ids in order and waits gap_timeout (10 minutes) on each missing id, one at
// a time. So once an id is claimed it must get a row: writes use a context detached from the
// HTTP request, and an alert that can't be written gets a filler row alerts-core can't parse
// and skips immediately.
package allocator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"sre-alert-ingestion-service/internal/cassandra"
	"sre-alert-ingestion-service/internal/model"
)

// Errors returned by Submit. All of them map to 503: none of them leaves a claimed id empty
// through a fault of the request itself.
var (
	ErrQueueFull    = errors.New("alert queue full")
	ErrShuttingDown = errors.New("shutting down")
	ErrClaimFailed  = errors.New("could not claim alert ids")
	ErrStoreFailed  = errors.New("alert could not be stored")
	ErrTimeout      = errors.New("timed out waiting for storage")
)

// fillerPrefix marks a row alerts-core can't parse as an alert: it logs
// "alert unprocessable, skipping" and moves to the next id without waiting.
const fillerPrefix = "VOID: "

// Store is the storage the allocator needs; *cassandra.Store implements it.
type Store interface {
	ReadSeq(ctx context.Context) (int64, error)
	// CompareAndSet moves alert_seq from `from` to `to`; when rejected, current is the value
	// the row actually holds.
	CompareAndSet(ctx context.Context, from, to int64) (applied bool, current int64, err error)
	Insert(ctx context.Context, id, vendor, alert string) error
	Exists(ctx context.Context, id string) (bool, error)
	// InsertFiller writes the filler only if id has no row; otherwise existing is that row's alert.
	InsertFiller(ctx context.Context, id, vendor, filler string) (applied bool, existing string, err error)
}

// StoreFailure describes an alert that couldn't be written after every attempt.
type StoreFailure struct {
	Vendor        string
	RequestID     string
	AltID         string
	Alert         model.Alert
	Err           error
	FillerWritten bool
}

// FailureNotifier is told about every alert that couldn't be stored (the DB-failure Chat card).
type FailureNotifier interface {
	StoreFailed(StoreFailure)
}

// Waker wakes alerts-core once per written batch. Implementations must not block.
type Waker interface {
	Wake()
}

// Config tunes the allocator; see config.toml.example.
type Config struct {
	QueueSize        int
	MaxBatch         int
	WriteConcurrency int
	ClaimMaxAttempts int
	InsertAttempts   int
	InsertBaseDelay  time.Duration
	// ClaimJitter bounds the random pause before retrying a rejected compare-and-set, so two
	// replicas that collided don't collide again in lockstep.
	ClaimJitter time.Duration
}

// Result is what a submitter receives: its ids, in submission order, or an error.
type Result struct {
	IDs []string
	Err error
}

type submission struct {
	vendor    string
	requestID string
	alerts    []model.Alert
	done      chan Result // buffered(1): the writer never blocks on a submitter that gave up
}

// Allocator is safe for concurrent Submit calls.
type Allocator struct {
	logger   *slog.Logger
	store    Store
	notifier FailureNotifier
	waker    Waker
	cfg      Config

	mu          sync.RWMutex // guards closed against the close of queue
	closed      bool
	queue       chan *submission
	writeSem    chan struct{}
	writes      sync.WaitGroup
	claimerDone chan struct{}

	pendingMu sync.Mutex
	pending   map[int64]struct{} // claimed ids whose row or filler isn't written yet
}

// New starts the claimer goroutine. notifier and waker may be nil.
func New(logger *slog.Logger, store Store, notifier FailureNotifier, waker Waker, cfg Config) *Allocator {
	a := &Allocator{
		logger:      logger,
		store:       store,
		notifier:    notifier,
		waker:       waker,
		cfg:         cfg,
		queue:       make(chan *submission, cfg.QueueSize),
		writeSem:    make(chan struct{}, cfg.WriteConcurrency),
		claimerDone: make(chan struct{}),
		pending:     map[int64]struct{}{},
	}
	go a.claimLoop()
	return a
}

// Submit queues alerts and waits for their ids. A full queue fails immediately. If ctx ends
// first Submit returns ErrTimeout, but the alerts are still written: their ids may already be
// claimed, and a claimed id must never be left empty.
func (a *Allocator) Submit(ctx context.Context, vendor, requestID string, alerts []model.Alert) ([]string, error) {
	if len(alerts) == 0 {
		return []string{}, nil
	}
	sub := &submission{vendor: vendor, requestID: requestID, alerts: alerts, done: make(chan Result, 1)}

	a.mu.RLock()
	if a.closed {
		a.mu.RUnlock()
		return nil, ErrShuttingDown
	}
	select {
	case a.queue <- sub:
		a.mu.RUnlock()
	default:
		a.mu.RUnlock()
		return nil, ErrQueueFull
	}

	select {
	case res := <-sub.done:
		return res.IDs, res.Err
	case <-ctx.Done():
		a.logger.Warn("request gave up waiting; its alerts are still being written",
			"request_id", requestID, "vendor", vendor, "alerts", len(alerts))
		return nil, ErrTimeout
	}
}

// Close stops accepting submissions, lets the claimer claim everything already queued, and
// waits for every write to finish or ctx to end. Safe to call more than once.
func (a *Allocator) Close(ctx context.Context) error {
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		close(a.queue)
	}
	a.mu.Unlock()

	select {
	case <-a.claimerDone:
	case <-ctx.Done():
		a.logUnwritten()
		return fmt.Errorf("claimer did not drain: %w", ctx.Err())
	}
	written := make(chan struct{})
	go func() {
		a.writes.Wait()
		close(written)
	}()
	select {
	case <-written:
		return nil
	case <-ctx.Done():
		a.logUnwritten()
		return fmt.Errorf("writers did not finish: %w", ctx.Err())
	}
}

// maxLoggedIDs bounds the id list in the unwritten-ids log line.
const maxLoggedIDs = 100

// logUnwritten names the claimed ids left without a row, which alerts-core will wait
// gap_timeout on.
func (a *Allocator) logUnwritten() {
	a.pendingMu.Lock()
	seqs := make([]int64, 0, len(a.pending))
	for s := range a.pending {
		seqs = append(seqs, s)
	}
	a.pendingMu.Unlock()
	if len(seqs) == 0 {
		return
	}
	slices.Sort(seqs)
	ids := make([]string, 0, min(len(seqs), maxLoggedIDs))
	for _, s := range seqs[:min(len(seqs), maxLoggedIDs)] {
		ids = append(ids, cassandra.FormatID(s))
	}
	a.logger.Error("shutdown cut off writes; these ids have no row and alerts-core will wait gap_timeout on them",
		"count", len(seqs), "first_id", cassandra.FormatID(seqs[0]),
		"last_id", cassandra.FormatID(seqs[len(seqs)-1]), "ids", ids)
}

// claimLoop takes the first waiting submission, then everything else already waiting up to
// MaxBatch alerts. There is no timer: a lone alert is claimed immediately. A submission is
// never split, so a Prometheus batch always gets consecutive ids; a submission larger than
// MaxBatch is claimed on its own.
func (a *Allocator) claimLoop() {
	defer close(a.claimerDone)
	var carry *submission
	for {
		first := carry
		carry = nil
		if first == nil {
			var ok bool
			if first, ok = <-a.queue; !ok {
				return
			}
		}
		batch := []*submission{first}
		n := len(first.alerts)
	gather:
		for n < a.cfg.MaxBatch {
			select {
			case sub, ok := <-a.queue:
				if !ok {
					break gather
				}
				if n+len(sub.alerts) > a.cfg.MaxBatch {
					carry = sub
					break gather
				}
				batch = append(batch, sub)
				n += len(sub.alerts)
			default:
				break gather
			}
		}

		claimStart := time.Now()
		start, attempts, err := a.claim(n)
		if err != nil {
			a.logger.Error("claim failed; batch rejected, no ids claimed", "alerts", n,
				"submissions", len(batch), "claim_attempts", attempts, "error", err)
			for _, sub := range batch {
				sub.done <- Result{Err: ErrClaimFailed}
			}
			continue
		}
		a.logger.Info("batch claimed", "alerts", n, "submissions", len(batch),
			"first_id", cassandra.FormatID(start), "last_id", cassandra.FormatID(start+int64(n)-1),
			"claim_attempts", attempts, "claim_ms", time.Since(claimStart).Milliseconds())
		a.pendingMu.Lock()
		for s := start; s < start+int64(n); s++ {
			a.pending[s] = struct{}{}
		}
		a.pendingMu.Unlock()
		a.writes.Add(1)
		go a.writeBatch(batch, start)
	}
}

// claim reserves n consecutive ids with one compare-and-set and returns the first. A rejected
// compare-and-set returns the row's current value, which the retry uses directly.
//
// If the compare-and-set call itself errors (e.g. times out), it may still have applied on the
// server; those ids would then have no rows and alerts-core would wait gap_timeout on each.
// The retry can't tell, so this is logged; it's the same class of failure as "DB down".
func (a *Allocator) claim(n int) (start int64, attempts int, err error) {
	ctx := context.Background()
	current, err := a.store.ReadSeq(ctx)
	if err != nil {
		return 0, 0, err
	}
	var lastErr error
	for attempt := 1; attempt <= a.cfg.ClaimMaxAttempts; attempt++ {
		applied, seen, err := a.store.CompareAndSet(ctx, current, current+int64(n))
		switch {
		case err != nil:
			lastErr = err
			a.logger.Warn("compare-and-set errored; it may have applied, leaving a gap",
				"from", current, "to", current+int64(n), "attempt", attempt, "error", err)
			if current, err = a.store.ReadSeq(ctx); err != nil {
				lastErr = err
			}
		case applied:
			return current + 1, attempt, nil
		default:
			current = seen
		}
		a.pause()
	}
	return 0, a.cfg.ClaimMaxAttempts, fmt.Errorf("gave up after %d attempts: %v", a.cfg.ClaimMaxAttempts, lastErr)
}

func (a *Allocator) pause() {
	if a.cfg.ClaimJitter > 0 {
		time.Sleep(rand.N(a.cfg.ClaimJitter))
	}
}

type alertJob struct {
	sub   *submission
	index int
	seq   int64
}

// writeBatch writes every alert in parallel (bounded across all batches by WriteConcurrency),
// replies to each submitter with its ids, and wakes alerts-core once if anything was stored.
func (a *Allocator) writeBatch(batch []*submission, start int64) {
	defer a.writes.Done()
	writeStart := time.Now()

	type outcome struct {
		ids    []string
		failed bool
	}
	outcomes := make([]outcome, len(batch))
	var mu sync.Mutex
	var wg sync.WaitGroup
	stored := false

	seq := start
	for si, sub := range batch {
		outcomes[si].ids = make([]string, len(sub.alerts))
		for i := range sub.alerts {
			job := alertJob{sub: sub, index: i, seq: seq}
			seq++
			wg.Add(1)
			a.writeSem <- struct{}{}
			go func(si int) {
				defer wg.Done()
				defer func() { <-a.writeSem }()
				id, ok := a.writeOne(job)
				mu.Lock()
				outcomes[si].ids[job.index] = id
				if ok {
					stored = true
				} else {
					outcomes[si].failed = true
				}
				mu.Unlock()
			}(si)
		}
	}
	wg.Wait()

	failed := 0
	for _, o := range outcomes {
		if o.failed {
			failed++
		}
	}
	a.logger.Info("batch written", "alerts", seq-start, "first_id", cassandra.FormatID(start),
		"failed_submissions", failed, "write_ms", time.Since(writeStart).Milliseconds())

	for si, sub := range batch {
		if outcomes[si].failed {
			sub.done <- Result{IDs: outcomes[si].ids, Err: ErrStoreFailed}
		} else {
			sub.done <- Result{IDs: outcomes[si].ids}
		}
	}
	if stored && a.waker != nil {
		a.waker.Wake()
	}
}

// writeOne writes one alert under its claimed id, retrying on the same id, then falls back to
// a filler row. ok reports whether the real alert was stored.
func (a *Allocator) writeOne(job alertJob) (id string, ok bool) {
	defer func() {
		a.pendingMu.Lock()
		delete(a.pending, job.seq)
		a.pendingMu.Unlock()
	}()
	ctx := context.Background() // detached: a disconnected client must not leave this id empty
	id = cassandra.FormatID(job.seq)
	alert := job.sub.alerts[job.index]
	if alert.UniqueIdentifier == "" {
		a.logger.Warn("vendor sent no id; using the alert id as unique_identifier, so this alert "+
			"won't merge with repeats or resolve on recovery",
			"request_id", job.sub.requestID, "vendor", job.sub.vendor, "alt_id", id)
		alert.UniqueIdentifier = id
	}
	body, err := json.Marshal(alert)
	if err != nil {
		// Unreachable for a struct of strings; handled so the id still gets a row.
		return id, a.fail(job, id, alert, err)
	}

	err = a.retry(func() error { return a.insertAndConfirm(ctx, id, job.sub.vendor, string(body)) },
		func(attempt int, err error) {
			a.logger.Warn("insert failed, retrying on the same id", "request_id", job.sub.requestID,
				"vendor", job.sub.vendor, "alt_id", id, "attempt", attempt, "error", err)
		})
	if err == nil {
		return id, true
	}
	return id, a.fail(job, id, alert, err)
}

// retry runs fn up to InsertAttempts times, doubling InsertBaseDelay between attempts.
func (a *Allocator) retry(fn func() error, onErr func(attempt int, err error)) error {
	var err error
	delay := a.cfg.InsertBaseDelay
	for attempt := 1; attempt <= a.cfg.InsertAttempts; attempt++ {
		if attempt > 1 {
			time.Sleep(delay)
			delay *= 2
		}
		if err = fn(); err == nil {
			return nil
		}
		onErr(attempt, err)
	}
	return err
}

func (a *Allocator) insertAndConfirm(ctx context.Context, id, vendor, body string) error {
	if err := a.store.Insert(ctx, id, vendor, body); err != nil {
		return err
	}
	visible, err := a.store.Exists(ctx, id)
	if err != nil {
		return err
	}
	if !visible {
		return fmt.Errorf("row %s not visible after insert", id)
	}
	return nil
}

// fail writes the filler row, reports the failure, and returns writeOne's ok. The filler never
// overwrites a row: if the alert did land (a timed-out insert, or a read-back Cosmos missed),
// it is kept and counted as stored.
func (a *Allocator) fail(job alertJob, id string, alert model.Alert, cause error) bool {
	filler := fillerPrefix + cause.Error()
	var existing string
	fillerErr := a.retry(func() error {
		applied, prev, err := a.store.InsertFiller(context.Background(), id, job.sub.vendor, filler)
		if err == nil && !applied {
			existing = prev
		}
		return err
	}, func(attempt int, err error) {
		a.logger.Warn("filler row failed, retrying", "request_id", job.sub.requestID,
			"vendor", job.sub.vendor, "alt_id", id, "attempt", attempt, "error", err)
	})
	if existing != "" && !strings.HasPrefix(existing, fillerPrefix) {
		a.logger.Warn("alert stored although its write reported a failure; filler not written",
			"request_id", job.sub.requestID, "vendor", job.sub.vendor, "alt_id", id, "error", cause)
		return true
	}
	if fillerErr != nil {
		// Cassandra is likely down: alerts-core will wait gap_timeout on this id.
		a.logger.Error("alert NOT stored and filler row failed; alerts-core will stall on this id until gap_timeout",
			"request_id", job.sub.requestID, "vendor", job.sub.vendor, "alt_id", id,
			"alert", alert, "error", cause, "filler_error", fillerErr)
	} else {
		a.logger.Error("alert NOT stored; filler row written so alerts-core skips the id",
			"request_id", job.sub.requestID, "vendor", job.sub.vendor, "alt_id", id,
			"alert", alert, "error", cause)
	}
	if a.notifier != nil {
		a.notifier.StoreFailed(StoreFailure{
			Vendor: job.sub.vendor, RequestID: job.sub.requestID, AltID: id,
			Alert: alert, Err: cause, FillerWritten: fillerErr == nil,
		})
	}
	return false
}
