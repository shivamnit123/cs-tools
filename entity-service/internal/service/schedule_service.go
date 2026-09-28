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
	"fmt"
	"regexp"
	"time"

	"github.com/wso2-open-operations/cs-tools/entity-service/internal/apierror"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/auth"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/domain"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/repository"
)

// maxScheduleWindowDays bounds a single rota read. The month roster is the
// widest view the UI has, so a little over two months leaves room for a month
// either side of a boundary without letting a client ask for a decade.
const maxScheduleWindowDays = 70

// ScheduleService serves the Team Schedule. It validates the window, applies
// the reading rules the schema deliberately does not encode, and leaves the
// plain data operations to the repository.
type ScheduleService interface {
	Catalogue(ctx context.Context) (domain.ScheduleCatalogue, error)
	SearchAssignments(ctx context.Context, req domain.SearchScheduleAssignmentsRequest) (domain.ScheduleAssignmentsResponse, error)
	SearchAbsences(ctx context.Context, req domain.SearchScheduleAbsencesRequest) (domain.ScheduleAbsencesResponse, error)
	OnDuty(ctx context.Context, at *time.Time) (domain.ScheduleAssignmentsResponse, error)

	// The lead edit path. Each checks that the caller leads the team the slot
	// belongs to before it touches anything.
	CreateAssignment(ctx context.Context, req domain.CreateScheduleAssignmentRequest) (domain.ScheduleAssignment, error)
	UpdateAssignment(ctx context.Context, id string, req domain.UpdateScheduleAssignmentRequest) (domain.ScheduleAssignment, error)
	DeleteAssignment(ctx context.Context, id string, note *string) error
	TeamActivity(ctx context.Context, teamKey, from, to string) ([]domain.ScheduleAssignmentActivity, error)

	// MyLeadTeams is which teams this caller may edit.
	MyLeadTeams(ctx context.Context) ([]string, error)

	// ApplyRange is how the roster's picker edits: one engineer, one window,
	// across a span of days.
	ApplyRange(ctx context.Context, req domain.ApplyScheduleRangeRequest) (domain.ApplyScheduleRangeResponse, error)
	// EditMarkers is which cells in a window somebody has changed by hand.
	EditMarkers(ctx context.Context, from, to string) (domain.ScheduleEditMarkersResponse, error)

	// ApplyAbsence is the same picker marking somebody away, or bringing them
	// back, across a span.
	ApplyAbsence(ctx context.Context, req domain.ApplyScheduleAbsenceRequest) (domain.ApplyScheduleAbsenceResponse, error)
}

type scheduleService struct {
	repo   repository.ScheduleRepository
	access AccessService
}

// NewScheduleService constructs a ScheduleService over the given repository.
func NewScheduleService(repo repository.ScheduleRepository, access AccessService) ScheduleService {
	return &scheduleService{repo: repo, access: access}
}

// requireInternalCaller rejects anyone whose AccessScope is not Unrestricted.
// The rota is staff data -- who is working, who is on leave and where -- and
// belongs to no project, so there is no narrower scope a customer could be
// given: an internal caller sees all of it and anyone else sees none. Mirrors
// sla_status_service.go's helper of the same name.
func (s *scheduleService) requireInternalCaller(ctx context.Context) error {
	scope, err := s.access.ResolveScope(ctx)
	if err != nil {
		return err
	}
	if !scope.Unrestricted {
		return &apierror.ForbiddenError{Msg: "the team schedule is only available to internal staff"}
	}
	return nil
}

func (s *scheduleService) Catalogue(ctx context.Context) (domain.ScheduleCatalogue, error) {
	if err := s.requireInternalCaller(ctx); err != nil {
		return domain.ScheduleCatalogue{}, err
	}
	return s.repo.Catalogue(ctx)
}

// uuidPattern is the canonical 8-4-4-4-12 form user ids are issued in.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validateUserID refuses a userId that is not a UUID before it reaches a
// uuid column, where Postgres would reject it as a 500 rather than the 400 a
// malformed filter deserves. Empty means "no filter" and passes.
func validateUserID(id string) error {
	if id != "" && !uuidPattern.MatchString(id) {
		return &apierror.ValidationError{Msg: fmt.Sprintf("userId %q is not a UUID", id)}
	}
	return nil
}

