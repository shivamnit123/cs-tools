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

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wso2-open-operations/cs-tools/apps/csm-portal/backend/internal/middleware"
	"github.com/wso2-open-operations/cs-tools/apps/csm-portal/backend/internal/servicenow"
)

type mockSplReportsClient struct {
	slaReport        servicenow.SLAReportDetails
	slaErr           error
	projectReport    servicenow.CSReportDetails
	projectReportErr error
	timelogs         servicenow.TimeLogBreakdownDetails
	timelogsErr      error
}

func (m *mockSplReportsClient) GetSLAReport(ctx context.Context, projectSysID, from, to string) (servicenow.SLAReportDetails, error) {
	return m.slaReport, m.slaErr
}

func (m *mockSplReportsClient) GetProjectReportDetails(ctx context.Context, projectSysID, from, to string) (servicenow.CSReportDetails, error) {
	return m.projectReport, m.projectReportErr
}

func (m *mockSplReportsClient) GetTimeLogBreakdown(ctx context.Context, projectID string) (servicenow.TimeLogBreakdownDetails, error) {
	return m.timelogs, m.timelogsErr
}

func TestGenerateSLAReport_RequiresQueryParams(t *testing.T) {
	h := NewSplReportsHandler(&mockSplReportsClient{}, splAccessGuard)
	req := withUser(httptest.NewRequest(http.MethodGet, "/spl/generate-sla-report", nil))
	w := httptest.NewRecorder()

	h.GenerateSLAReport(w, req)

	assertStatus(t, w, http.StatusBadRequest)
}

func TestGenerateSLAReport_RejectsMissingSPLAccess(t *testing.T) {
	h := NewSplReportsHandler(&mockSplReportsClient{}, splAccessGuard)
	req := httptest.NewRequest(http.MethodGet, "/spl/generate-sla-report?projectSysId=p1&from=2024-01-01&to=2024-01-31", nil)
	// Authenticated but holds no role granting PermSPLAccess.
	req = req.WithContext(middleware.WithUserInfo(req.Context(), &middleware.UserInfo{Email: "nobody@example.com", UserID: "u-nobody"}))
	w := httptest.NewRecorder()

	h.GenerateSLAReport(w, req)

	assertStatus(t, w, http.StatusForbidden)
}

func TestGenerateSLAReport_Success(t *testing.T) {
	mock := &mockSplReportsClient{slaReport: servicenow.SLAReportDetails{ProjectName: "Acme"}}
	h := NewSplReportsHandler(mock, splAccessGuard)
	req := withUser(httptest.NewRequest(http.MethodGet, "/spl/generate-sla-report?projectSysId=p1&from=2024-01-01&to=2024-01-31", nil))
	w := httptest.NewRecorder()

	h.GenerateSLAReport(w, req)

	assertStatus(t, w, http.StatusOK)
	got := decodeJSON[servicenow.SLAReportDetails](t, w)
	if got.ProjectName != "Acme" {
		t.Errorf("ProjectName = %q, want Acme", got.ProjectName)
	}
}

func TestGenerateSLAReport_RejectsUnsafeProjectSysID(t *testing.T) {
	h := NewSplReportsHandler(&mockSplReportsClient{}, splAccessGuard)
	req := withUser(httptest.NewRequest(http.MethodGet, "/spl/generate-sla-report?projectSysId=p1%5EORactive%3Dtrue&from=2024-01-01&to=2024-01-31", nil))
	w := httptest.NewRecorder()

	h.GenerateSLAReport(w, req)

	assertStatus(t, w, http.StatusBadRequest)
}

func TestGenerateTimelogsBreakdownReport_NotFound(t *testing.T) {
	mock := &mockSplReportsClient{timelogsErr: servicenow.ErrProjectNotFound}
	h := NewSplReportsHandler(mock, splAccessGuard)
	req := withUser(httptest.NewRequest(http.MethodGet, "/spl/generate-timelogs-breakdown-report?projectId=p1", nil))
	w := httptest.NewRecorder()

	h.GenerateTimelogsBreakdownReport(w, req)

	assertStatus(t, w, http.StatusNotFound)
}

func TestGetReportDetails_Success(t *testing.T) {
	mock := &mockSplReportsClient{projectReport: servicenow.CSReportDetails{SubscriptionDetails: servicenow.SubscriptionDetail{ProjectName: "Acme"}}}
	h := NewSplReportsHandler(mock, splAccessGuard)
	req := withUser(httptest.NewRequest(http.MethodGet, "/spl/report-details?projectSysId=p1&from=2024-01-01&to=2024-01-31", nil))
	w := httptest.NewRecorder()

	h.GetReportDetails(w, req)

	assertStatus(t, w, http.StatusOK)
}
