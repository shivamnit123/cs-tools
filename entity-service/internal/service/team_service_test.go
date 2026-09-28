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

	"github.com/wso2-open-operations/cs-tools/entity-service/internal/apierror"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/repository"
)

type fakeTeamRepo struct {
	exists     bool
	existsErr  error
	members    []repository.TeamMemberRow
	membersErr error
}

func (f *fakeTeamRepo) TeamExists(context.Context, string) (bool, error) {
	return f.exists, f.existsErr
}

func (f *fakeTeamRepo) GetTeamMembers(context.Context, string) ([]repository.TeamMemberRow, error) {
	return f.members, f.membersErr
}

const testTeamID = "11111111-1111-1111-1111-111111111111"

func TestTeamService_GetTeamMembers_RequiresAuthentication(t *testing.T) {
	svc := NewTeamService(&fakeTeamRepo{exists: true})

	_, err := svc.GetTeamMembers(contextWithUserIDToken(""), testTeamID)

	var ue *apierror.UnauthorizedError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnauthorizedError for a tokenless request, got %v", err)
	}
}

func TestTeamService_GetTeamMembers_NotFound(t *testing.T) {
	svc := NewTeamService(&fakeTeamRepo{exists: false})

	_, err := svc.GetTeamMembers(contextWithUserIDToken("tok"), testTeamID)

	var nf *apierror.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("expected NotFoundError for a missing team, got %v", err)
	}
}

func TestTeamService_GetTeamMembers_ReturnsRoster(t *testing.T) {
	role := "lead"
	email := "lead@wso2.com"
	repo := &fakeTeamRepo{
		exists: true,
		members: []repository.TeamMemberRow{
			{UserID: "u1", Name: "A Lead", Email: &email, Role: role},
		},
	}
	svc := NewTeamService(repo)

	resp, err := svc.GetTeamMembers(contextWithUserIDToken("tok"), testTeamID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Members) != 1 || resp.Members[0].ID != "u1" || resp.Members[0].Role == nil || *resp.Members[0].Role != "lead" {
		t.Fatalf("unexpected members: %+v", resp.Members)
	}
}
