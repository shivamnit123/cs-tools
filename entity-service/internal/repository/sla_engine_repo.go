// Copyright (c) 2026 WSO2 LLC. (https://www.wso2.com).
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

package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/apierror"
)

// slaEngineActiveStageFilter names the terminal sla.stage values a "sla" row
// can never leave -- shared by every write below so a row this engine has
// already finished with is never resurrected by a later, out-of-order call
// (e.g. a retried case-create hook after a slow first attempt already
// registered the clock, or a state-transition hook firing after the case
// was already closed and its resolution clock completed).
const slaEngineActiveStageFilter = `NOT IN ('ACHIEVED', 'BREACHED', 'CANCELLED', 'COMPLETED')`

// slaEngineTerminalOutcomeFilter names the stages that represent a real,
// meaningful outcome already reached for a clock -- deliberately narrower
// than "not active" (slaEngineActiveStageFilter's complement, which also
// includes CANCELLED): CANCELLED means "this clock was deliberately retired
// and its slot is free for a fresh one" (see ReviseClocks), while ACHIEVED/
// BREACHED/COMPLETED mean "this clock type already ran its course for this
// case" and must never be silently re-registered as a brand new running
// clock just because a later severity change (or any other RegisterClock
// caller) finds no ACTIVE row to collide with.
const slaEngineTerminalOutcomeFilter = `IN ('ACHIEVED', 'BREACHED', 'COMPLETED')`

// SLAPolicyRef is the subset of an sla_policy row the engine's resolver
// needs: enough to register a new "sla" row against it, nothing this
// service would otherwise have to re-derive (target, duration) or display
// (name, for logging).
type SLAPolicyRef struct {
	ID       string
	Name     string
	Target   string
	Duration time.Duration
}

// SLAEngineRepository defines the read/write operations backing the
// CSM-native SLA engine (internal/service/sla_policy_resolver.go,
// sla_engine_service.go) -- every write here is scoped to source='CSM' rows
// only (migration 0134): source='SERVICENOW' rows are exclusively owned
// by the ServiceNow sync and this repository never mutates one. Read-side
// consumers of "sla" (GET /sla-status, POST /task-slas/search) need no
// changes at all -- both source values look identical to them, which is the
// entire point of sharing the table (see migration 0134's own comment).
type SLAEngineRepository interface {
	// FindPolicyByName resolves the single active sla_policy row matching
	// name/target, preferring a source='SERVICENOW' row (the real
	// ServiceNow-synced policy) but falling back to a source='CSM' row (a
	// gap-filling policy this engine itself seeded, e.g. the P0 rows added
	// by migration 0136) when no synced row exists under that exact name.
	// Returns apierror.NotFoundError if neither exists.
	FindPolicyByName(ctx context.Context, name, target string) (SLAPolicyRef, error)

	// RegisterClock inserts a new source='CSM' "sla" row for
	// (workItemID, policy.Target) and starts it running now, UNLESS an
	// active (see slaEngineActiveStageFilter) source='CSM' row already
	// exists for that (work_item, target) pair -- idempotent, so a retried
	// case-create hook never double-registers -- OR a row already exists in
	// a genuine terminal outcome stage (see slaEngineTerminalOutcomeFilter):
	// a clock type that already reached ACHIEVED/BREACHED/COMPLETED must
	// never be silently resurrected as a fresh running clock just because no
	// ACTIVE row remains to block it. A CANCELLED row does NOT block a fresh
	// insert -- cancellation deliberately frees that clock type up for a
	// genuinely new one (see ReviseClocks). Returns whether a row was
	// actually inserted.
	RegisterClock(ctx context.Context, workItemID string, policy SLAPolicyRef) (bool, error)

	// CompleteClock marks the active source='CSM' clock for
	// (workItemID, target) ACHIEVED (end_on=now, 100% elapsed). Returns
	// whether a row was found and updated -- false (not an error) when no
	// such clock was ever registered, e.g. a LOW/Query-severity case, which
	// never gets a "response" clock's workaround/resolution siblings, or a
	// case whose policy lookup found nothing at create time.
	CompleteClock(ctx context.Context, workItemID, target string) (bool, error)

	// SetPaused pauses (true) or resumes (false) the active source='CSM'
	// clock for (workItemID, target). Idempotent and a no-op (not an error)
	// when no such clock exists, same reasoning as CompleteClock.
	SetPaused(ctx context.Context, workItemID, target string, paused bool) (bool, error)

	// RecomputeActive recomputes business_elapsed_percentage for every
	// source='CSM' row currently IN_PROGRESS (deliberately narrower than
	// slaEngineActiveStageFilter -- see this method's own doc comment on
	// its implementation for why PAUSED is excluded too), flips has_breached
	// and stage to BREACHED once elapsed time reaches the policy duration,
	// and returns how many rows were touched.
	RecomputeActive(ctx context.Context) (int, error)

	// ReviseClocks marks every active (see slaEngineActiveStageFilter)
	// source='CSM' clock for workItemID CANCELLED, then registers a fresh
	// row for each given policy (same insert shape and terminal-outcome
	// guard as RegisterClock) -- both in ONE database transaction. Used
	// when a case's severity changes: per explicit product direction, the
	// old severity's clocks must not be revised or carried forward in any
	// way, they run into a terminal CANCELLED state, and the new severity's
	// clocks start completely fresh with no relation to the old numbers
	// (see SLAEngineService.ReviseCaseClocks).
	//
	// The whole operation is one transaction, not two independent
	// statements, specifically so a failure partway through registering the
	// new clocks rolls back the cancellation too -- the case is left with
	// its OLD clocks exactly as they were, never with the old ones
	// cancelled and no replacement in their place. A clock already in a
	// terminal outcome stage (see slaEngineTerminalOutcomeFilter, e.g. a
	// response clock CompleteResponseClock already marked ACHIEVED) is left
	// untouched by the cancellation step (it is not "active") and never
	// resurrected by the registration step either (RegisterClock's own
	// terminal-outcome guard applies here too). policies may be empty (e.g.
	// a nil/unresolvable severity) -- the cancellation still runs, nothing
	// gets registered. Returns how many rows were cancelled.
	ReviseClocks(ctx context.Context, workItemID string, policies []SLAPolicyRef) (int, error)
}

