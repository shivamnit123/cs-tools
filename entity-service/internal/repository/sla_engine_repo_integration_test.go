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

// This is an integration test: it exercises the real SQL in
// sla_engine_repo.go's RegisterClock/ReviseClocks against a live PostgreSQL
// instance, because the two bugs it regression-tests -- a terminal
// (ACHIEVED) clock getting resurrected as a fresh running one, and a
// previously-CANCELLED clock failing to be replaced -- both depend on real
// enum/stage filtering behavior a fake repository can't reproduce. Same DSN
// and skip-when-unset pattern as project_stats_repo_integration_test.go/
// time_card_repo_test.go (package repository_test, reuses caseStatsPool):
//
//	CASE_STATS_TEST_DSN=postgres://... go test ./internal/repository/ -run SLAEngineIntegration

package repository_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/repository"
)

const slaEngineIntegrationWorkItemID = "47777777-0000-0000-0000-000000000001"

// seedSLAEngineWorkItem inserts one minimal work_item row (all its FK
// columns are nullable, so account/project/deployment/user need not exist)
// for the test to register/revise "sla" rows against, and removes both it
// and any "sla" rows created for it before and after the test so the
// integration test stays re-runnable against a shared database.
func seedSLAEngineWorkItem(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM sla WHERE work_item_id = $1::uuid`, slaEngineIntegrationWorkItemID)
		_, _ = pool.Exec(ctx, `DELETE FROM work_item WHERE id = $1::uuid`, slaEngineIntegrationWorkItemID)
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := pool.Exec(ctx, `
		INSERT INTO work_item (id, created_on, updated_on, created_by, updated_by, number, wso2_id, subject, type)
		VALUES ($1::uuid, NOW(), NOW(), 'sla-engine-test', 'sla-engine-test', 'SLAENGINE01', 'SLAENGINE-1', 'sla engine integration test case', 'CASE')`,
		slaEngineIntegrationWorkItemID); err != nil {
		t.Fatalf("seed work_item: %v", err)
	}
}

// TestSLAEngineIntegration_ReviseClocksDoesNotResurrectTerminalClock is the
// regression test CodeRabbit asked for on PR #2051's cancel-and-restart
// design: a RESPONSE clock already reached ACHIEVED (a genuine terminal
// outcome, e.g. via CompleteClock once a reply went out) must NOT be
// resurrected as a brand-new IN_PROGRESS clock just because a later
// severity change calls ReviseClocks again with the same RESPONSE policy --
// RegisterClock's terminal-outcome guard (slaEngineTerminalOutcomeFilter)
// is what prevents this, verified here against real Postgres because the
// bug can only manifest through the real NOT EXISTS filtering on real enum
// values.
//
// Same test also confirms the sibling requirement: a clock already
// CANCELLED (not a genuine terminal outcome, just retired by an earlier
// revision) is NOT blocked -- it IS replaced with a fresh IN_PROGRESS row,
// and a still-active clock is cancelled and replaced too. All three
// outcomes are exercised in a single ReviseClocks call, matching how
// SLAEngineService.ReviseCaseClocks actually calls it (one call per
// severity change, covering every applicable clock type at once).
func TestSLAEngineIntegration_ReviseClocksDoesNotResurrectTerminalClock(t *testing.T) {
	pool := caseStatsPool(t)
	seedSLAEngineWorkItem(t, pool)
	ctx := context.Background()
	repo := repository.NewSLAEngineRepository(pool)

	respPolicy, err := repo.FindPolicyByName(ctx, "P0 - Response (Managed Services)", "RESPONSE")
	if err != nil {
		t.Fatalf("FindPolicyByName(response): %v", err)
	}
	workaroundPolicy, err := repo.FindPolicyByName(ctx, "P0 - Workaround (Managed Services)", "WORKAROUND")
	if err != nil {
		t.Fatalf("FindPolicyByName(workaround): %v", err)
	}
	resolutionPolicy, err := repo.FindPolicyByName(ctx, "P0 - Resolution (Managed Services)", "RESOLUTION")
	if err != nil {
		t.Fatalf("FindPolicyByName(resolution): %v", err)
	}

	// Register all three, then put each into a different starting state:
	// RESPONSE -> ACHIEVED (a genuine terminal outcome, via CompleteClock),
	// WORKAROUND -> left IN_PROGRESS (still active),
	// RESOLUTION -> CANCELLED directly (simulating an earlier revision that
	// already retired it).
	for _, p := range []repository.SLAPolicyRef{respPolicy, workaroundPolicy, resolutionPolicy} {
		if _, err := repo.RegisterClock(ctx, slaEngineIntegrationWorkItemID, p); err != nil {
			t.Fatalf("RegisterClock(%s) setup: %v", p.Target, err)
		}
	}
	if _, err := repo.CompleteClock(ctx, slaEngineIntegrationWorkItemID, "RESPONSE"); err != nil {
		t.Fatalf("CompleteClock(RESPONSE) setup: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE sla SET stage = 'CANCELLED' WHERE work_item_id = $1::uuid AND sla_policy_id = $2::uuid`,
		slaEngineIntegrationWorkItemID, resolutionPolicy.ID); err != nil {
		t.Fatalf("pre-cancel RESOLUTION setup: %v", err)
	}

	// One ReviseClocks call with all three policies -- as SLAEngineService.
	// ReviseCaseClocks actually calls it on a real severity change.
	if _, err := repo.ReviseClocks(ctx, slaEngineIntegrationWorkItemID,
		[]repository.SLAPolicyRef{respPolicy, workaroundPolicy, resolutionPolicy}); err != nil {
		t.Fatalf("ReviseClocks: %v", err)
	}

	// countRows fails the test outright on a query error, rather than
	// letting a scan failure silently read back as a misleading "0 rows"
	// assertion failure below.
	countRows := func(t *testing.T, query string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, query, slaEngineIntegrationWorkItemID).Scan(&n); err != nil {
			t.Fatalf("count query failed: %v\nquery: %s", err, query)
		}
		return n
	}

	// RESPONSE: exactly one row, still ACHIEVED -- not resurrected.
	responseTotal := countRows(t, `SELECT COUNT(*) FROM sla s JOIN sla_policy sp ON sp.id = s.sla_policy_id
		WHERE s.work_item_id = $1::uuid AND sp.target::TEXT = 'RESPONSE'`)
	responseAchieved := countRows(t, `SELECT COUNT(*) FROM sla s JOIN sla_policy sp ON sp.id = s.sla_policy_id
		WHERE s.work_item_id = $1::uuid AND sp.target::TEXT = 'RESPONSE' AND s.stage = 'ACHIEVED'`)
	if responseTotal != 1 || responseAchieved != 1 {
		t.Errorf("RESPONSE rows = %d (achieved = %d), want exactly 1 row still ACHIEVED -- a terminal clock must never be resurrected", responseTotal, responseAchieved)
	}

	// WORKAROUND: the original active row is now CANCELLED, and a fresh
	// IN_PROGRESS row exists alongside it.
	workaroundCancelled := countRows(t, `SELECT COUNT(*) FROM sla s JOIN sla_policy sp ON sp.id = s.sla_policy_id
		WHERE s.work_item_id = $1::uuid AND sp.target::TEXT = 'WORKAROUND' AND s.stage = 'CANCELLED'`)
	workaroundActive := countRows(t, `SELECT COUNT(*) FROM sla s JOIN sla_policy sp ON sp.id = s.sla_policy_id
		WHERE s.work_item_id = $1::uuid AND sp.target::TEXT = 'WORKAROUND' AND s.stage = 'IN_PROGRESS'`)
	if workaroundCancelled != 1 || workaroundActive != 1 {
		t.Errorf("WORKAROUND cancelled = %d, active = %d, want 1/1 -- the old active clock must be cancelled and a fresh one registered", workaroundCancelled, workaroundActive)
	}

	// RESOLUTION: was already CANCELLED before this call -- must now ALSO
	// have a fresh IN_PROGRESS row (a cancelled clock does not block a
	// fresh registration).
	resolutionActive := countRows(t, `SELECT COUNT(*) FROM sla s JOIN sla_policy sp ON sp.id = s.sla_policy_id
		WHERE s.work_item_id = $1::uuid AND sp.target::TEXT = 'RESOLUTION' AND s.stage = 'IN_PROGRESS'`)
	if resolutionActive != 1 {
		t.Errorf("RESOLUTION active rows = %d, want 1 -- a previously-CANCELLED clock must still be replaced by a fresh registration", resolutionActive)
	}
}