// parseWindow validates a from/to pair and returns it normalised. Both dates
// are required: an unbounded rota read would happily return every row in the
// table, which is nobody's intent and a slow way to find that out.
func parseWindow(from, to string) (time.Time, time.Time, error) {
	if from == "" || to == "" {
		return time.Time{}, time.Time{}, &apierror.ValidationError{Msg: "both from and to are required (YYYY-MM-DD)"}
	}
	f, err := time.Parse("2006-01-02", from)
	if err != nil {
		return time.Time{}, time.Time{}, &apierror.ValidationError{Msg: fmt.Sprintf("from %q is not a YYYY-MM-DD date", from)}
	}
	t, err := time.Parse("2006-01-02", to)
	if err != nil {
		return time.Time{}, time.Time{}, &apierror.ValidationError{Msg: fmt.Sprintf("to %q is not a YYYY-MM-DD date", to)}
	}
	if t.Before(f) {
		return time.Time{}, time.Time{}, &apierror.ValidationError{Msg: "to is before from"}
	}
	if t.Sub(f) > maxScheduleWindowDays*24*time.Hour {
		return time.Time{}, time.Time{}, &apierror.ValidationError{Msg: fmt.Sprintf("window is longer than %d days", maxScheduleWindowDays)}
	}
	return f, t, nil
}

func (s *scheduleService) SearchAssignments(ctx context.Context, req domain.SearchScheduleAssignmentsRequest) (domain.ScheduleAssignmentsResponse, error) {
	if err := s.requireInternalCaller(ctx); err != nil {
		return domain.ScheduleAssignmentsResponse{}, err
	}
	if _, _, err := parseWindow(req.From, req.To); err != nil {
		return domain.ScheduleAssignmentsResponse{}, err
	}
	if err := validateUserID(req.UserID); err != nil {
		return domain.ScheduleAssignmentsResponse{}, err
	}
	if req.Family != "" && req.Family != "CRE" && req.Family != "SRE" {
		return domain.ScheduleAssignmentsResponse{}, &apierror.ValidationError{Msg: "family must be CRE or SRE"}
	}
	rows, err := s.repo.SearchAssignments(ctx, req)
	if err != nil {
		return domain.ScheduleAssignmentsResponse{}, err
	}
	return domain.ScheduleAssignmentsResponse{Assignments: rows, Count: len(rows)}, nil
}

func (s *scheduleService) SearchAbsences(ctx context.Context, req domain.SearchScheduleAbsencesRequest) (domain.ScheduleAbsencesResponse, error) {
	if err := s.requireInternalCaller(ctx); err != nil {
		return domain.ScheduleAbsencesResponse{}, err
	}
	if _, _, err := parseWindow(req.From, req.To); err != nil {
		return domain.ScheduleAbsencesResponse{}, err
	}
	if err := validateUserID(req.UserID); err != nil {
		return domain.ScheduleAbsencesResponse{}, err
	}
	rows, err := s.repo.SearchAbsences(ctx, req)
	if err != nil {
		return domain.ScheduleAbsencesResponse{}, err
	}
	return domain.ScheduleAbsencesResponse{Absences: rows, Count: len(rows)}, nil
}

// OnDuty answers who is responsible at an instant, defaulting to now. This is
// the lookup an alert escalation needs before it decides who to ring.
func (s *scheduleService) OnDuty(ctx context.Context, at *time.Time) (domain.ScheduleAssignmentsResponse, error) {
	if err := s.requireInternalCaller(ctx); err != nil {
		return domain.ScheduleAssignmentsResponse{}, err
	}
	moment := time.Now()
	if at != nil {
		moment = *at
	}
	rows, err := s.repo.OnDutyAt(ctx, moment)
	if err != nil {
		return domain.ScheduleAssignmentsResponse{}, err
	}
	return domain.ScheduleAssignmentsResponse{Assignments: rows, Count: len(rows)}, nil
}

// ── lead edit ───────────────────────────────────────────────────────────────

