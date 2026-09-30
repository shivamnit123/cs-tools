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
// KIND, either express or implied. See the License for the
// specific language governing permissions and limitations
// under the License.

package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/apierror"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/domain"
)

// SalesforceProjectRepository writes the Salesforce Project__c ingest into
// project. Every write records its salesforce_ingest_state row in the same
// transaction, so the ledger can never disagree with the table.
type SalesforceProjectRepository interface {
	// UpsertFromSalesforce writes the Salesforce-owned columns of one project,
	// resolved by sf_id and then by key, and records state. With
	// row.AllowInsert false a project CSM does not have is a NotFoundError
	// ("project not found for sfId ...", which the delayed-retry job
	// matches) and nothing is written.
	UpsertFromSalesforce(ctx context.Context, row domain.SalesforceProjectUpsert, state domain.UpsertSalesforceIngestStateRequest) (domain.SalesforceProjectUpsertResult, error)
	// SoftDeleteBySfID marks every project carrying sfID inactive
	// (is_active = FALSE) and records state. found is false when no project
	// carries it; the ledger row is written regardless.
	SoftDeleteBySfID(ctx context.Context, sfID string, state domain.UpsertSalesforceIngestStateRequest) (bool, error)
	// LookupProjectIDBySfID returns the id of the oldest project carrying
	// sfID, or nil when there is none.
	LookupProjectIDBySfID(ctx context.Context, sfID string) (*string, error)
	// LookupProjectTypeIDByName returns project_type.id for an exact name,
	// or nil when no type has that name. The ingest never creates types:
	// they carry feature entitlements only a migration can set.
	LookupProjectTypeIDByName(ctx context.Context, name string) (*string, error)
}

type salesforceProjectRepo struct {
	db *pgxpool.Pool
}

// NewSalesforceProjectRepository constructs a SalesforceProjectRepository backed by the pool.
func NewSalesforceProjectRepository(db *pgxpool.Pool) SalesforceProjectRepository {
	return &salesforceProjectRepo{db: db}
}

// salesforceProjectLockKey serialises every write of one project: sf_id is
// not unique (migration 0095), so two concurrent events would otherwise
// both miss the row and both insert.
func salesforceProjectLockKey(sfID string) string { return "project-sf:" + sfID }

// resolveSalesforceProjectQuery picks the row to write: the row carrying the
// sf_id (preferring one whose key already matches), else the row with the
// key (a ServiceNow-synced row whose sf_id is empty or different; its sf_id
// is stamped). by_key is true in the second case. key is UNIQUE, so the key
// arm matches at most one row.
const resolveSalesforceProjectQuery = `
	SELECT id::text, (sf_id IS DISTINCT FROM $1) AS by_key
	FROM project
	WHERE sf_id = $1 OR key = $2
	ORDER BY CASE WHEN sf_id = $1 AND key = $2 THEN 0 WHEN sf_id = $1 THEN 1 ELSE 2 END, created_on, id
	LIMIT 1`

// updateSalesforceProjectQuery lists ONLY the ten columns Salesforce owns
// (SALESFORCE_SYNC_PLAN.md §6) plus the audit pair. The hour counters,
// wso2_closure_state and the three CSM closure states, the onboarding
// fields, the product-consumption credentials, number, is_active,
// planned_end_date and assignment_group_id belong to ServiceNow or CSM and
// must never appear here (TestSalesforceProjectSQL_ColumnList).
//
// account_id keeps the stored value when Salesforce names no account, so a
// record read without Account__c cannot orphan the project.
// onboarding_go_live_date is Go_Live_Date__c, which CSM writes to Salesforce
// (decision D4); writing it back is a mirror of CSM's own value.
const updateSalesforceProjectQuery = `
	UPDATE project SET
		sf_id = $2,
		key = $3,
		name = $4,
		account_id = COALESCE($5::uuid, project.account_id),
		start_date = $6,
		end_date = $7,
		description = $8,
		project_type_id = $9::uuid,
		compliance_violation_date = $10,
		onboarding_go_live_date = $11,
		updated_on = now(),
		updated_by = $1
	WHERE id = $12::uuid`

// insertSalesforceProjectQuery creates a project Salesforce knows and CSM
// does not; only allowed once csm-sync-service no longer writes project
// (CSM_MIGRATION_SALESFORCE_PROJECT_INSERT_ENABLED). The row id is random:
// no ServiceNow sys_id exists for it. is_active starts TRUE, the only value
// a live Salesforce project can have; number stays NULL (ServiceNow's record
// number has no Salesforce source).
const insertSalesforceProjectQuery = `
	INSERT INTO project (
		id, created_on, updated_on, created_by, updated_by,
		sf_id, key, name, account_id, start_date, end_date, description,
		project_type_id, compliance_violation_date, onboarding_go_live_date, is_active
	) VALUES (
		gen_random_uuid(), now(), now(), $1, $1,
		$2, $3, $4, $5::uuid, $6, $7, $8,
		$9::uuid, $10, $11, TRUE
	)
	RETURNING id::text`

// reactivateSalesforceProjectQuery clears the DELETED marker. It is a
// separate statement so the main UPDATE never names is_active, which
// ServiceNow owns while it runs; it touches only rows the marker is on.
const reactivateSalesforceProjectQuery = `
	UPDATE project SET is_active = TRUE, updated_on = now(), updated_by = $2
	WHERE sf_id = $1 AND is_active IS FALSE`

// softDeleteSalesforceProjectQuery is the DELETED marker. project has no
// deleted_on column; is_active = FALSE is the least invasive marker
// (plan §6), and a RESTORED event clears it.
const softDeleteSalesforceProjectQuery = `
	UPDATE project SET is_active = FALSE, updated_on = now(), updated_by = $2
	WHERE sf_id = $1`

