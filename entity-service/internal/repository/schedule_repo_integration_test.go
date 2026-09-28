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

// These tests exercise the real SQL in schedule_repo.go against a live
// PostgreSQL instance.
//
// The service-level tests run against an in-memory fake, so everything that
// makes this feature work is invisible to them: the GiST range index that
// answers who is on call at an instant, the absence exclusion that keeps
// somebody on leave out of that answer, the day-scope skip that lets a
// weekday rotation cross a weekend, and the trim-and-split that shortens a
// stretch of leave instead of cancelling it. None of that has behaviour a
// fake can reproduce -- it is all SQL -- and this file is where it is checked.
//
// Skipped unless ENTITY_TEST_DATABASE_URL is set, so `go test ./...` on a
// machine with no database stays green. Apply every migration in order first;
// this file reads tables and enums spread across 000088-000096:
//
//	createdb entity_test
//	for f in migrations/*.up.sql; do psql -v ON_ERROR_STOP=1 -d entity_test -f "$f"; done
//	ENTITY_TEST_DATABASE_URL="postgres:///entity_test" go test -v -run TestScheduleIntegration ./internal/repository/

package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/domain"
)

// Fixture ids, distinct from every other integration file's so the two can run
// in the same database without clearing each other's rows.
const (
	schedTeamID   = "5c8e0000-0000-4000-8000-000000000001"
	schedLeadID   = "5c8e0000-0000-4000-8000-000000000002"
	schedMemberID = "5c8e0000-0000-4000-8000-000000000003"
	schedOtherID  = "5c8e0000-0000-4000-8000-000000000004"

	schedLeadEmail   = "sched.lead@example.test"
	schedMemberEmail = "sched.member@example.test"
	schedOtherEmail  = "sched.other@example.test"
	schedTeamKey     = "schedfixture"
	schedOtherTeam   = "schedother"

	// A Monday, so the weekday/weekend arithmetic below reads plainly.
	schedMonday = "2026-09-21"
)

// newScheduleIntegrationRepo connects, rebuilds this file's fixtures from
// scratch so the tests are order-independent and rerunnable, and returns a
// repository over a real pool.
func newScheduleIntegrationRepo(t *testing.T) (ScheduleRepository, *pgxpool.Pool) {
	t.Helper()

	dsn := os.Getenv("ENTITY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ENTITY_TEST_DATABASE_URL is not set; skipping the live-database tests")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	// Torn down first, not after: a test that fails half way should still
	// leave the next run a clean slate.
	for _, stmt := range []string{
		`DELETE FROM team_schedule_assignment_activity WHERE team_key IN ($1, $2)`,
		`DELETE FROM team_schedule_absence_activity WHERE team_key IN ($1, $2)`,
		`DELETE FROM team_schedule_assignment WHERE team_key IN ($1, $2)`,
		`DELETE FROM team_schedule_absence WHERE team_key IN ($1, $2)`,
	} {
		mustExec(t, pool, stmt, schedTeamKey, schedOtherTeam)
	}
	mustExec(t, pool, `DELETE FROM team_member WHERE user_id IN ($1, $2, $3)`,
		schedLeadID, schedMemberID, schedOtherID)

	mustExec(t, pool, `
		INSERT INTO team (id, created_on, updated_on, created_by, updated_by, name, key, type)
		VALUES ($1, NOW(), NOW(), 'fixture', 'fixture', $2, $2, 'cre-abt')
		ON CONFLICT (id) DO NOTHING`, schedTeamID, schedTeamKey)

	// Two of these deliberately share a display name. A rota can carry two
	// people called the same thing, and the history has to survive it.
	for _, u := range []struct{ id, email, first, last string }{
		{schedLeadID, schedLeadEmail, "Sched", "Lead"},
		{schedMemberID, schedMemberEmail, "Chamara", "Perera"},
		{schedOtherID, schedOtherEmail, "Chamara", "Perera"},
	} {
		mustExec(t, pool, `
			INSERT INTO "user" (id, created_on, updated_on, created_by, updated_by,
			                    user_name, first_name, last_name, email)
			VALUES ($1, NOW(), NOW(), 'fixture', 'fixture', $2, $3, $4, $2)
			ON CONFLICT (id) DO UPDATE SET first_name = EXCLUDED.first_name, last_name = EXCLUDED.last_name`,
			u.id, u.email, u.first, u.last)
	}

	mustExec(t, pool, `
		INSERT INTO team_member (id, created_on, updated_on, created_by, updated_by, team_id, user_id, role)
		VALUES (gen_random_uuid(), NOW(), NOW(), 'fixture', 'fixture', $1, $2, 'lead')`,
		schedTeamID, schedLeadID)
	mustExec(t, pool, `
		INSERT INTO team_member (id, created_on, updated_on, created_by, updated_by, team_id, user_id, role)
		VALUES (gen_random_uuid(), NOW(), NOW(), 'fixture', 'fixture', $1, $2, 'member')`,
		schedTeamID, schedMemberID)

	return NewScheduleRepository(pool), pool
}

