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

	"github.com/wso2-open-operations/cs-tools/entity-service/internal/domain"
)

const testConversationID = "22222222-0000-4000-8000-000000000003"

// stubConversationRepo is a minimal repository.ConversationRepository whose
// unconfigured methods panic if called -- same convention as
// stubCallRequestRepo.
type stubConversationRepo struct {
	updateConversation func(ctx context.Context, id string, state domain.ConversationState, actorEmail string) (domain.UpdatedConversation, error)
}

func (s *stubConversationRepo) SearchConversations(context.Context, domain.SearchConversationsRequest, string) ([]domain.SearchConversationView, int, error) {
	panic("not implemented")
}
func (s *stubConversationRepo) GetConversation(context.Context, string) (domain.ConversationDetails, error) {
	panic("not implemented")
}
func (s *stubConversationRepo) UpdateConversation(ctx context.Context, id string, state domain.ConversationState, actorEmail string) (domain.UpdatedConversation, error) {
	if s.updateConversation != nil {
		return s.updateConversation(ctx, id, state, actorEmail)
	}
	panic("not implemented")
}

// stubMirrorConversationService embeds ConversationService (nil) and
// overrides only UpdateConversation -- same convention as
// stubMirrorCallRequestService.
type stubMirrorConversationService struct {
	ConversationService
	updateConversation func(ctx context.Context, id string, req domain.UpdateConversationRequest) (domain.UpdateConversationResponse, error)
}

func (s *stubMirrorConversationService) UpdateConversation(ctx context.Context, id string, req domain.UpdateConversationRequest) (domain.UpdateConversationResponse, error) {
	return s.updateConversation(ctx, id, req)
}

// TestConversationService_UpdateConversation_MirrorsToServiceNow covers the
// writeback wiring added for fix 3: on a successful Postgres update, the
// mirror's UpdateConversation is dispatched asynchronously and does not
// block or affect the response, and a successful mirror records 0
// sn_writeback_failures.
func TestConversationService_UpdateConversation_MirrorsToServiceNow(t *testing.T) {
	ctx := contextWithUserIDToken(fakeJWTWithEmail(t, "jane.doe@example.com"))
	req := domain.UpdateConversationRequest{State: domain.ConversationStateActive}

	type call struct {
		id  string
		req domain.UpdateConversationRequest
	}
	called := make(chan call, 1)
	mirror := &stubMirrorConversationService{
		updateConversation: func(_ context.Context, id string, mirrorReq domain.UpdateConversationRequest) (domain.UpdateConversationResponse, error) {
			called <- call{id: id, req: mirrorReq}
			return domain.UpdateConversationResponse{}, nil
		},
	}
	repo := &stubConversationRepo{
		updateConversation: func(context.Context, string, domain.ConversationState, string) (domain.UpdatedConversation, error) {
			return domain.UpdatedConversation{ID: testConversationID}, nil
		},
	}
	failures := &recordingSNWritebackFailures{}
	dispatcher := NewSNWritebackDispatcher(failures)
	svc := NewConversationServiceWithSNWriteback(repo, dispatcher, mirror)

	if _, err := svc.UpdateConversation(ctx, testConversationID, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case got := <-called:
		if got.id != testConversationID {
			t.Errorf("mirror UpdateConversation id = %q, want %q", got.id, testConversationID)
		}
		if got.req.State != req.State {
			t.Errorf("mirror UpdateConversation state = %q, want %q", got.req.State, req.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mirror.UpdateConversation was never called")
	}
	if got := failures.count(); got != 0 {
		t.Errorf("expected 0 sn_writeback_failures records for a successful mirror, got %d", got)
	}
}

// TestConversationService_UpdateConversation_MirrorFailureRecordsWritebackFailure
// covers the failure half: Postgres already succeeded, so the call must
// still report success, but the mirror error lands in sn_writeback_failures
// for manual backfill.
func TestConversationService_UpdateConversation_MirrorFailureRecordsWritebackFailure(t *testing.T) {
	ctx := contextWithUserIDToken(fakeJWTWithEmail(t, "jane.doe@example.com"))
	req := domain.UpdateConversationRequest{State: domain.ConversationStateResolved}

	mirror := &stubMirrorConversationService{
		updateConversation: func(context.Context, string, domain.UpdateConversationRequest) (domain.UpdateConversationResponse, error) {
			return domain.UpdateConversationResponse{}, errors.New("sn downstream unreachable")
		},
	}
	repo := &stubConversationRepo{
		updateConversation: func(context.Context, string, domain.ConversationState, string) (domain.UpdatedConversation, error) {
			return domain.UpdatedConversation{ID: testConversationID}, nil
		},
	}
	failures := &recordingSNWritebackFailures{}
	dispatcher := NewSNWritebackDispatcher(failures)
	svc := NewConversationServiceWithSNWriteback(repo, dispatcher, mirror)

	if _, err := svc.UpdateConversation(ctx, testConversationID, req); err != nil {
		t.Fatalf("expected the Postgres-side success to be reported despite the mirror failure, got %v", err)
	}

	waitFor(t, func() bool { return failures.count() == 1 })
}

// TestConversationService_UpdateConversation_NoMirrorOnPlainPostgres covers
// the plain-Postgres (non-dual-write) regression guard: with snWriteback/
// snMirror both nil (NewConversationService, not the SNWriteback
// constructor), UpdateConversation must still succeed and must never touch
// any mirror.
func TestConversationService_UpdateConversation_NoMirrorOnPlainPostgres(t *testing.T) {
	ctx := contextWithUserIDToken(fakeJWTWithEmail(t, "jane.doe@example.com"))
	req := domain.UpdateConversationRequest{State: domain.ConversationStateClosed}

	repo := &stubConversationRepo{
		updateConversation: func(context.Context, string, domain.ConversationState, string) (domain.UpdatedConversation, error) {
			return domain.UpdatedConversation{ID: testConversationID}, nil
		},
	}
	svc := NewConversationService(repo)

	if _, err := svc.UpdateConversation(ctx, testConversationID, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