// requireTeamLead is the whole of the edit permission: internal, and a lead of
// the team the slot belongs to.
//
// Own team only, by decision. A lead editing another ABT's rota is not a
// capability anyone asked for, and the blast radius of getting it wrong -- one
// team silently rewriting another's cover -- is worse than the inconvenience of
// not having it.
//
// The internal check comes first so a customer never learns whether a team key
// exists by being told they do not lead it.
func (s *scheduleService) requireTeamLead(ctx context.Context, teamKey string) error {
	if err := s.requireInternalCaller(ctx); err != nil {
		return err
	}
	id := auth.IdentityFromContext(ctx)
	if id.UserEmail == "" {
		return &apierror.ForbiddenError{Msg: "editing the rota needs a user token, not a service credential"}
	}
	ok, err := s.repo.LeadsTeam(ctx, id.UserEmail, teamKey)
	if err != nil {
		return err
	}
	if !ok {
		return &apierror.ForbiddenError{Msg: fmt.Sprintf("only a lead of %s can change its rota", teamKey)}
	}
	return nil
}

// requireTeamLeadOver is the lead check plus the engineer being changed.
//
// Leading a team says what a lead may change; it does not say whose rota they
// may change it on. Without this second check a lead could name their own team
// -- which they genuinely lead, so the first check passes -- and pass the id of
// somebody on another team entirely, writing a row against a person they have
// no say over. A team key in a request decides nothing on its own.
func (s *scheduleService) requireTeamLeadOver(ctx context.Context, teamKey, userID string) error {
	if err := s.requireTeamLead(ctx, teamKey); err != nil {
		return err
	}
	member, err := s.repo.UserInTeam(ctx, userID, teamKey)
	if err != nil {
		return err
	}
	if !member {
		return &apierror.ForbiddenError{
			Msg: fmt.Sprintf("that engineer is not on %s, so their rota is not yours to change", teamKey),
		}
	}
	return nil
}

// CreateAssignment implements ScheduleService.
func (s *scheduleService) CreateAssignment(ctx context.Context, req domain.CreateScheduleAssignmentRequest) (domain.ScheduleAssignment, error) {
	if err := validateUserID(req.UserID); err != nil {
		return domain.ScheduleAssignment{}, err
	}
	if req.TeamKey == "" || req.ShiftCode == "" || req.RotaDate == "" {
		return domain.ScheduleAssignment{}, &apierror.ValidationError{Msg: "teamKey, shiftCode and rotaDate are required"}
	}
	if _, err := time.Parse("2006-01-02", req.RotaDate); err != nil {
		return domain.ScheduleAssignment{}, &apierror.ValidationError{Msg: "rotaDate must be YYYY-MM-DD"}
	}
	if err := s.requireTeamLeadOver(ctx, req.TeamKey, req.UserID); err != nil {
		return domain.ScheduleAssignment{}, err
	}
	return s.repo.CreateAssignment(ctx, req, auth.IdentityFromContext(ctx).UserEmail)
}

// UpdateAssignment implements ScheduleService.
//
// The team is read from the row rather than taken from the caller: otherwise a
// lead could name their own team and edit anyone's slot.
func (s *scheduleService) UpdateAssignment(ctx context.Context, id string, req domain.UpdateScheduleAssignmentRequest) (domain.ScheduleAssignment, error) {
	if req.UserID != nil {
		if err := validateUserID(*req.UserID); err != nil {
			return domain.ScheduleAssignment{}, err
		}
	}
	existing, err := s.assignmentForEdit(ctx, id)
	if err != nil {
		return domain.ScheduleAssignment{}, err
	}
	return s.repo.UpdateAssignment(ctx, existing.ID, req, auth.IdentityFromContext(ctx).UserEmail)
}

// DeleteAssignment implements ScheduleService.
func (s *scheduleService) DeleteAssignment(ctx context.Context, id string, note *string) error {
	existing, err := s.assignmentForEdit(ctx, id)
	if err != nil {
		return err
	}
	return s.repo.DeleteAssignment(ctx, existing.ID, auth.IdentityFromContext(ctx).UserEmail, note)
}

