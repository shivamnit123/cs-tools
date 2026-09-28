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

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wso2-open-operations/cs-tools/entity-service/internal/apierror"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/domain"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/repository"
)

// fakePolicyLookupRepo answers FindPolicyByName from a fixed in-memory
// set of "name|target" -> policy, exactly the real production names given
// in this engine's own delivering task brief (e.g.
// "P1 - Response (Managed Services)"), plus the migration-000089 P0 rows
// (Managed Services only, matching real ServiceNow data's own P0 gap).
type fakePolicyLookupRepo struct {
	policies map[string]repository.SLAPolicyRef
	calls    []string
	// errOnName, if set, makes FindPolicyByName fail with a real (non-
	// NotFoundError) error for that exact name -- used to simulate a
	// transient lookup failure (as opposed to a genuinely absent policy)
	// for one specific clock type, leaving every other name unaffected.
	errOnName string
}

func (f *fakePolicyLookupRepo) FindPolicyByName(_ context.Context, name, target string) (repository.SLAPolicyRef, error) {
	f.calls = append(f.calls, name+"|"+target)
	if f.errOnName != "" && name == f.errOnName {
		return repository.SLAPolicyRef{}, errors.New("db unavailable")
	}
	ref, ok := f.policies[name+"|"+target]
	if !ok {
		return repository.SLAPolicyRef{}, &apierror.NotFoundError{Msg: "no sla_policy found named " + name}
	}
	return ref, nil
}

func (f *fakePolicyLookupRepo) RegisterClock(context.Context, string, repository.SLAPolicyRef) (bool, error) {
	panic("not implemented")
}
func (f *fakePolicyLookupRepo) CompleteClock(context.Context, string, string) (bool, error) {
	panic("not implemented")
}
func (f *fakePolicyLookupRepo) SetPaused(context.Context, string, string, bool) (bool, error) {
	panic("not implemented")
}
func (f *fakePolicyLookupRepo) RecomputeActive(context.Context) (int, error) {
	panic("not implemented")
}
func (f *fakePolicyLookupRepo) ReviseClocks(context.Context, string, []repository.SLAPolicyRef) (int, error) {
	panic("not implemented")
}

func newFakePolicyLookupRepo() *fakePolicyLookupRepo {
	return &fakePolicyLookupRepo{
		policies: map[string]repository.SLAPolicyRef{
			"P1 - Response (Managed Services)|RESPONSE":     {ID: "p1-r-ms", Target: "RESPONSE", Duration: time.Hour},
			"P2 - Workaround (Open Source)|WORKAROUND":      {ID: "p2-w-os", Target: "WORKAROUND", Duration: 48 * time.Hour},
			"P3 - Resolution (Managed Services)|RESOLUTION": {ID: "p3-res-ms", Target: "RESOLUTION", Duration: 72 * time.Hour},
			"Query - Response (Open Source)|RESPONSE":       {ID: "q-r-os", Target: "RESPONSE", Duration: 24 * time.Hour},
			"P0 - Response (Managed Services)|RESPONSE":     {ID: "p0-r-ms", Target: "RESPONSE", Duration: 15 * time.Minute},
			"P0 - Workaround (Managed Services)|WORKAROUND": {ID: "p0-w-ms", Target: "WORKAROUND", Duration: 4 * time.Hour},
			"P0 - Resolution (Managed Services)|RESOLUTION": {ID: "p0-res-ms", Target: "RESOLUTION", Duration: 48 * time.Hour},
		},
	}
}

func TestSLAPolicyResolver_Resolve_MatchesRealPolicyNames(t *testing.T) {
	tests := []struct {
		name       string
		severity   domain.CaseSeverity
		clockType  string
		plan       string
		wantID     string
		wantTarget string
	}{
		{"P1 response, exact plan", domain.CaseSeverityCritical, slaClockTypeResponse, slaPlanManagedServices, "p1-r-ms", "RESPONSE"},
		{"P2 workaround, exact plan", domain.CaseSeverityHigh, slaClockTypeWorkaround, slaPlanOpenSource, "p2-w-os", "WORKAROUND"},
		{"P3 resolution, exact plan", domain.CaseSeverityMedium, slaClockTypeResolution, slaPlanManagedServices, "p3-res-ms", "RESOLUTION"},
		{"Query/LOW response, exact plan", domain.CaseSeverityLow, slaClockTypeResponse, slaPlanOpenSource, "q-r-os", "RESPONSE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakePolicyLookupRepo()
			r := newSLAPolicyResolver(repo)
			got, ok, err := r.resolve(context.Background(), tt.severity, tt.clockType, tt.plan)
			if err != nil {
				t.Fatalf("resolve() error = %v, want nil", err)
			}
			if !ok {
				t.Fatalf("resolve() ok = false, want true")
			}
			if got.ID != tt.wantID || got.Target != tt.wantTarget {
				t.Errorf("resolve() = %+v, want ID=%s Target=%s", got, tt.wantID, tt.wantTarget)
			}
		})
	}
}