type slaEngineRepo struct {
	db *pgxpool.Pool
}

// NewSLAEngineRepository constructs an SLAEngineRepository backed by the
// given connection pool.
func NewSLAEngineRepository(db *pgxpool.Pool) SLAEngineRepository {
	return &slaEngineRepo{db: db}
}

// FindPolicyByName implements SLAEngineRepository.
//
// ORDER BY source: 'SERVICENOW' sorts before 'CSM' lexically, so a plain
// ORDER BY source, then LIMIT 1, prefers the real synced policy whenever
// both a SERVICENOW and a CSM row happen to share a name -- which should
// never actually happen (this engine only ever seeds names ServiceNow's
// own real policy set is confirmed NOT to define, e.g. the P0 rows from
// migration 0136), but preferring the synced row costs nothing and
// removes any doubt about which one wins if it ever did.
func (r *slaEngineRepo) FindPolicyByName(ctx context.Context, name, target string) (SLAPolicyRef, error) {
	// EXTRACT(EPOCH FROM duration) rather than scanning the INTERVAL column
	// directly into time.Duration -- pgx v5 has no default scan plan from
	// PostgreSQL INTERVAL to time.Duration (it scans into pgtype.Interval,
	// whose Months/Days fields have no fixed conversion), so a direct scan
	// errors at query time. Same pattern task_sla_repo.go's
	// scanTaskSlaView already uses for its own INTERVAL columns.
	const query = `
		SELECT id, name, target::TEXT, EXTRACT(EPOCH FROM duration)
		FROM sla_policy
		WHERE name = $1 AND target = $2::sla_policy_target_enum
		  AND source IN ('SERVICENOW', 'CSM')
		  AND (is_active IS NULL OR is_active)
		  AND duration IS NOT NULL
		ORDER BY source
		LIMIT 1`

	var ref SLAPolicyRef
	var durationSeconds float64
	err := r.db.QueryRow(ctx, query, name, target).Scan(&ref.ID, &ref.Name, &ref.Target, &durationSeconds)
	if errors.Is(err, pgx.ErrNoRows) {
		return SLAPolicyRef{}, &apierror.NotFoundError{Msg: "no sla_policy found named " + name}
	}
	if err != nil {
		return SLAPolicyRef{}, fmt.Errorf("find sla policy by name: %w", err)
	}
	ref.Duration = time.Duration(durationSeconds * float64(time.Second))
	return ref, nil
}

