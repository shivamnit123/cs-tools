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
	"reflect"
	"sort"
	"testing"

	"github.com/wso2-open-operations/cs-tools/entity-service/internal/apierror"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/domain"
)

// fakeGroupMemberResolver seeds a fixed set of "group" -> team_member rows in
// memory, exactly matching groupMemberResolver's contract (empty/unknown
// group id -> empty slice, not an error) without a real team_member table.
type fakeGroupMemberResolver struct {
	membersByGroup map[string][]string
}

// GroupMemberUserIDs ignores q entirely -- this fake never touches a real
// database, so it has nothing to run a query through; q is accepted (and a
// test may legitimately pass nil for it) purely to satisfy groupMemberResolver's
// real signature, which now threads CreateEscalation's own tx through
// (rowsQuerier, case_repo.go) rather than always using a separate pool
// connection -- see resolveEscalationRecipients's own doc comment for why.
func (f *fakeGroupMemberResolver) GroupMemberUserIDs(_ context.Context, _ rowsQuerier, groupID string) ([]string, error) {
	// A group id with no seeded team_member rows (unknown group, or a real
	// group with zero members) resolves to nil, not an error -- the map's
	// own zero value already gives this for free, mirroring
	// dbGroupMemberResolver's real "zero rows back, no error" behavior.
	return f.membersByGroup[groupID], nil
}

var _ groupMemberResolver = (*fakeGroupMemberResolver)(nil)

// --- nextEscalationLevel: level-transition math ---

func TestNextEscalationLevel_EscalateIncrements(t *testing.T) {
	for previous := 0; previous < maxEscalationLevel; previous++ {
		got, err := nextEscalationLevel(domain.EscalationActionEscalate, previous)
		if err != nil {
			t.Fatalf("previous=%d: unexpected error: %v", previous, err)
		}
		if want := previous + 1; got != want {
			t.Errorf("previous=%d: got %d, want %d", previous, got, want)
		}
	}
}

func TestNextEscalationLevel_EscalateCapsAtEL5(t *testing.T) {
	got, err := nextEscalationLevel(domain.EscalationActionEscalate, maxEscalationLevel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != maxEscalationLevel {
		t.Errorf("got %d, want %d (capped, not an error)", got, maxEscalationLevel)
	}
}

func TestNextEscalationLevel_DeescalateDecrements(t *testing.T) {
	for previous := 1; previous <= maxEscalationLevel; previous++ {
		got, err := nextEscalationLevel(domain.EscalationActionDeescalate, previous)
		if err != nil {
			t.Fatalf("previous=%d: unexpected error: %v", previous, err)
		}
		if want := previous - 1; got != want {
			t.Errorf("previous=%d: got %d, want %d", previous, got, want)
		}
	}
}

func TestNextEscalationLevel_DeescalateAtEL0IsValidationError(t *testing.T) {
	_, err := nextEscalationLevel(domain.EscalationActionDeescalate, 0)
	var valErr *apierror.ValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("got error %v (%T), want *apierror.ValidationError", err, err)
	}
}

