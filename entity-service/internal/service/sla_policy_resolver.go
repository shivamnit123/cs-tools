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
	"log/slog"

	"github.com/wso2-open-operations/cs-tools/entity-service/internal/apierror"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/domain"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/repository"
)

// SLA clock type names -- kept as the same three string values the
// now-deleted sla_clocks design used (internal/service/sla_policy.go
// before commit 116d43522), and matching sla_policy_target_enum's real
// labels (RESPONSE/WORKAROUND/RESOLUTION) once upper-cased.
const (
	slaClockTypeResponse   = "response"
	slaClockTypeWorkaround = "workaround"
	slaClockTypeResolution = "resolution"
)

// slaClockTypeTarget maps this package's own clock-type strings to
// sla_policy.target's real enum labels.
var slaClockTypeTarget = map[string]string{
	slaClockTypeResponse:   "RESPONSE",
	slaClockTypeWorkaround: "WORKAROUND",
	slaClockTypeResolution: "RESOLUTION",
}

// slaClockTypeNameLabel is the title-case word sla_policy.name uses for
// each target, e.g. "P1 - Response (Managed Services)" -- confirmed
// against real production sla_policy names (see this package's own
// CLAUDE.md task notes / the migration 0136 doc comment for the full
// set of examples this was checked against).
var slaClockTypeNameLabel = map[string]string{
	slaClockTypeResponse:   "Response",
	slaClockTypeWorkaround: "Workaround",
	slaClockTypeResolution: "Resolution",
}

// slaSeverityPolicyPrefix maps domain.CaseSeverity to the severity prefix
// ServiceNow's real sla_policy.name values use -- CATASTROPHIC/CRITICAL/
// HIGH/MEDIUM map 1:1 to P0-P3, LOW maps to "Query" (ServiceNow's own name
// for its lowest, "best efforts" tier). This mirrors the resolved mapping
// from the product owner (S0=P0, S1=P1, S2=P2, S3=P3, S4=Query) composed
// with case_repo.go's own caseSeverityToEnum (domain.CaseSeverity ->
// S0-S4), so it does not need to duplicate that first half itself.
var slaSeverityPolicyPrefix = map[domain.CaseSeverity]string{
	domain.CaseSeverityCatastrophic: "P0",
	domain.CaseSeverityCritical:     "P1",
	domain.CaseSeverityHigh:         "P2",
	domain.CaseSeverityMedium:       "P3",
	domain.CaseSeverityLow:          "Query",
}

// slaApplicableClockTypes lists, per severity, which clock types this
// engine ever registers -- LOW/Query gets "response" only, matching the
// old, now-deleted sla_clocks design's LOW-severity entry (WSO2's support
// policy defines no fixed Workaround/Resolution SLA at that tier, "best
// efforts"). This is a hardcoded scope decision per the SLA engine task
// brief, independent of whether ServiceNow's real sla_policy data happens
// to also define a "Query - Resolution" row (it does) -- deliberately not
// consulted, to match the old LOW-severity behavior exactly rather than
// registering a clock type WSO2's own published policy doesn't promise.
var slaApplicableClockTypes = map[domain.CaseSeverity][]string{
	domain.CaseSeverityCatastrophic: {slaClockTypeResponse, slaClockTypeWorkaround, slaClockTypeResolution},
	domain.CaseSeverityCritical:     {slaClockTypeResponse, slaClockTypeWorkaround, slaClockTypeResolution},
	domain.CaseSeverityHigh:         {slaClockTypeResponse, slaClockTypeWorkaround, slaClockTypeResolution},
	domain.CaseSeverityMedium:       {slaClockTypeResponse, slaClockTypeWorkaround, slaClockTypeResolution},
	domain.CaseSeverityLow:          {slaClockTypeResponse},
}