// slaEngineRegisterClockQuery inserts a new source='CSM' "sla" row for
// ($1=workItemID, $5=policy.Target) unless an existing row for that pair is
// either still active or already reached a genuine terminal outcome (see
// slaEngineActiveStageFilter/slaEngineTerminalOutcomeFilter) -- a CANCELLED
// row blocks neither, deliberately, since cancellation is what frees a
// clock type up for a fresh registration (see SLAEngineRepository.
// ReviseClocks). Shared, identical SQL text between RegisterClock (run
// against the pool directly) and ReviseClocks (run inside its own
// transaction) -- sqlExecutor is satisfied by both *pgxpool.Pool and pgx.Tx.
const slaEngineRegisterClockQuery = `
	INSERT INTO sla (
		id, created_on, updated_on, created_by, updated_by,
		work_item_id, sla_policy_id, is_active, stage, start_on,
		duration, business_elapsed_percentage, has_breached, source
	)
	SELECT gen_random_uuid(), NOW(), NOW(), $3, $3,
	       $1::uuid, $2::uuid, TRUE, 'IN_PROGRESS'::sla_stage_enum, NOW(),
	       $4::interval, 0, FALSE, 'CSM'::sla_source_enum
	WHERE NOT EXISTS (
		SELECT 1 FROM sla s
		JOIN sla_policy sp ON sp.id = s.sla_policy_id
		WHERE s.work_item_id = $1::uuid
		  AND s.source = 'CSM'
		  AND sp.target::TEXT = $5
		  AND s.stage::TEXT ` + slaEngineActiveStageFilter + `
	)
	AND NOT EXISTS (
		SELECT 1 FROM sla s
		JOIN sla_policy sp ON sp.id = s.sla_policy_id
		WHERE s.work_item_id = $1::uuid
		  AND s.source = 'CSM'
		  AND sp.target::TEXT = $5
		  AND s.stage::TEXT ` + slaEngineTerminalOutcomeFilter + `
	)`

