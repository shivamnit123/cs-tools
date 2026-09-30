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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/domain"
)

// SalesforceIngestStateRepository persists the per-record ledger of the
// Salesforce ingest (table salesforce_ingest_state, migration 0170). It is
// generic over entity: the account, project and opportunity ingests share
// one table and one repository, each under its own entity value
// (domain.SalesforceIngestEntityAccount and the constants the other
// families add beside it).
//
// A repository that writes its own rows in a transaction records the ledger
// inside that same transaction through upsertSalesforceIngestState, which
// takes a querier (pool or pgx.Tx) exactly as upsertOnboardingStep does, so
// the ledger can never disagree with the row.
type SalesforceIngestStateRepository interface {
	// Get returns the ledger row for one Salesforce record, or nil (no
	// error) when that record was never ingested.
	Get(ctx context.Context, entity, sfID string) (*domain.SalesforceIngestState, error)
	// Upsert writes the latest outcome of one record's ingest. The
	// (entity, sf_id) pair is the primary key: a repeat updates the row;
	// attempt_count counts consecutive failures (see
	// upsertSalesforceIngestState).
	Upsert(ctx context.Context, req domain.UpsertSalesforceIngestStateRequest) (domain.SalesforceIngestState, error)
	// ListMissingParentFailures returns the FAILED rows the delayed-retry
	// job can act on: entity in entities (those with a registered retrier),
	// last_error a missing-parent error (missingParentErrorPatterns),
	// attempt_count below maxAttempts and the last write older than
	// olderThan; oldest first, at most limit of them. Filtering before the
	// LIMIT keeps a backlog of rows the job would skip from starving the
	// ones it would retry.
	ListMissingParentFailures(ctx context.Context, entities []string, olderThan time.Duration, maxAttempts, limit int) ([]domain.SalesforceIngestState, error)
	// RecordRetryAttempt counts one failed re-run of a FAILED row that the
	// re-run itself did not record (it failed before its own ledger write,
	// e.g. the Sales Entity fetch): attempt_count + 1 and updated_on = now(),
	// last_error kept. It only applies while the row is still FAILED and its
	// updated_on is still seenUpdatedOn, so an outcome written in the
	// meantime is never overwritten. Reports whether a row was updated.
	RecordRetryAttempt(ctx context.Context, entity, sfID string, seenUpdatedOn time.Time) (bool, error)
}

type salesforceIngestStateRepo struct {
	db *pgxpool.Pool
}

// NewSalesforceIngestStateRepository constructs a SalesforceIngestStateRepository backed by the pool.
func NewSalesforceIngestStateRepository(db *pgxpool.Pool) SalesforceIngestStateRepository {
	return &salesforceIngestStateRepo{db: db}
}

const salesforceIngestStateColumns = `
	entity, sf_id, event_modified_on, event_type, status, last_error, attempt_count,
	created_on, updated_on`

func scanSalesforceIngestState(row pgx.Row) (domain.SalesforceIngestState, error) {
	var s domain.SalesforceIngestState
	var status string
	if err := row.Scan(
		&s.Entity, &s.SfID, &s.EventModifiedOn, &s.EventType, &status, &s.LastError, &s.AttemptCount,
		&s.CreatedOn, &s.UpdatedOn,
	); err != nil {
		return domain.SalesforceIngestState{}, err
	}
	s.Status = domain.SalesforceIngestStatus(status)
	return s, nil
}

func (r *salesforceIngestStateRepo) Get(ctx context.Context, entity, sfID string) (*domain.SalesforceIngestState, error) {
	return getSalesforceIngestState(ctx, r.db, entity, sfID)
}