// assignmentForEdit loads a slot and confirms the caller leads its team.
func (s *scheduleService) assignmentForEdit(ctx context.Context, id string) (domain.ScheduleAssignment, error) {
	if err := validateUserID(id); err != nil {
		return domain.ScheduleAssignment{}, &apierror.ValidationError{Msg: fmt.Sprintf("assignment id %q is not a UUID", id)}
	}
	// Internal first: an outside caller should not be able to probe which
	// assignment ids exist by the difference between 403 and 404.
	if err := s.requireInternalCaller(ctx); err != nil {
		return domain.ScheduleAssignment{}, err
	}
	existing, err := s.repo.AssignmentByID(ctx, id)
	if err != nil {
		return domain.ScheduleAssignment{}, err
	}
	if err := s.requireTeamLead(ctx, existing.TeamKey); err != nil {
		return domain.ScheduleAssignment{}, err
	}
	return existing, nil
}

// TeamActivity implements ScheduleService.
func (s *scheduleService) TeamActivity(ctx context.Context, teamKey, from, to string) ([]domain.ScheduleAssignmentActivity, error) {
	if _, _, err := parseWindow(from, to); err != nil {
		return nil, err
	}
	if err := s.requireTeamLead(ctx, teamKey); err != nil {
		return nil, err
	}
	return s.repo.ActivityForTeam(ctx, teamKey, from, to)
}

// MyLeadTeams implements ScheduleService.
//
// No team argument and nothing to authorize beyond being internal: the answer
// is about the caller, and an empty list is a perfectly good answer for
// somebody who leads nothing.
func (s *scheduleService) MyLeadTeams(ctx context.Context) ([]string, error) {
	if err := s.requireInternalCaller(ctx); err != nil {
		return nil, err
	}
	email := auth.IdentityFromContext(ctx).UserEmail
	if email == "" {
		return []string{}, nil
	}
	return s.repo.LeadTeamsFor(ctx, email)
}

// ApplyRange implements ScheduleService.
func (s *scheduleService) ApplyRange(ctx context.Context, req domain.ApplyScheduleRangeRequest) (domain.ApplyScheduleRangeResponse, error) {
	if err := validateUserID(req.UserID); err != nil {
		return domain.ApplyScheduleRangeResponse{}, err
	}
	// shiftCode is the one optional field: empty means take them off the rota
	// over the span rather than put them on a window.
	if req.UserID == "" || req.TeamKey == "" || req.From == "" || req.To == "" {
		return domain.ApplyScheduleRangeResponse{}, &apierror.ValidationError{
			Msg: "userId, teamKey, from and to are all required",
		}
	}
	if err := s.requireTeamLeadOver(ctx, req.TeamKey, req.UserID); err != nil {
		return domain.ApplyScheduleRangeResponse{}, err
	}
	return s.repo.ApplyRange(ctx, req, auth.IdentityFromContext(ctx).UserEmail)
}

// ApplyAbsence implements ScheduleService.
func (s *scheduleService) ApplyAbsence(ctx context.Context, req domain.ApplyScheduleAbsenceRequest) (domain.ApplyScheduleAbsenceResponse, error) {
	if err := validateUserID(req.UserID); err != nil {
		return domain.ApplyScheduleAbsenceResponse{}, err
	}
	// kindCode is the one optional field: empty means bring them back over the
	// span rather than mark them away across it.
	if req.UserID == "" || req.TeamKey == "" || req.From == "" || req.To == "" {
		return domain.ApplyScheduleAbsenceResponse{}, &apierror.ValidationError{
			Msg: "userId, teamKey, from and to are all required",
		}
	}
	if err := s.requireTeamLeadOver(ctx, req.TeamKey, req.UserID); err != nil {
		return domain.ApplyScheduleAbsenceResponse{}, err
	}
	return s.repo.ApplyAbsence(ctx, req, auth.IdentityFromContext(ctx).UserEmail)
}

// EditMarkers implements ScheduleService.
func (s *scheduleService) EditMarkers(ctx context.Context, from, to string) (domain.ScheduleEditMarkersResponse, error) {
	if err := s.requireInternalCaller(ctx); err != nil {
		return domain.ScheduleEditMarkersResponse{}, err
	}
	if _, _, err := parseWindow(from, to); err != nil {
		return domain.ScheduleEditMarkersResponse{}, err
	}
	rows, err := s.repo.EditMarkers(ctx, from, to)
	if err != nil {
		return domain.ScheduleEditMarkersResponse{}, err
	}
	return domain.ScheduleEditMarkersResponse{Markers: rows, Count: len(rows)}, nil
}