// weekdayShift returns a shift code the catalogue actually has for the given
// day scope, so these tests bind to the seeded catalogue rather than inventing
// codes that a later migration might rename.
func shiftWithScope(t *testing.T, pool *pgxpool.Pool, family, scope string) string {
	t.Helper()
	var code string
	err := pool.QueryRow(context.Background(), `
		SELECT code FROM team_schedule_shift
		 WHERE family::text = $1 AND day_scope::text = $2
		 ORDER BY sort_order LIMIT 1`, family, scope).Scan(&code)
	if err != nil {
		t.Fatalf("no %s %s shift in the catalogue: %v", family, scope, err)
	}
	return code
}

func countRows(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// ── the catalogue ─────────────────────────────────────────────────────────

func TestScheduleIntegration_CatalogueServesAllThreeParts(t *testing.T) {
	repo, _ := newScheduleIntegrationRepo(t)

	cat, err := repo.Catalogue(context.Background())
	if err != nil {
		t.Fatalf("Catalogue: %v", err)
	}
	// One payload because a client needs all of it to draw a single day.
	if len(cat.Zones) == 0 || len(cat.Shifts) == 0 || len(cat.AbsenceKinds) == 0 {
		t.Fatalf("got %d zones, %d shifts, %d absence kinds; want all three populated",
			len(cat.Zones), len(cat.Shifts), len(cat.AbsenceKinds))
	}

	// The teams too, since the frontend no longer holds that list: if this
	// comes back empty the team picker renders empty and every team draws in
	// the same fallback grey.
	var fixture *domain.ScheduleTeam
	for i := range cat.Teams {
		if cat.Teams[i].Key == schedTeamKey {
			fixture = &cat.Teams[i]
		}
	}
	if fixture == nil {
		t.Fatalf("the fixture team is missing from %d served teams", len(cat.Teams))
	}
	if fixture.Family != "CRE" {
		t.Fatalf("fixture team family %q, want CRE from its type", fixture.Family)
	}
	// Positions are what colour a team, so they have to be 1-based and
	// distinct -- a zero or a repeat puts two teams in one colour.
	seen := map[int]string{}
	for _, tm := range cat.Teams {
		if tm.SortOrder < 1 {
			t.Fatalf("team %s has sortOrder %d, want 1 or more", tm.Key, tm.SortOrder)
		}
		if other, dup := seen[tm.SortOrder]; dup {
			t.Fatalf("teams %s and %s share sortOrder %d", other, tm.Key, tm.SortOrder)
		}
		seen[tm.SortOrder] = tm.Key
	}
}

// ── who may edit ──────────────────────────────────────────────────────────

func TestScheduleIntegration_LeadsTeamIsTrueOnlyForTheLead(t *testing.T) {
	repo, _ := newScheduleIntegrationRepo(t)
	ctx := context.Background()

	lead, err := repo.LeadsTeam(ctx, schedLeadEmail, schedTeamKey)
	if err != nil {
		t.Fatalf("LeadsTeam(lead): %v", err)
	}
	if !lead {
		t.Fatal("the team's lead was not recognised as its lead")
	}

	// A member of the same team is not a lead of it: the whole edit
	// permission rests on this distinction.
	member, err := repo.LeadsTeam(ctx, schedMemberEmail, schedTeamKey)
	if err != nil {
		t.Fatalf("LeadsTeam(member): %v", err)
	}
	if member {
		t.Fatal("an ordinary member was reported as leading the team")
	}

	// Leading one team says nothing about another.
	elsewhere, err := repo.LeadsTeam(ctx, schedLeadEmail, schedOtherTeam)
	if err != nil {
		t.Fatalf("LeadsTeam(other team): %v", err)
	}
	if elsewhere {
		t.Fatal("a lead was reported as leading a team they are not on")
	}
}

func TestScheduleIntegration_LeadTeamsForListsOnlyLedTeams(t *testing.T) {
	repo, _ := newScheduleIntegrationRepo(t)
	ctx := context.Background()

	teams, err := repo.LeadTeamsFor(ctx, schedLeadEmail)
	if err != nil {
		t.Fatalf("LeadTeamsFor: %v", err)
	}
	var found bool
	for _, k := range teams {
		if k == schedTeamKey {
			found = true
		}
	}
	if !found {
		t.Fatalf("the lead's own team is missing from %v", teams)
	}

	member, err := repo.LeadTeamsFor(ctx, schedMemberEmail)
	if err != nil {
		t.Fatalf("LeadTeamsFor(member): %v", err)
	}
	for _, k := range member {
		if k == schedTeamKey {
			t.Fatalf("a member was told they lead %s", k)
		}
	}
}

// ── ApplyRange ────────────────────────────────────────────────────────────

// The headline behaviour: a weekday rotation asked for across a week sets the
// five weekdays and skips the Saturday and Sunday rather than refusing the
// whole call or forcing them.
func TestScheduleIntegration_ApplyRangeSkipsDaysTheWindowIsNotWorkedOn(t *testing.T) {
	repo, pool := newScheduleIntegrationRepo(t)
	ctx := context.Background()
	code := shiftWithScope(t, pool, "CRE", "WEEKDAY")

	res, err := repo.ApplyRange(ctx, domain.ApplyScheduleRangeRequest{
		UserID:    schedMemberID,
		TeamKey:   schedTeamKey,
		ShiftCode: code,
		From:      schedMonday,  // Mon 21 Sept
		To:        "2026-09-27", // Sun 27 Sept
	}, schedLeadEmail)
	if err != nil {
		t.Fatalf("ApplyRange: %v", err)
	}
	if res.Applied != 5 {
		t.Fatalf("applied %d days, want the 5 weekdays", res.Applied)
	}
	if res.Skipped != 2 {
		t.Fatalf("skipped %d days, want the 2 weekend days", res.Skipped)
	}
	// It reports which, not only how many, so a caller can say why.
	if len(res.SkippedDates) != 2 ||
		res.SkippedDates[0] != "2026-09-26" || res.SkippedDates[1] != "2026-09-27" {
		t.Fatalf("skipped dates %v, want the Saturday and the Sunday", res.SkippedDates)
	}

	if n := countRows(t, pool,
		`SELECT count(*) FROM team_schedule_assignment WHERE user_id = $1::uuid AND rota_date BETWEEN $2::date AND $3::date`,
		schedMemberID, schedMonday, "2026-09-27"); n != 5 {
		t.Fatalf("%d assignment rows in the range, want 5", n)
	}
}

// Every day is one slot per person, so applying over a day that already holds
// something replaces it -- and the replaced row is recorded before it goes,
// or the history would show a row appearing from nowhere.
func TestScheduleIntegration_ApplyRangeReplacesAndRecordsWhatItDisplaced(t *testing.T) {
	repo, pool := newScheduleIntegrationRepo(t)
	ctx := context.Background()
	first := shiftWithScope(t, pool, "CRE", "WEEKDAY")

	base := domain.ApplyScheduleRangeRequest{
		UserID: schedMemberID, TeamKey: schedTeamKey, ShiftCode: first,
		From: schedMonday, To: schedMonday,
	}
	if _, err := repo.ApplyRange(ctx, base, schedLeadEmail); err != nil {
		t.Fatalf("first ApplyRange: %v", err)
	}
	if _, err := repo.ApplyRange(ctx, base, schedLeadEmail); err != nil {
		t.Fatalf("second ApplyRange: %v", err)
	}

	if n := countRows(t, pool,
		`SELECT count(*) FROM team_schedule_assignment WHERE user_id = $1::uuid AND rota_date = $2::date`,
		schedMemberID, schedMonday); n != 1 {
		t.Fatalf("%d rows on the day, want exactly 1 -- the day is one slot per person", n)
	}
	if n := countRows(t, pool,
		`SELECT count(*) FROM team_schedule_assignment_activity WHERE user_id = $1::uuid AND action = 'DELETED'`,
		schedMemberID); n != 1 {
		t.Fatalf("%d DELETED activity rows, want 1 for the displaced assignment", n)
	}
	if n := countRows(t, pool,
		`SELECT count(*) FROM team_schedule_assignment_activity WHERE user_id = $1::uuid AND action = 'CREATED'`,
		schedMemberID); n != 2 {
		t.Fatalf("%d CREATED activity rows, want 2", n)
	}
}

// An empty shift code is the picker's clear: it takes the days off the rota
// rather than putting anybody on a window.
func TestScheduleIntegration_ApplyRangeWithNoShiftCodeClearsTheSpan(t *testing.T) {
	repo, pool := newScheduleIntegrationRepo(t)
	ctx := context.Background()
	code := shiftWithScope(t, pool, "CRE", "WEEKDAY")

	if _, err := repo.ApplyRange(ctx, domain.ApplyScheduleRangeRequest{
		UserID: schedMemberID, TeamKey: schedTeamKey, ShiftCode: code,
		From: schedMonday, To: "2026-09-23",
	}, schedLeadEmail); err != nil {
		t.Fatalf("seed ApplyRange: %v", err)
	}

	res, err := repo.ApplyRange(ctx, domain.ApplyScheduleRangeRequest{
		UserID: schedMemberID, TeamKey: schedTeamKey, ShiftCode: "",
		From: schedMonday, To: "2026-09-23",
	}, schedLeadEmail)
	if err != nil {
		t.Fatalf("clearing ApplyRange: %v", err)
	}
	// Nothing is skipped when clearing: there is no window whose day scope
	// could rule a day out.
	if res.Skipped != 0 {
		t.Fatalf("skipped %d days while clearing, want 0", res.Skipped)
	}
	if res.Applied != 3 {
		t.Fatalf("cleared %d days, want the 3 that held something", res.Applied)
	}
	if n := countRows(t, pool,
		`SELECT count(*) FROM team_schedule_assignment WHERE user_id = $1::uuid`, schedMemberID); n != 0 {
		t.Fatalf("%d assignments left after clearing, want 0", n)
	}
}

func TestScheduleIntegration_ApplyRangeRefusesAnUnknownShift(t *testing.T) {
	repo, _ := newScheduleIntegrationRepo(t)

	_, err := repo.ApplyRange(context.Background(), domain.ApplyScheduleRangeRequest{
		UserID: schedMemberID, TeamKey: schedTeamKey, ShiftCode: "NO_SUCH_WINDOW",
		From: schedMonday, To: schedMonday,
	}, schedLeadEmail)
	if err == nil {
		t.Fatal("a shift code that is not in the catalogue was accepted")
	}
}

// A backwards range is a slip, not a refusal: the ends are swapped.
func TestScheduleIntegration_ApplyRangeAcceptsABackwardsSpan(t *testing.T) {
	repo, pool := newScheduleIntegrationRepo(t)
	code := shiftWithScope(t, pool, "CRE", "WEEKDAY")

	res, err := repo.ApplyRange(context.Background(), domain.ApplyScheduleRangeRequest{
		UserID: schedMemberID, TeamKey: schedTeamKey, ShiftCode: code,
		From: "2026-09-23", To: schedMonday,
	}, schedLeadEmail)
	if err != nil {
		t.Fatalf("ApplyRange: %v", err)
	}
	if res.Applied != 3 {
		t.Fatalf("applied %d days, want 3", res.Applied)
	}
}

// ── ApplyAbsence ──────────────────────────────────────────────────────────

func absenceKind(t *testing.T, pool *pgxpool.Pool, bucket string) string {
	t.Helper()
	var code string
	if err := pool.QueryRow(context.Background(),
		`SELECT code FROM team_schedule_absence_kind WHERE bucket = $1 ORDER BY sort_order LIMIT 1`,
		bucket).Scan(&code); err != nil {
		t.Fatalf("no %s absence kind: %v", bucket, err)
	}
	return code
}

// Leave is a span, not a day at a time: a weekend inside it is covered too,
// which is the opposite of how a weekday rotation behaves.
func TestScheduleIntegration_ApplyAbsenceMarksTheWholeSpanIncludingTheWeekend(t *testing.T) {
	repo, pool := newScheduleIntegrationRepo(t)
	kind := absenceKind(t, pool, "LEAVE")

	res, err := repo.ApplyAbsence(context.Background(), domain.ApplyScheduleAbsenceRequest{
		UserID: schedMemberID, TeamKey: schedTeamKey, KindCode: kind,
		From: "2026-09-25", To: "2026-09-28", // Fri through Mon
	}, schedLeadEmail)
	if err != nil {
		t.Fatalf("ApplyAbsence: %v", err)
	}
	if res.Created != 1 {
		t.Fatalf("created %d rows, want 1 span", res.Created)
	}
	var starts, ends time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT starts_on, ends_on FROM team_schedule_absence WHERE user_id = $1::uuid`,
		schedMemberID).Scan(&starts, &ends); err != nil {
		t.Fatalf("read the absence back: %v", err)
	}
	if starts.Format("2006-01-02") != "2026-09-25" || ends.Format("2006-01-02") != "2026-09-28" {
		t.Fatalf("stored %s..%s, want the span as asked for -- weekend included",
			starts.Format("2006-01-02"), ends.Format("2006-01-02"))
	}
}

// Clearing three days out of a fortnight of leave means exactly that. The
// stretch either side still stands, so one row becomes two.
func TestScheduleIntegration_ClearingTheMiddleOfALeaveSpanSplitsIt(t *testing.T) {
	repo, pool := newScheduleIntegrationRepo(t)
	ctx := context.Background()
	kind := absenceKind(t, pool, "LEAVE")

	if _, err := repo.ApplyAbsence(ctx, domain.ApplyScheduleAbsenceRequest{
		UserID: schedMemberID, TeamKey: schedTeamKey, KindCode: kind,
		From: "2026-09-01", To: "2026-09-14",
	}, schedLeadEmail); err != nil {
		t.Fatalf("seed ApplyAbsence: %v", err)
	}

	res, err := repo.ApplyAbsence(ctx, domain.ApplyScheduleAbsenceRequest{
		UserID: schedMemberID, TeamKey: schedTeamKey, KindCode: "",
		From: "2026-09-06", To: "2026-09-08",
	}, schedLeadEmail)
	if err != nil {
		t.Fatalf("clearing ApplyAbsence: %v", err)
	}
	if res.Trimmed != 1 {
		t.Fatalf("trimmed %d, want 1 -- the span straddles both ends of the hole", res.Trimmed)
	}
	if res.Removed != 0 {
		t.Fatalf("removed %d, want 0 -- most of the leave still stands", res.Removed)
	}

	rows, err := pool.Query(ctx,
		`SELECT starts_on, ends_on FROM team_schedule_absence WHERE user_id = $1::uuid ORDER BY starts_on`,
		schedMemberID)
	if err != nil {
		t.Fatalf("read the absences back: %v", err)
	}
	defer rows.Close()
	var spans [][2]string
	for rows.Next() {
		var s, e time.Time
		if err := rows.Scan(&s, &e); err != nil {
			t.Fatalf("scan: %v", err)
		}
		spans = append(spans, [2]string{s.Format("2006-01-02"), e.Format("2006-01-02")})
	}
	if len(spans) != 2 {
		t.Fatalf("got %d spans %v, want the two stretches either side of the hole", len(spans), spans)
	}
	if spans[0] != [2]string{"2026-09-01", "2026-09-05"} {
		t.Fatalf("first span %v, want 01..05", spans[0])
	}
	if spans[1] != [2]string{"2026-09-09", "2026-09-14"} {
		t.Fatalf("second span %v, want 09..14", spans[1])
	}
}

// An absence entirely inside the cleared span has nothing left to keep.
func TestScheduleIntegration_ClearingOverALeaveSpanRemovesIt(t *testing.T) {
	repo, pool := newScheduleIntegrationRepo(t)
	ctx := context.Background()
	kind := absenceKind(t, pool, "LEAVE")

	if _, err := repo.ApplyAbsence(ctx, domain.ApplyScheduleAbsenceRequest{
		UserID: schedMemberID, TeamKey: schedTeamKey, KindCode: kind,
		From: "2026-09-10", To: "2026-09-11",
	}, schedLeadEmail); err != nil {
		t.Fatalf("seed ApplyAbsence: %v", err)
	}

	res, err := repo.ApplyAbsence(ctx, domain.ApplyScheduleAbsenceRequest{
		UserID: schedMemberID, TeamKey: schedTeamKey, KindCode: "",
		From: "2026-09-01", To: "2026-09-30",
	}, schedLeadEmail)
	if err != nil {
		t.Fatalf("clearing ApplyAbsence: %v", err)
	}
	if res.Removed != 1 || res.Trimmed != 0 {
		t.Fatalf("removed %d trimmed %d, want removed 1 trimmed 0", res.Removed, res.Trimmed)
	}
	if n := countRows(t, pool,
		`SELECT count(*) FROM team_schedule_absence WHERE user_id = $1::uuid`, schedMemberID); n != 0 {
		t.Fatalf("%d absences left, want 0", n)
	}
	// The removal is recorded, since the row it describes is gone.
	if n := countRows(t, pool,
		`SELECT count(*) FROM team_schedule_absence_activity WHERE user_id = $1::uuid AND action = 'DELETED'`,
		schedMemberID); n != 1 {
		t.Fatalf("%d DELETED absence activity rows, want 1", n)
	}
}

// Marking leave over a stretch that already holds some replaces it rather
// than leaving two overlapping spans behind.
func TestScheduleIntegration_MarkingOverExistingLeaveLeavesOneSpan(t *testing.T) {
	repo, pool := newScheduleIntegrationRepo(t)
	ctx := context.Background()
	leave := absenceKind(t, pool, "LEAVE")

	for _, span := range [][2]string{{"2026-09-02", "2026-09-03"}, {"2026-09-01", "2026-09-05"}} {
		if _, err := repo.ApplyAbsence(ctx, domain.ApplyScheduleAbsenceRequest{
			UserID: schedMemberID, TeamKey: schedTeamKey, KindCode: leave,
			From: span[0], To: span[1],
		}, schedLeadEmail); err != nil {
			t.Fatalf("ApplyAbsence %v: %v", span, err)
		}
	}
	if n := countRows(t, pool,
		`SELECT count(*) FROM team_schedule_absence WHERE user_id = $1::uuid`, schedMemberID); n != 1 {
		t.Fatalf("%d absence rows, want 1 -- the second span swallowed the first", n)
	}
}

func TestScheduleIntegration_ApplyAbsenceRefusesAnUnknownKind(t *testing.T) {
	repo, _ := newScheduleIntegrationRepo(t)

	_, err := repo.ApplyAbsence(context.Background(), domain.ApplyScheduleAbsenceRequest{
		UserID: schedMemberID, TeamKey: schedTeamKey, KindCode: "NO_SUCH_KIND",
		From: schedMonday, To: schedMonday,
	}, schedLeadEmail)
	if err == nil {
		t.Fatal("an absence kind that is not in the catalogue was accepted")
	}
}

// ── reads ─────────────────────────────────────────────────────────────────

func TestScheduleIntegration_SearchAssignmentsFiltersByTeamAndWindow(t *testing.T) {
	repo, pool := newScheduleIntegrationRepo(t)
	ctx := context.Background()
	code := shiftWithScope(t, pool, "CRE", "WEEKDAY")

	if _, err := repo.ApplyRange(ctx, domain.ApplyScheduleRangeRequest{
		UserID: schedMemberID, TeamKey: schedTeamKey, ShiftCode: code,
		From: schedMonday, To: "2026-09-23",
	}, schedLeadEmail); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := repo.SearchAssignments(ctx, domain.SearchScheduleAssignmentsRequest{
		From: schedMonday, To: "2026-09-23", TeamKeys: []string{schedTeamKey},
	})
	if err != nil {
		t.Fatalf("SearchAssignments: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d assignments, want 3", len(got))
	}

	// A window that ends before the rota starts finds nothing.
	none, err := repo.SearchAssignments(ctx, domain.SearchScheduleAssignmentsRequest{
		From: "2026-08-01", To: "2026-08-02", TeamKeys: []string{schedTeamKey},
	})
	if err != nil {
		t.Fatalf("SearchAssignments(empty window): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("got %d assignments outside the window, want 0", len(none))
	}

	// Asking by email resolves to the same person, which is what saves every
	// client its own lookup.
	byEmail, err := repo.SearchAssignments(ctx, domain.SearchScheduleAssignmentsRequest{
		From: schedMonday, To: "2026-09-23", UserEmail: schedMemberEmail,
	})
	if err != nil {
		t.Fatalf("SearchAssignments(by email): %v", err)
	}
	if len(byEmail) != 3 {
		t.Fatalf("got %d assignments by email, want 3", len(byEmail))
	}
}

// The point of the GiST range index: on-call is a containment question, and
// the answer has to exclude anyone on leave or it names somebody who is away.
func TestScheduleIntegration_OnDutyAtExcludesSomebodyOnLeave(t *testing.T) {
	repo, pool := newScheduleIntegrationRepo(t)
	ctx := context.Background()
	code := shiftWithScope(t, pool, "CRE", "WEEKDAY")

	if _, err := repo.ApplyRange(ctx, domain.ApplyScheduleRangeRequest{
		UserID: schedMemberID, TeamKey: schedTeamKey, ShiftCode: code,
		From: schedMonday, To: schedMonday,
	}, schedLeadEmail); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var startsAt, endsAt time.Time
	if err := pool.QueryRow(ctx,
		`SELECT starts_at, ends_at FROM team_schedule_assignment WHERE user_id = $1::uuid`,
		schedMemberID).Scan(&startsAt, &endsAt); err != nil {
		t.Fatalf("read the window back: %v", err)
	}
	middle := startsAt.Add(endsAt.Sub(startsAt) / 2)

	onDuty := func() bool {
		rows, err := repo.OnDutyAt(ctx, middle)
		if err != nil {
			t.Fatalf("OnDutyAt: %v", err)
		}
		for _, a := range rows {
			if a.Engineer.UserID == schedMemberID {
				return true
			}
		}
		return false
	}

	if !onDuty() {
		t.Fatal("an engineer inside their own window was not reported on duty")
	}

	// Just outside it, they are not.
	if rows, err := repo.OnDutyAt(ctx, endsAt.Add(time.Hour)); err != nil {
		t.Fatalf("OnDutyAt(after): %v", err)
	} else {
		for _, a := range rows {
			if a.Engineer.UserID == schedMemberID {
				t.Fatal("an engineer was reported on duty an hour after their window ended")
			}
		}
	}

	// Now book leave over the same day. The assignment is untouched -- leave
	// covers the rota rather than deleting it -- so this is exactly the case
	// a query that forgot to reconcile the two would get wrong.
	if _, err := repo.ApplyAbsence(ctx, domain.ApplyScheduleAbsenceRequest{
		UserID: schedMemberID, TeamKey: schedTeamKey,
		KindCode: absenceKind(t, pool, "LEAVE"),
		From:     schedMonday, To: schedMonday,
	}, schedLeadEmail); err != nil {
		t.Fatalf("ApplyAbsence: %v", err)
	}
	if n := countRows(t, pool,
		`SELECT count(*) FROM team_schedule_assignment WHERE user_id = $1::uuid`, schedMemberID); n != 1 {
		t.Fatalf("the assignment went away when leave was booked; %d rows left", n)
	}
	if onDuty() {
		t.Fatal("somebody on leave was still reported on duty")
	}
}

func TestScheduleIntegration_SearchAbsencesFindsAnOverlappingSpan(t *testing.T) {
	repo, pool := newScheduleIntegrationRepo(t)
	ctx := context.Background()

	if _, err := repo.ApplyAbsence(ctx, domain.ApplyScheduleAbsenceRequest{
		UserID: schedMemberID, TeamKey: schedTeamKey,
		KindCode: absenceKind(t, pool, "LEAVE"),
		From:     "2026-09-10", To: "2026-09-20",
	}, schedLeadEmail); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A window touching only the tail of the span still finds it: the search
	// is an overlap, not a containment.
	got, err := repo.SearchAbsences(ctx, domain.SearchScheduleAbsencesRequest{
		From: "2026-09-19", To: "2026-09-25", TeamKeys: []string{schedTeamKey},
	})
	if err != nil {
		t.Fatalf("SearchAbsences: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d absences overlapping the tail, want 1", len(got))
	}

	none, err := repo.SearchAbsences(ctx, domain.SearchScheduleAbsencesRequest{
		From: "2026-09-21", To: "2026-09-25", TeamKeys: []string{schedTeamKey},
	})
	if err != nil {
		t.Fatalf("SearchAbsences(after): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("got %d absences after the span ended, want 0", len(none))
	}
}

// ── the single-row writes, and the history they leave ─────────────────────

func TestScheduleIntegration_CreateUpdateDeleteLeaveATrail(t *testing.T) {
	repo, pool := newScheduleIntegrationRepo(t)
	ctx := context.Background()
	code := shiftWithScope(t, pool, "CRE", "WEEKDAY")

	made, err := repo.CreateAssignment(ctx, domain.CreateScheduleAssignmentRequest{
		UserID: schedMemberID, TeamKey: schedTeamKey, ShiftCode: code, RotaDate: schedMonday,
	}, schedLeadEmail)
	if err != nil {
		t.Fatalf("CreateAssignment: %v", err)
	}
	// StartsAt/EndsAt are resolved from the window rather than accepted from
	// the caller, so a hand-placed cover cannot drift from what it claims.
	if made.StartsAt.IsZero() || !made.EndsAt.After(made.StartsAt) {
		t.Fatalf("resolved window %s..%s is not a forward span", made.StartsAt, made.EndsAt)
	}

	back, err := repo.AssignmentByID(ctx, made.ID)
	if err != nil {
		t.Fatalf("AssignmentByID: %v", err)
	}
	if back.TeamKey != schedTeamKey {
		t.Fatalf("read back team %q, want %q -- the service checks this to decide who may edit",
			back.TeamKey, schedTeamKey)
	}

	moved := schedOtherID
	if _, err := repo.UpdateAssignment(ctx, made.ID,
		domain.UpdateScheduleAssignmentRequest{UserID: &moved}, schedLeadEmail); err != nil {
		t.Fatalf("UpdateAssignment: %v", err)
	}
	// Handing a slot to somebody else is a swap, not a plain edit, and the
	// row says so.
	var source string
	if err := pool.QueryRow(ctx,
		`SELECT source FROM team_schedule_assignment WHERE id = $1::uuid`, made.ID).Scan(&source); err != nil {
		t.Fatalf("read source: %v", err)
	}
	if source != "SWAP" {
		t.Fatalf("source %q after moving the slot to another engineer, want SWAP", source)
	}

	note := "done with it"
	if err := repo.DeleteAssignment(ctx, made.ID, schedLeadEmail, &note); err != nil {
		t.Fatalf("DeleteAssignment: %v", err)
	}
	if n := countRows(t, pool,
		`SELECT count(*) FROM team_schedule_assignment WHERE id = $1::uuid`, made.ID); n != 0 {
		t.Fatalf("%d rows left after the delete, want 0", n)
	}

	// Recorded even though both engineers read as "Chamara Perera": the
	// change is detected on who they are, not on what they are called.
	var field, oldV, newV string
	if err := pool.QueryRow(ctx, `
		SELECT field_name, old_value, new_value FROM team_schedule_assignment_activity
		 WHERE assignment_id = $1::uuid AND action = 'UPDATED' AND field_name = 'user'`,
		made.ID).Scan(&field, &oldV, &newV); err != nil {
		t.Fatalf("no UPDATED row for the engineer change: %v", err)
	}
	if oldV != newV {
		t.Fatalf("recorded %q -> %q; this fixture's two engineers share a name, so both sides should read alike", oldV, newV)
	}

	// The whole point of the activity table: the row is gone and its history
	// is not.
	for _, action := range []string{"CREATED", "UPDATED", "DELETED"} {
		if n := countRows(t, pool,
			`SELECT count(*) FROM team_schedule_assignment_activity WHERE assignment_id = $1::uuid AND action = $2`,
			made.ID, action); n == 0 {
			t.Fatalf("no %s activity row survived for the deleted assignment", action)
		}
	}

	acts, err := repo.ActivityForTeam(ctx, schedTeamKey, schedMonday, schedMonday)
	if err != nil {
		t.Fatalf("ActivityForTeam: %v", err)
	}
	if len(acts) < 3 {
		t.Fatalf("got %d activity rows for the team, want at least the 3 changes made", len(acts))
	}
	for _, a := range acts {
		if a.ActorEmail != schedLeadEmail {
			t.Fatalf("activity attributed to %q, want the lead who made the change", a.ActorEmail)
		}
	}
}

// An engineer can be on two teams. A lead of one may clear their day on that
// team; the row the other team put them on is not theirs to touch.
func TestScheduleIntegration_ApplyRangeOnlyClearsTheCallersOwnTeam(t *testing.T) {
	repo, pool := newScheduleIntegrationRepo(t)
	ctx := context.Background()
	// Two windows that do not overlap: one person cannot hold the same shift
	// twice on a day, nor two windows sharing hours, so the two teams have to
	// have put them on genuinely different parts of the day.
	morning, evening := "CRE_MORNING", "CRE_EVENING"

	// A second team, and the same engineer rostered on it the same day.
	otherTeamID := "5c8e0000-0000-4000-8000-000000000005"
	mustExec(t, pool, `
		INSERT INTO team (id, created_on, updated_on, created_by, updated_by, name, key, type)
		VALUES ($1, NOW(), NOW(), 'fixture', 'fixture', $2, $2, 'cre-abt')
		ON CONFLICT (id) DO NOTHING`, otherTeamID, schedOtherTeam)
	t.Cleanup(func() {
		mustExec(t, pool, `DELETE FROM team_schedule_assignment WHERE team_key = $1`, schedOtherTeam)
	})

	for _, seed := range []struct{ team, code string }{
		{schedTeamKey, morning},
		{schedOtherTeam, evening},
	} {
		if _, err := repo.ApplyRange(ctx, domain.ApplyScheduleRangeRequest{
			UserID: schedMemberID, TeamKey: seed.team, ShiftCode: seed.code,
			From: schedMonday, To: schedMonday,
		}, schedLeadEmail); err != nil {
			t.Fatalf("seed %s: %v", seed.team, err)
		}
	}
	if n := countRows(t, pool,
		`SELECT count(*) FROM team_schedule_assignment WHERE user_id = $1::uuid AND rota_date = $2::date`,
		schedMemberID, schedMonday); n != 2 {
		t.Fatalf("%d rows on the day, want one per team", n)
	}

	// Clearing on one team leaves the other standing.
	if _, err := repo.ApplyRange(ctx, domain.ApplyScheduleRangeRequest{
		UserID: schedMemberID, TeamKey: schedTeamKey, ShiftCode: "",
		From: schedMonday, To: schedMonday,
	}, schedLeadEmail); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if n := countRows(t, pool,
		`SELECT count(*) FROM team_schedule_assignment WHERE user_id = $1::uuid AND team_key = $2`,
		schedMemberID, schedTeamKey); n != 0 {
		t.Fatalf("%d rows left on the caller's own team, want 0", n)
	}
	if n := countRows(t, pool,
		`SELECT count(*) FROM team_schedule_assignment WHERE user_id = $1::uuid AND team_key = $2`,
		schedMemberID, schedOtherTeam); n != 1 {
		t.Fatalf("%d rows left on the other team, want the 1 it put there", n)
	}
}