// slaPlanManagedServices/slaPlanOpenSource are the two "plan" labels
// sla_policy.name's P0-P3 rows carry in parens (e.g.
// "P1 - Response (Managed Services)"). See resolveCasePlan's own doc
// comment for how (and how unreliably) a case's plan is determined --
// this is the single biggest judgment call in this engine; flagged
// prominently in the delivering task's own final report.
const (
	slaPlanManagedServices = "Managed Services"
	slaPlanOpenSource      = "Open Source"
)

// slaPolicyResolver resolves the real sla_policy row (duration, id) backing
// a given severity/clock-type/plan combination, replacing the old, deleted
// sla_clocks design's hardcoded slaDurations map (internal/service/
// sla_policy.go before commit 116d43522) with a lookup against the real
// ServiceNow-synced policy data (migration 0047/0048) that map never
// read at all.
type slaPolicyResolver struct {
	repo repository.SLAEngineRepository
}

func newSLAPolicyResolver(repo repository.SLAEngineRepository) *slaPolicyResolver {
	return &slaPolicyResolver{repo: repo}
}

// resolve returns the sla_policy row for severity/clockType, trying the
// derived plan (see resolveCasePlan) first and falling back to the other
// plan label if that exact name doesn't exist. The fallback exists for two
// reasons: (1) resolveCasePlan's derivation is a best-effort heuristic with
// no reliable underlying signal (see its own doc comment) -- a wrong guess
// must not silently drop SLA tracking for a case entirely; (2) P0 policies
// (migration 0136) are seeded ONLY under "Managed Services" (ServiceNow's
// own real data has no Open Source P0 rows either -- P0 is WSO2's most
// severe, paid-support-only tier), so a CATASTROPHIC-severity case whose
// project looks like Open Source must still resolve to the real P0 policy
// rather than getting no clock at all.
//
// ok=false with a nil error means no policy exists under EITHER plan label
// for this severity/clockType combination -- logged as a warning by the
// caller, exactly like the old slaDurations map's "severity not in map"
// case, never a fabricated fallback duration. A non-nil error means the
// lookup itself failed (e.g. a database blip), NOT that the policy is
// absent -- callers must treat these two cases differently: RegisterCaseClocks
// safely skips either one (nothing existing is at risk), but ReviseCaseClocks
// must NOT proceed to cancel a case's existing clocks on the strength of an
// incomplete policy list caused by a transient lookup failure (see
// resolveApplicablePolicies' own doc comment).
func (r *slaPolicyResolver) resolve(ctx context.Context, severity domain.CaseSeverity, clockType, derivedPlan string) (repository.SLAPolicyRef, bool, error) {
	prefix, ok := slaSeverityPolicyPrefix[severity]
	if !ok {
		slog.WarnContext(ctx, "sla engine: no policy name prefix for severity", "severity", severity)
		return repository.SLAPolicyRef{}, false, nil
	}
	target, ok := slaClockTypeTarget[clockType]
	if !ok {
		slog.WarnContext(ctx, "sla engine: unknown clock type", "clockType", clockType)
		return repository.SLAPolicyRef{}, false, nil
	}
	label := slaClockTypeNameLabel[clockType]

	altPlan := slaPlanOpenSource
	if derivedPlan == slaPlanOpenSource {
		altPlan = slaPlanManagedServices
	}

	for _, plan := range []string{derivedPlan, altPlan} {
		name := prefix + " - " + label + " (" + plan + ")"
		ref, err := r.repo.FindPolicyByName(ctx, name, target)
		if err == nil {
			return ref, true, nil
		}
		var notFound *apierror.NotFoundError
		if !errors.As(err, &notFound) {
			slog.ErrorContext(ctx, "sla engine: policy lookup failed", "name", name, "err", err)
			return repository.SLAPolicyRef{}, false, err
		}
	}

	// Third tier: a looser pattern match, tried only after both exact plan
	// names above found nothing. Confirmed on wso2sndev.service-now.com's
	// synced data (not prod, which the exact-name lookup above was verified
	// against) that a real environment's policy catalog doesn't always
	// follow the "<prefix> - <label> (<plan>)" convention -- e.g.
	// "P2 - IR - Resolution (Open Source)" instead of
	// "P2 - Resolution (Open Source)". Without this, such an environment
	// gets NO clock for that severity/clockType at all despite a policy
	// for it clearly existing, purely because of a naming variant this
	// engine's exact lookup can't see past. See
	// SLAEngineRepository.FindPolicyByPattern's own doc comment for the
	// matching rule and why it's safe to also leave enabled on prod (it
	// only ever fires once both exact-name attempts above have failed).
	ref, err := r.repo.FindPolicyByPattern(ctx, prefix, label, target, derivedPlan)
	if err == nil {
		slog.WarnContext(ctx, "sla engine: resolved policy by loose pattern match, not exact name -- the environment's sla_policy naming may not follow the P{n} - {type} (plan) convention",
			"severity", severity, "clockType", clockType, "triedPlans", []string{derivedPlan, altPlan}, "matchedName", ref.Name)
		return ref, true, nil
	}
	var notFound *apierror.NotFoundError
	if !errors.As(err, &notFound) {
		slog.ErrorContext(ctx, "sla engine: pattern policy lookup failed", "prefix", prefix, "label", label, "err", err)
		return repository.SLAPolicyRef{}, false, err
	}

	slog.WarnContext(ctx, "sla engine: no sla_policy found for severity/clockType under either plan or the loose pattern fallback",
		"severity", severity, "clockType", clockType, "triedPlans", []string{derivedPlan, altPlan})
	return repository.SLAPolicyRef{}, false, nil
}