func (r *salesforceProjectRepo) UpsertFromSalesforce(ctx context.Context, row domain.SalesforceProjectUpsert, state domain.UpsertSalesforceIngestStateRequest) (domain.SalesforceProjectUpsertResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.SalesforceProjectUpsertResult{}, fmt.Errorf("upsert project from salesforce: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	res, err := writeSalesforceProject(ctx, tx, row, state)
	if err != nil {
		return domain.SalesforceProjectUpsertResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.SalesforceProjectUpsertResult{}, fmt.Errorf("upsert project from salesforce: commit: %w", err)
	}
	return res, nil
}

// writeSalesforceProject is UpsertFromSalesforce's body, run on q (the
// transaction):
//
//  1. lock on the sf_id;
//  2. resolve the row by sf_id, else by key;
//  3. update its Salesforce-owned columns, or insert when allowed, or fail
//     with a NotFoundError;
//  4. clear the DELETED marker when the event is RESTORED, or the ledger's
//     last write for this project was a DELETED (or a RESTORED that failed);
//  5. record the ledger.
func writeSalesforceProject(ctx context.Context, q querier, row domain.SalesforceProjectUpsert, state domain.UpsertSalesforceIngestStateRequest) (domain.SalesforceProjectUpsertResult, error) {
	var res domain.SalesforceProjectUpsertResult
	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0::bigint))`, salesforceProjectLockKey(row.SfID)); err != nil {
		return res, fmt.Errorf("upsert project from salesforce: lock: %w", err)
	}

	args := []any{
		domain.SalesforceSyncActor,
		row.SfID, row.Key, row.Name, row.AccountID, row.StartDate, row.EndDate, row.Description,
		row.ProjectTypeID, row.ComplianceViolationDate, row.GoLiveDate,
	}
	err := q.QueryRow(ctx, resolveSalesforceProjectQuery, row.SfID, row.Key).Scan(&res.ProjectID, &res.LinkedByKey)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if !row.AllowInsert {
			return res, &apierror.NotFoundError{Msg: fmt.Sprintf("project not found for sfId %q / key %q (inserts are off while ServiceNow writes project)", row.SfID, row.Key)}
		}
		if err := q.QueryRow(ctx, insertSalesforceProjectQuery, args...).Scan(&res.ProjectID); err != nil {
			return res, fmt.Errorf("upsert project from salesforce: insert: %w", err)
		}
		res.Created, res.LinkedByKey = true, false
	case err != nil:
		return res, fmt.Errorf("upsert project from salesforce: resolve: %w", err)
	default:
		if _, err := q.Exec(ctx, updateSalesforceProjectQuery, append(args, res.ProjectID)...); err != nil {
			return res, fmt.Errorf("upsert project from salesforce: update: %w", err)
		}
	}

	reactivate := row.Reactivate
	if !reactivate {
		prev, err := getSalesforceIngestState(ctx, q, state.Entity, row.SfID)
		if err != nil {
			return res, fmt.Errorf("upsert project from salesforce: %w", err)
		}
		// A RESTORED that failed is re-run by the retry job as UPDATED, so
		// its FAILED ledger row stands in for the event type.
		reactivate = prev != nil &&
			((prev.EventType == domain.SalesforceEventDeleted && prev.Status == domain.SalesforceIngestSucceeded) ||
				(prev.EventType == domain.SalesforceEventRestored && prev.Status == domain.SalesforceIngestFailed))
	}
	if reactivate {
		tag, err := q.Exec(ctx, reactivateSalesforceProjectQuery, row.SfID, domain.SalesforceSyncActor)
		if err != nil {
			return res, fmt.Errorf("upsert project from salesforce: reactivate: %w", err)
		}
		res.Reactivated = tag.RowsAffected() > 0
	}

	if _, err := upsertSalesforceIngestState(ctx, q, state); err != nil {
		return res, fmt.Errorf("upsert project from salesforce: %w", err)
	}
	return res, nil
}

func (r *salesforceProjectRepo) SoftDeleteBySfID(ctx context.Context, sfID string, state domain.UpsertSalesforceIngestStateRequest) (bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("soft-delete project by sf_id: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	found, err := softDeleteSalesforceProject(ctx, tx, sfID, state)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("soft-delete project by sf_id: commit: %w", err)
	}
	return found, nil
}

func softDeleteSalesforceProject(ctx context.Context, q querier, sfID string, state domain.UpsertSalesforceIngestStateRequest) (bool, error) {
	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0::bigint))`, salesforceProjectLockKey(sfID)); err != nil {
		return false, fmt.Errorf("soft-delete project by sf_id: lock: %w", err)
	}
	tag, err := q.Exec(ctx, softDeleteSalesforceProjectQuery, sfID, domain.SalesforceSyncActor)
	if err != nil {
		return false, fmt.Errorf("soft-delete project by sf_id: %w", err)
	}
	if _, err := upsertSalesforceIngestState(ctx, q, state); err != nil {
		return false, fmt.Errorf("soft-delete project by sf_id: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *salesforceProjectRepo) LookupProjectIDBySfID(ctx context.Context, sfID string) (*string, error) {
	var id string
	err := r.db.QueryRow(ctx, `SELECT id::text FROM project WHERE sf_id = $1 ORDER BY created_on, id LIMIT 1`, sfID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup project id by sf_id: %w", err)
	}
	return &id, nil
}

func (r *salesforceProjectRepo) LookupProjectTypeIDByName(ctx context.Context, name string) (*string, error) {
	var id string
	err := r.db.QueryRow(ctx, `SELECT id::text FROM project_type WHERE name = $1`, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup project type by name: %w", err)
	}
	return &id, nil
}