// TestSLAPolicyResolver_Resolve_P0FallsBackAcrossPlan is the whole reason
// resolve() tries both plan labels: migration 0136 seeds P0 policies
// under "Managed Services" only (matching real ServiceNow data, which has
// no Open Source P0 rows at all), so a CATASTROPHIC-severity case whose
// derived plan guessed "Open Source" must still resolve via the fallback.
func TestSLAPolicyResolver_Resolve_P0FallsBackAcrossPlan(t *testing.T) {
	repo := newFakePolicyLookupRepo()
	r := newSLAPolicyResolver(repo)

	got, ok, err := r.resolve(context.Background(), domain.CaseSeverityCatastrophic, slaClockTypeWorkaround, slaPlanOpenSource)
	if err != nil {
		t.Fatalf("resolve() error = %v, want nil", err)
	}
	if !ok {
		t.Fatalf("resolve() ok = false, want true (should have fallen back to Managed Services)")
	}
	if got.ID != "p0-w-ms" {
		t.Errorf("resolve() = %+v, want ID=p0-w-ms", got)
	}
	// Both plan names must have been tried, derived plan first.
	wantCalls := []string{
		"P0 - Workaround (Open Source)|WORKAROUND",
		"P0 - Workaround (Managed Services)|WORKAROUND",
	}
	if len(repo.calls) != len(wantCalls) || repo.calls[0] != wantCalls[0] || repo.calls[1] != wantCalls[1] {
		t.Errorf("FindPolicyByName calls = %v, want %v", repo.calls, wantCalls)
	}
}

func TestSLAPolicyResolver_Resolve_NoPolicyEitherPlan(t *testing.T) {
	repo := newFakePolicyLookupRepo()
	r := newSLAPolicyResolver(repo)

	_, ok, err := r.resolve(context.Background(), domain.CaseSeverityLow, slaClockTypeWorkaround, slaPlanOpenSource)
	if err != nil {
		t.Fatalf("resolve() error = %v, want nil -- a genuinely absent policy is not a lookup failure", err)
	}
	if ok {
		t.Fatalf("resolve() ok = true, want false: no Query workaround policy is seeded/faked at all")
	}
}

func TestSLAPolicyResolver_Resolve_UnknownClockType(t *testing.T) {
	repo := newFakePolicyLookupRepo()
	r := newSLAPolicyResolver(repo)

	_, ok, err := r.resolve(context.Background(), domain.CaseSeverityCritical, "bogus", slaPlanOpenSource)
	if err != nil {
		t.Fatalf("resolve() error = %v, want nil", err)
	}
	if ok {
		t.Fatalf("resolve() ok = true, want false for an unrecognized clock type")
	}
}

// fakeRepoLookupErr forces FindPolicyByName to fail with a non-NotFound
// error, so resolve() must stop trying further plans and propagate the
// failure as "no policy" rather than silently falling back.
type erroringPolicyLookupRepo struct{ fakePolicyLookupRepo }

func (f *erroringPolicyLookupRepo) FindPolicyByName(context.Context, string, string) (repository.SLAPolicyRef, error) {
	return repository.SLAPolicyRef{}, errors.New("db unavailable")
}

// TestSLAPolicyResolver_Resolve_InfrastructureErrorDoesNotFallBack confirms
// resolve() both refuses to fall back to the other plan on a real lookup
// failure (a genuinely-absent policy and a failed lookup must not be
// treated the same -- see resolve's own doc comment) AND propagates the
// error rather than swallowing it, so callers like resolveApplicablePolicies
// can tell "not configured" apart from "couldn't check right now."
func TestSLAPolicyResolver_Resolve_InfrastructureErrorDoesNotFallBack(t *testing.T) {
	repo := &erroringPolicyLookupRepo{}
	r := newSLAPolicyResolver(repo)

	_, ok, err := r.resolve(context.Background(), domain.CaseSeverityCritical, slaClockTypeResponse, slaPlanOpenSource)
	if err == nil {
		t.Fatalf("resolve() error = nil, want the repository's error propagated")
	}
	if ok {
		t.Fatalf("resolve() ok = true, want false when the repository call itself fails")
	}
}

// fakeProjectSvc is a minimal ProjectService fake for resolveCasePlan's
// own tests -- see this package's own doc comments (fakeProjectService in
// sn_deployed_product_search_by_version_test.go) for why this file defines
// its own rather than reusing that one (it panics on GetProjectByID).
type fakeProjectSvc struct {
	project domain.ProjectDetailsView
	err     error
}

func (f fakeProjectSvc) SearchProjects(context.Context, domain.SearchProjectsRequest) (domain.SearchProjectsResponse, error) {
	panic("not implemented")
}

func (f fakeProjectSvc) GetProjectByID(context.Context, string) (domain.ProjectDetailsView, error) {
	return f.project, f.err
}

func TestResolveCasePlan(t *testing.T) {
	tests := []struct {
		name       string
		projectSvc ProjectService
		projectID  string
		want       string
	}{
		{"nil project service defaults to open source", nil, "proj-1", slaPlanOpenSource},
		{"empty project id defaults to open source", fakeProjectSvc{}, "", slaPlanOpenSource},
		{
			"managed cloud subscription maps to managed services",
			fakeProjectSvc{project: domain.ProjectDetailsView{SubscriptionType: domain.SubscriptionTypeManagedCloudSubscription}},
			"proj-1", slaPlanManagedServices,
		},
		{
			"development support defaults to open source",
			fakeProjectSvc{project: domain.ProjectDetailsView{SubscriptionType: domain.SubscriptionTypeDevelopmentSupport}},
			"proj-1", slaPlanOpenSource,
		},
		{
			"lookup failure defaults to open source",
			fakeProjectSvc{err: errors.New("sn unavailable")},
			"proj-1", slaPlanOpenSource,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveCasePlan(context.Background(), tt.projectSvc, tt.projectID)
			if got != tt.want {
				t.Errorf("resolveCasePlan() = %q, want %q", got, tt.want)
			}
		})
	}
}
