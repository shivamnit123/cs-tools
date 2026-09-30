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

package repository_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/apierror"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/domain"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/repository"
)

// Skipped without CHANGE_REQUEST_TEST_DSN, the same convention every other
// repository integration test in this package uses.
//
//	CHANGE_REQUEST_TEST_DSN=postgres://... go test ./internal/repository/ -run ChangeRequestIntegration

const changeRequestApprovalTestID = "36666666-0000-0000-0000-000000000001"

// seedChangeRequestForApprovalTest inserts a minimal work_item/change_request
// pair in the given state -- enough for PatchChangeRequest's own read (via
// GetChangeRequestByID at the end of a successful patch) to resolve, since
// every other join in changeRequestFromJoins is a LEFT JOIN.
func seedChangeRequestForApprovalTest(t *testing.T, pool *pgxpool.Pool, state string) {
	t.Helper()
	ctx := context.Background()

	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM work_item WHERE id = $1`, changeRequestApprovalTestID)
	}
	cleanup()
	t.Cleanup(cleanup)

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed (%.60s): %v", sql, err)
		}
	}

	mustExec(`INSERT INTO work_item (id, created_on, updated_on, created_by, updated_by, number, subject, type)
	          VALUES ($1, now(), now(), 'cr-approval-test', 'cr-approval-test', 'CRAPPRV01', 'approval guard test', 'CHANGE_REQUEST')`,
		changeRequestApprovalTestID)
	mustExec(`INSERT INTO change_request (id, state) VALUES ($1, $2::change_request_state_enum)`,
		changeRequestApprovalTestID, state)
}

// TestChangeRequestIntegration_RequestApprovalRejectsNonNewState guards
// against the regression CodeRabbit flagged on PR #2132: PatchChangeRequest's
// {requestApproval: true} branch used to write state=ASSESS unconditionally
// whenever the caller didn't also send an explicit state, regardless of the
// record's actual current state -- so a stale/replayed request against a
// change request already at, say, Review or Closed would silently regress it
// back to Assess. It must now be rejected instead.
func TestChangeRequestIntegration_RequestApprovalRejectsNonNewState(t *testing.T) {
	dsn := os.Getenv("CHANGE_REQUEST_TEST_DSN")
	if dsn == "" {
		t.Skip("CHANGE_REQUEST_TEST_DSN not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	repo := repository.NewChangeRequestRepository(pool)
	seedChangeRequestForApprovalTest(t, pool, "REVIEW")

	yes := true
	_, err = repo.PatchChangeRequest(context.Background(), changeRequestApprovalTestID,
		domain.PatchChangeRequestRequest{RequestApproval: &yes}, "cr-approval-test")

	var conflict *apierror.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("PatchChangeRequest(requestApproval=true) on a Review-state change request: got err=%v, want *apierror.ConflictError", err)
	}

	var gotState *string
	if scanErr := pool.QueryRow(context.Background(),
		`SELECT state::TEXT FROM change_request WHERE id = $1`, changeRequestApprovalTestID).Scan(&gotState); scanErr != nil {
		t.Fatalf("read back state: %v", scanErr)
	}
	if gotState == nil || *gotState != "REVIEW" {
		t.Fatalf("state after rejected approval request = %v, want unchanged \"REVIEW\"", gotState)
	}
}

// TestChangeRequestIntegration_RequestApprovalAdvancesNewToAssess is the
// companion positive case: a change request genuinely in New must still
// advance to Assess exactly as before this fix.
func TestChangeRequestIntegration_RequestApprovalAdvancesNewToAssess(t *testing.T) {
	dsn := os.Getenv("CHANGE_REQUEST_TEST_DSN")
	if dsn == "" {
		t.Skip("CHANGE_REQUEST_TEST_DSN not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	repo := repository.NewChangeRequestRepository(pool)
	seedChangeRequestForApprovalTest(t, pool, "NEW")

	yes := true
	got, err := repo.PatchChangeRequest(context.Background(), changeRequestApprovalTestID,
		domain.PatchChangeRequestRequest{RequestApproval: &yes}, "cr-approval-test")
	if err != nil {
		t.Fatalf("PatchChangeRequest(requestApproval=true) on a New-state change request: %v", err)
	}
	if got.State == nil || *got.State != "assess" {
		t.Fatalf("state after approval request = %v, want \"assess\"", got.State)
	}
}