func TestEscalationLevelInt(t *testing.T) {
	el := func(s string) *string { return &s }
	cases := []struct {
		name string
		raw  *string
		want int
	}{
		{"nil (never escalated) is EL0", nil, 0},
		{"EL0", el("EL0"), 0},
		{"EL3", el("EL3"), 3},
		{"EL5", el("EL5"), 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := escalationLevelInt(tc.raw); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// --- resolveEscalationRecipients: cumulative EL1..EL5 recipient rule ---

func sortedIDs(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

func TestResolveEscalationRecipients_CumulativeAcrossLevels(t *testing.T) {
	ctx := context.Background()
	// Seeded "group" + team_member fixture: three groups, each with real
	// (possibly multiple) members -- resolveEscalationRecipients must widen
	// to every member of a configured group, not just one address.
	groups := &fakeGroupMemberResolver{membersByGroup: map[string][]string{
		"group-el1-tl":  {"user-el1-tl-a", "user-el1-tl-b"},
		"group-el2-tu":  {"user-el2-tu"},
		"group-el3-cre": {"user-el3-cre"},
	}}
	notifyCfg := EscalationNotificationConfig{
		EL1AmericasTLGroupID: "group-el1-tl",
		EL2AmericasTUGroupID: "group-el2-tu",
		EL3CREHeadGroupID:    "group-el3-cre",
	}
	r := &escalationRepo{groups: groups, notifyCfg: notifyCfg}

	cc := escalationCaseContext{
		technicalOwnerID: strPtr("user-tech-owner"),
		csmID:            strPtr("user-csm"),
		creTeamManagerID: strPtr("user-cre-manager"),
	}

	// Level 1: only the EL1 sources, but the whole group membership (both
	// group-el1-tl members), not just one of them.
	got1, err := r.resolveEscalationRecipients(ctx, nil, 1, cc)
	if err != nil {
		t.Fatalf("level 1: unexpected error: %v", err)
	}
	want1 := []string{"user-el1-tl-a", "user-el1-tl-b", "user-tech-owner", "user-cre-manager"}
	if !reflect.DeepEqual(sortedIDs(got1), sortedIDs(want1)) {
		t.Errorf("level 1: got %v, want %v", sortedIDs(got1), sortedIDs(want1))
	}

	// Level 3: EL1 recipients are STILL present (cumulative), plus EL2/EL3
	// sources join in. This is the key assertion: escalating straight to
	// EL3 must not drop the EL1 recipients in favor of only EL3's own.
	got3, err := r.resolveEscalationRecipients(ctx, nil, 3, cc)
	if err != nil {
		t.Fatalf("level 3: unexpected error: %v", err)
	}
	want3 := []string{
		"user-el1-tl-a", "user-el1-tl-b", "user-tech-owner", "user-cre-manager", // EL1
		"user-el2-tu",              // EL2 (no product configured, so no product-routed recipient)
		"user-el3-cre", "user-csm", // EL3
	}
	if !reflect.DeepEqual(sortedIDs(got3), sortedIDs(want3)) {
		t.Errorf("level 3: got %v, want %v", sortedIDs(got3), sortedIDs(want3))
	}
}

// TestResolveEscalationRecipients_LevelZeroReturnsEmpty guards the fact that
// none of the "if newLevel >= N" branches fire at level 0 (a DEESCALATE that
// brought the case back down to EL0): the notification list must come back
// empty, matching is_escalated turning FALSE at that level. Configures a
// real EL1 group AND a case-derived id (technicalOwnerID) that WOULD be
// picked up at level >= 1, specifically so a future edit that accidentally
// adds an unconditional recipient outside any level guard fails this test
// instead of shipping silently.
func TestResolveEscalationRecipients_LevelZeroReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	groups := &fakeGroupMemberResolver{membersByGroup: map[string][]string{
		"group-el1-tl": {"user-a"},
	}}
	r := &escalationRepo{
		groups:    groups,
		notifyCfg: EscalationNotificationConfig{EL1AmericasTLGroupID: "group-el1-tl"},
	}
	cc := escalationCaseContext{technicalOwnerID: strPtr("user-owner")}

	got, err := r.resolveEscalationRecipients(ctx, nil, 0, cc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("level 0 must produce no recipients, got %v", got)
	}
}

func TestResolveEscalationRecipients_ProductRouting(t *testing.T) {
	ctx := context.Background()
	groups := &fakeGroupMemberResolver{membersByGroup: map[string][]string{
		"group-service": {"user-service"},
		"group-iam":     {"user-iam"},
		"group-default": {"user-default"},
	}}
	notifyCfg := EscalationNotificationConfig{
		EL2ServiceProductGroupID: "group-service",
		EL2IdentityServerGroupID: "group-iam",
		EL2DefaultProductGroupID: "group-default",
	}
	r := &escalationRepo{groups: groups, notifyCfg: notifyCfg}

	cases := []struct {
		name    string
		cc      escalationCaseContext
		wantIDs []string
	}{
		{
			name:    "no deployed product/product info at all -- no product recipient, silently",
			cc:      escalationCaseContext{},
			wantIDs: nil,
		},
		{
			name:    "category SERVICE routes to the service product group",
			cc:      escalationCaseContext{productCategory: strPtr("SERVICE")},
			wantIDs: []string{"user-service"},
		},
		{
			name:    "category SOFTWARE + business_unit IAM routes to the identity server group",
			cc:      escalationCaseContext{productCategory: strPtr("SOFTWARE"), productBusinessUnit: strPtr("IAM")},
			wantIDs: []string{"user-iam"},
		},
		{
			name:    "category SOFTWARE + a non-IAM business_unit falls to the default product group",
			cc:      escalationCaseContext{productCategory: strPtr("SOFTWARE"), productBusinessUnit: strPtr("INTEGRATION_SOFTWARE")},
			wantIDs: []string{"user-default"},
		},
		{
			name:    "category SOFTWARE with no business_unit at all also falls to the default",
			cc:      escalationCaseContext{productCategory: strPtr("SOFTWARE")},
			wantIDs: []string{"user-default"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.resolveEscalationRecipients(ctx, nil, 2, tc.cc)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(sortedIDs(got), sortedIDs(tc.wantIDs)) {
				t.Errorf("got %v, want %v", sortedIDs(got), sortedIDs(tc.wantIDs))
			}
		})
	}
}

func TestResolveEscalationRecipients_UnconfiguredEnvVarsDoNotError(t *testing.T) {
	ctx := context.Background()
	// Zero-value EscalationNotificationConfig: every group id slot unset.
	r := &escalationRepo{groups: &fakeGroupMemberResolver{membersByGroup: map[string][]string{}}, notifyCfg: EscalationNotificationConfig{}}

	// Level 5 exercises every tier's group id slot at once.
	got, err := r.resolveEscalationRecipients(ctx, nil, 5, escalationCaseContext{})
	if err != nil {
		t.Fatalf("unexpected error with every notifyCfg slot unset: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want no recipients (nothing configured, no case-derived ids)", got)
	}
}

func TestResolveEscalationRecipients_UnknownOrEmptyGroupIsSkippedNotFatal(t *testing.T) {
	ctx := context.Background()
	// notifyCfg names a group id that has no seeded team_member rows at all
	// -- a group that doesn't exist, or currently has zero members. Either
	// way: zero recipients from that slot, not an error.
	r := &escalationRepo{
		groups:    &fakeGroupMemberResolver{membersByGroup: map[string][]string{}},
		notifyCfg: EscalationNotificationConfig{EL5CEOGroupID: "group-ceo-empty"},
	}

	got, err := r.resolveEscalationRecipients(ctx, nil, 5, escalationCaseContext{})
	if err != nil {
		t.Fatalf("an unknown/empty group must be skipped, not fatal: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want no recipients (the one configured group has no members)", got)
	}
}

// TestResolveEscalationRecipients_AccountManagerIDNeverRead is a structural
// regression guard for the confirmed, deliberate gap documented on
// EscalationRepository.CreateEscalation: account.account_manager_id (SN's
// u_owner) must never be treated as a real recipient signal, since nothing
// in this repo's write path ever populates it. escalationCaseContext simply
// has no field to carry it -- if a future change adds one and wires it into
// resolveEscalationRecipients, this test's field-count/name assertion catches
// the regression before it ships.
func TestResolveEscalationRecipients_AccountManagerIDNeverRead(t *testing.T) {
	typ := reflect.TypeOf(escalationCaseContext{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if name == "accountManagerID" {
			t.Fatalf("escalationCaseContext must not carry account_manager_id -- it's a confirmed, always-NULL-in-practice gap (see CreateEscalation's own doc comment), not a real recipient signal")
		}
	}
}