// resolveCasePlan is this engine's single biggest judgment call: nothing in
// this codebase's domain model records, for a given case, which support
// plan its account/project is actually on -- account has no tier-like
// column at all (see internal/repository/case_repo.go's own comment on
// AccountRef.Type, always ""), and account.classification is a differently
// -shaped, freeform Salesforce field (seen with values like "enterprise" in
// this codebase's own test fixtures) with no confirmed relationship to
// "Managed Services vs Open Source" at all -- using it would fabricate
// false precision from a field of unknown semantics, worse than an honest,
// flagged default.
//
// The one real, populated signal that exists anywhere near a case is
// domain.ProjectDetailsView.SubscriptionType (entity.go) -- populated on
// both data sources, with SubscriptionTypeManagedCloudSubscription being
// the one value that unambiguously means "on WSO2's managed cloud", so
// that value maps to "Managed Services" and every other subscription type
// (development_support, evaluation_subscription, cloud_support,
// professional_services, ...) maps to "Open Source" as a default -- NOT
// because those are all genuinely Open Source, but because there is no
// better signal to distinguish them and "Open Source" is the more common,
// unpaid-tier default. This is a real, if weak, per-case signal (unlike
// account.classification), but still a guess for any subscription type
// other than managed_cloud_subscription -- see resolve's own doc comment
// for how the engine tolerates this guess being wrong (a two-plan
// fallback), and this engine's delivering task's final report for the
// explicit flag that a real "which support plan is this account on" field
// is what would actually fix this properly.
//
// projectSvc/projectID may be nil/empty (e.g. a case with no project
// linked, a valid state -- see domain.CaseView.ProjectDetails' own doc
// comment); a lookup failure is logged and treated the same as "unknown",
// never propagated as an error -- this must never block case creation.
func resolveCasePlan(ctx context.Context, projectSvc ProjectService, projectID string) string {
	if projectSvc == nil || projectID == "" {
		return slaPlanOpenSource
	}
	project, err := projectSvc.GetProjectByID(ctx, projectID)
	if err != nil {
		slog.InfoContext(ctx, "sla engine: could not resolve case's project subscription type, defaulting plan", "projectId", projectID)
		return slaPlanOpenSource
	}
	if project.SubscriptionType == domain.SubscriptionTypeManagedCloudSubscription {
		return slaPlanManagedServices
	}
	return slaPlanOpenSource
}