// sqlExecutor is the subset of *pgxpool.Pool/pgx.Tx this file's queries
// need -- lets slaEngineRegisterClockQuery run identically against either.
type sqlExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// registerClockExec runs slaEngineRegisterClockQuery against any sqlExecutor
// -- the pool directly for RegisterClock's own standalone call, or a
// transaction for ReviseClocks, so both share one query and one insert
// decision instead of two copies that could drift apart.
func registerClockExec(ctx context.Context, exec sqlExecutor, workItemID string, policy SLAPolicyRef) (bool, error) {
	tag, err := exec.Exec(ctx, slaEngineRegisterClockQuery, workItemID, policy.ID, sqlActorLiteral, formatIntervalLiteral(policy.Duration), policy.Target)
	if err != nil {
		return false, fmt.Errorf("register csm sla clock: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// RegisterClock implements SLAEngineRepository.
func (r *slaEngineRepo) RegisterClock(ctx context.Context, workItemID string, policy SLAPolicyRef) (bool, error) {
	return registerClockExec(ctx, r.db, workItemID, policy)
}

// CompleteClock implements SLAEngineRepository.
func (r *slaEngineRepo) CompleteClock(ctx context.Context, workItemID, target string) (bool, error) {
	const query = `
		UPDATE sla s
		SET stage = 'ACHIEVED'::sla_stage_enum, end_on = NOW(),
		    business_elapsed_percentage = 100, updated_on = NOW(), updated_by = $3
		FROM sla_policy sp
		WHERE s.sla_policy_id = sp.id
		  AND s.work_item_id = $1::uuid
		  AND s.source = 'CSM'
		  AND sp.target::TEXT = $2
		  AND s.stage::TEXT ` + slaEngineActiveStageFilter

	tag, err := r.db.Exec(ctx, query, workItemID, target, sqlActorLiteral)
	if err != nil {
		return false, fmt.Errorf("complete csm sla clock: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// SetPaused implements SLAEngineRepository.
func (r *slaEngineRepo) SetPaused(ctx context.Context, workItemID, target string, paused bool) (bool, error) {
	const query = `
		UPDATE sla s
		SET stage = CASE WHEN $3 THEN 'PAUSED'::sla_stage_enum ELSE 'IN_PROGRESS'::sla_stage_enum END,
		    paused_on = CASE WHEN $3 THEN NOW() ELSE NULL END,
		    updated_on = NOW(), updated_by = $4
		FROM sla_policy sp
		WHERE s.sla_policy_id = sp.id
		  AND s.work_item_id = $1::uuid
		  AND s.source = 'CSM'
		  AND sp.target::TEXT = $2
		  AND s.stage::TEXT ` + slaEngineActiveStageFilter

	tag, err := r.db.Exec(ctx, query, workItemID, target, paused, sqlActorLiteral)
	if err != nil {
		return false, fmt.Errorf("set csm sla clock paused: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// RecomputeActive implements SLAEngineRepository.
//
// Deliberately scoped to stage = 'IN_PROGRESS' rather than the broader
// slaEngineActiveStageFilter (NOT IN the four terminal stages, which would
// also match PAUSED): a paused clock's whole point is to stop accumulating
// elapsed time, so recomputing its percentage against wall-clock "now" would
// silently undo SetPaused's own effect the very next tick. Excluding PAUSED
// here is what makes pause actually pause.
//
// No business-hours calendar: elapsed is flat wall-clock time since
// start_on, exactly as crude as the deleted sla_clocks map this replaces
// (see internal/service/sla_policy_resolver.go's own doc comment) -- and,
// same as that old code, resuming a paused clock does NOT extend start_on
// or duration to account for the paused interval, so time spent paused is
// silently counted once the clock resumes. A known, accepted gap, not
// something this method works around.
func (r *slaEngineRepo) RecomputeActive(ctx context.Context) (int, error) {
	const query = `
		UPDATE sla
		SET business_elapsed_percentage = LEAST(100, GREATEST(0,
		        EXTRACT(EPOCH FROM (NOW() - start_on)) / NULLIF(EXTRACT(EPOCH FROM duration), 0) * 100
		    )),
		    has_breached = has_breached OR (EXTRACT(EPOCH FROM (NOW() - start_on)) >= EXTRACT(EPOCH FROM duration)),
		    stage = CASE
		        WHEN EXTRACT(EPOCH FROM (NOW() - start_on)) >= EXTRACT(EPOCH FROM duration)
		        THEN 'BREACHED'::sla_stage_enum
		        ELSE stage
		    END,
		    updated_on = NOW(), updated_by = $1
		WHERE source = 'CSM'
		  AND stage = 'IN_PROGRESS'
		  AND start_on IS NOT NULL
		  AND duration IS NOT NULL`

	tag, err := r.db.Exec(ctx, query, sqlActorLiteral)
	if err != nil {
		return 0, fmt.Errorf("recompute csm sla clocks: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ReviseClocks implements SLAEngineRepository.
func (r *slaEngineRepo) ReviseClocks(ctx context.Context, workItemID string, policies []SLAPolicyRef) (int, error) {
	const cancelQuery = `
		UPDATE sla
		SET stage = 'CANCELLED'::sla_stage_enum,
		    updated_on = NOW(), updated_by = $2
		WHERE work_item_id = $1::uuid
		  AND source = 'CSM'
		  AND stage::TEXT ` + slaEngineActiveStageFilter

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("revise csm sla clocks: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, cancelQuery, workItemID, sqlActorLiteral)
	if err != nil {
		return 0, fmt.Errorf("revise csm sla clocks: cancel active: %w", err)
	}
	cancelled := int(tag.RowsAffected())

	for _, policy := range policies {
		if _, err := registerClockExec(ctx, tx, workItemID, policy); err != nil {
			return 0, fmt.Errorf("revise csm sla clocks: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("revise csm sla clocks: commit: %w", err)
	}
	return cancelled, nil
}

// sqlActorLiteral is created_by/updated_by for every row this repository
// writes -- see domain.SLAEngineActor's own doc comment.
const sqlActorLiteral = "sla-engine"

// formatIntervalLiteral renders a time.Duration as a Postgres INTERVAL
// literal ("3600 seconds") -- binding a Go time.Duration directly as
// ::interval has no pgx encode plan registered in this codebase (same
// class of issue as comment_repo.go's ::text[]::comment_type_enum[] cast
// note), so this passes a plain numeric-seconds string instead, which
// Postgres's own ::interval cast parses natively.
func formatIntervalLiteral(d time.Duration) string {
	return fmt.Sprintf("%d seconds", int64(d.Seconds()))
}