func getSalesforceIngestState(ctx context.Context, q querier, entity, sfID string) (*domain.SalesforceIngestState, error) {
	s, err := scanSalesforceIngestState(q.QueryRow(ctx, `SELECT `+salesforceIngestStateColumns+`
		FROM salesforce_ingest_state WHERE entity = $1 AND sf_id = $2`, entity, sfID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get salesforce ingest state: %w", err)
	}
	return &s, nil
}

func (r *salesforceIngestStateRepo) Upsert(ctx context.Context, req domain.UpsertSalesforceIngestStateRequest) (domain.SalesforceIngestState, error) {
	return upsertSalesforceIngestState(ctx, r.db, req)
}

// upsertSalesforceIngestState is shared with the per-entity repositories,
// which record the ledger inside their own transaction (q is then that
// transaction).
//
// Retries and out-of-order deliveries hit the same (entity, sf_id) row, so the
// outcome columns (status, last_error, event_type, event_modified_on) only
// move when the incoming event is at least as new as the recorded one, or
// when the recorded row was stamped by a DELETED event (an undelete keeps the
// Salesforce LastModifiedDate, and the row must be allowed to leave that
// state) — the same rule upsertOnboardingStep applies. updated_on advances on
// every write so a stale retry is still visible. attempt_count counts
// consecutive failures: it goes up while the row stays FAILED and restarts
// at 1 on a success or on the first failure after one, so a record that
// Salesforce saves often never reaches the retry job's cap by succeeding.
func upsertSalesforceIngestState(ctx context.Context, q querier, req domain.UpsertSalesforceIngestStateRequest) (domain.SalesforceIngestState, error) {
	row, err := scanSalesforceIngestState(q.QueryRow(ctx, `
		INSERT INTO salesforce_ingest_state (
			entity, sf_id, event_modified_on, event_type, status, last_error
		) VALUES (
			$1, $2, $3, $4, $5, $6
		)
		ON CONFLICT (entity, sf_id) DO UPDATE SET
			status            = CASE WHEN EXCLUDED.event_modified_on >= salesforce_ingest_state.event_modified_on OR salesforce_ingest_state.event_type = 'DELETED' THEN EXCLUDED.status ELSE salesforce_ingest_state.status END,
			last_error        = CASE WHEN EXCLUDED.event_modified_on >= salesforce_ingest_state.event_modified_on OR salesforce_ingest_state.event_type = 'DELETED' THEN EXCLUDED.last_error ELSE salesforce_ingest_state.last_error END,
			event_type        = CASE WHEN EXCLUDED.event_modified_on >= salesforce_ingest_state.event_modified_on OR salesforce_ingest_state.event_type = 'DELETED' THEN EXCLUDED.event_type ELSE salesforce_ingest_state.event_type END,
			event_modified_on = GREATEST(EXCLUDED.event_modified_on, salesforce_ingest_state.event_modified_on),
			attempt_count     = CASE WHEN salesforce_ingest_state.status = 'FAILED'
			                          AND (NOT (EXCLUDED.event_modified_on >= salesforce_ingest_state.event_modified_on OR salesforce_ingest_state.event_type = 'DELETED')
			                               OR EXCLUDED.status = 'FAILED')
			                         THEN salesforce_ingest_state.attempt_count + 1 ELSE 1 END,
			updated_on        = NOW()
		RETURNING `+salesforceIngestStateColumns,
		req.Entity, req.SfID, req.EventModifiedOn, req.EventType, string(req.Status), req.LastError,
	))
	if err != nil {
		return domain.SalesforceIngestState{}, fmt.Errorf("upsert salesforce ingest state: %w", err)
	}
	return row, nil
}

func (r *salesforceIngestStateRepo) ListMissingParentFailures(ctx context.Context, entities []string, olderThan time.Duration, maxAttempts, limit int) ([]domain.SalesforceIngestState, error) {
	if len(entities) == 0 {
		return []domain.SalesforceIngestState{}, nil
	}
	rows, err := r.db.Query(ctx, `SELECT `+salesforceIngestStateColumns+`
		FROM salesforce_ingest_state
		WHERE status = $1
		  AND entity = ANY($2::text[])
		  AND last_error ILIKE ANY($3::text[])
		  AND attempt_count < $4
		  AND updated_on < NOW() - make_interval(secs => $5::int)
		ORDER BY updated_on, entity, sf_id
		LIMIT $6`, string(domain.SalesforceIngestFailed), entities, missingParentErrorPatterns,
		maxAttempts, int(olderThan.Seconds()), limit)
	if err != nil {
		return nil, fmt.Errorf("query failed salesforce ingest states: %w", err)
	}
	defer rows.Close()
	out := []domain.SalesforceIngestState{}
	for rows.Next() {
		s, err := scanSalesforceIngestState(rows)
		if err != nil {
			return nil, fmt.Errorf("scan salesforce ingest state: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate salesforce ingest states: %w", err)
	}
	return out, nil
}

func (r *salesforceIngestStateRepo) RecordRetryAttempt(ctx context.Context, entity, sfID string, seenUpdatedOn time.Time) (bool, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE salesforce_ingest_state
		   SET attempt_count = attempt_count + 1, updated_on = NOW()
		 WHERE entity = $1 AND sf_id = $2 AND status = $3 AND updated_on = $4`,
		entity, sfID, string(domain.SalesforceIngestFailed), seenUpdatedOn)
	if err != nil {
		return false, fmt.Errorf("record salesforce ingest retry attempt: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
