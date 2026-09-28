// Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
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

package chat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sre-alert-ingestion-service/internal/allocator"
	"sre-alert-ingestion-service/internal/model"
	"sre-alert-ingestion-service/internal/server"
)

type fakeChat struct {
	mu     sync.Mutex
	cards  []string
	status int
	srv    *httptest.Server
}

func newFakeChat(t *testing.T) *fakeChat {
	f := &fakeChat{status: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.cards = append(f.cards, string(b))
		status := f.status
		f.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeChat) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cards...)
}

func settings() Settings {
	return Settings{RejectWindow: 15 * time.Minute, BodyPreviewChars: 20, CardsPerMinute: 2,
		SummaryInterval: time.Hour, HTTPTimeout: time.Second}
}

func newNotifier(t *testing.T, urls []string, logger *slog.Logger) (*Notifier, *time.Time) {
	t.Helper()
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	n := New(logger, urls, "ingestion-7f9c", settings())
	clock := time.Date(2026, 9, 27, 6, 0, 0, 0, time.UTC)
	n.now = func() time.Time { return clock }
	t.Cleanup(func() { n.Close(context.Background()) })
	return n, &clock
}

func rejection(vendor, errMsg string) server.Rejection {
	return server.Rejection{
		Vendor: vendor, Status: 400, Error: errMsg, Route: "/api/wso2/v1/sre_alert_api/" + vendor,
		RequestID: "req-1", RemoteAddr: "203.0.113.9:4312", ContentType: "application/json",
		Body: []byte(`{"monitor":"<script>alert(1)</script>","long":"xxxxxxxxxxxxxxxxxxxxxxxx"}`), BodySize: 75,
	}
}

func TestRejectedCard_Content(t *testing.T) {
	chat := newFakeChat(t)
	n, _ := newNotifier(t, []string{chat.srv.URL}, nil)
	n.Rejected(rejection("datadog", "INVALID DATADOG ALERT PAYLOAD STRUCTURE"))
	n.sends.Wait()

	cards := chat.all()
	if len(cards) != 1 {
		t.Fatalf("cards = %d, want 1", len(cards))
	}
	c := cards[0]
	for _, want := range []string{
		"Rejected webhook: datadog", "HTTP status:\\u003c/b\\u003e 400", "INVALID DATADOG ALERT PAYLOAD STRUCTURE",
		"/api/wso2/v1/sre_alert_api/datadog", "req-1", "2026-09-27 06:00:00 UTC", "ingestion-7f9c",
		"203.0.113.9:4312", "application/json", "75 bytes", "first 20 chars",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("card missing %q:\n%s", want, c)
		}
	}
	if strings.Contains(c, "<script>") || strings.Contains(c, "\\u003cscript") {
		t.Error("vendor-controlled body must be HTML-escaped in the card")
	}
	if strings.Contains(c, "xxxxxxxx") {
		t.Error("body preview must stop at body_preview_chars")
	}
}

func TestRejected_RateLimitedPerVendorAndError(t *testing.T) {
	chat := newFakeChat(t)
	n, clock := newNotifier(t, []string{chat.srv.URL}, nil)

	for range 3 {
		n.Rejected(rejection("datadog", "BAD"))
	}
	n.Rejected(rejection("datadog", "OTHER")) // different error: its own card
	n.Rejected(rejection("icinga", "BAD"))    // different vendor: its own card
	n.sends.Wait()
	if got := len(chat.all()); got != 3 {
		t.Fatalf("cards = %d, want 3 (one per vendor+error)", got)
	}

	*clock = clock.Add(16 * time.Minute)
	n.Rejected(rejection("datadog", "BAD"))
	n.sends.Wait()
	cards := chat.all()
	if len(cards) != 4 {
		t.Fatalf("cards = %d, want 4 after the window passed", len(cards))
	}
	if !strings.Contains(cards[3], "+2 more since 2026-09-27 06:00:00 UTC") {
		t.Errorf("next card should report the suppressed rejections:\n%s", cards[3])
	}
}

func TestRejected_ParserErrorsShareOneCard(t *testing.T) {
	chat := newFakeChat(t)
	n, clock := newNotifier(t, []string{chat.srv.URL}, nil)

	for _, detail := range []string{"invalid character 'x' looking for beginning of value",
		"unexpected end of JSON input", "invalid character '}' after object key"} {
		n.Rejected(rejection("datadog", "INVALID DATADOG ALERT PAYLOAD STRUCTURE: "+detail))
	}
	n.sends.Wait()
	if got := len(chat.all()); got != 1 {
		t.Fatalf("cards = %d, want 1 for one error class", got)
	}

	*clock = clock.Add(16 * time.Minute)
	n.Rejected(rejection("datadog", "INVALID DATADOG ALERT PAYLOAD STRUCTURE: other"))
	n.sends.Wait()
	if cards := chat.all(); len(cards) != 2 || !strings.Contains(cards[1], "+2 more since") {
		t.Errorf("next card should report the 2 held back:\n%s", strings.Join(cards, "\n"))
	}
}

func TestRejected_GlobalCapPerWindow(t *testing.T) {
	chat := newFakeChat(t)
	n, clock := newNotifier(t, []string{chat.srv.URL}, nil)

	for i := range maxRejectCards + 5 {
		n.Rejected(rejection(fmt.Sprintf("vendor%d", i), "BAD"))
	}
	n.sends.Wait()
	if got := len(chat.all()); got != maxRejectCards {
		t.Fatalf("cards = %d, want the cap of %d", got, maxRejectCards)
	}

	*clock = clock.Add(16 * time.Minute)
	n.Rejected(rejection("aws", "BAD"))
	n.sends.Wait()
	cards := chat.all()
	if len(cards) != maxRejectCards+1 || !strings.Contains(cards[maxRejectCards], "Other rejections") ||
		!strings.Contains(cards[maxRejectCards], "+5 more since") {
		t.Errorf("next card should report the 5 held back by the cap:\n%s", cards[len(cards)-1])
	}
	n.mu.Lock()
	entries := len(n.rejects)
	n.mu.Unlock()
	if entries != 1 {
		t.Errorf("rate-limit entries = %d, want 1 (older ones dropped)", entries)
	}
}

func storeFailure(id string) allocator.StoreFailure {
	return allocator.StoreFailure{
		Vendor: "prometheus", RequestID: "req-9", AltID: id, FillerWritten: true, Err: errors.New("write timeout"),
		Alert: model.Alert{Service: "payments", MetricName: "PodCrashLoopBackOff", Severity: "Critical",
			Environment: "production", Source: "Prometheus", UniqueIdentifier: "a1b2"},
	}
}

func TestDBFailureCard_Content(t *testing.T) {
	chat := newFakeChat(t)
	n, _ := newNotifier(t, []string{chat.srv.URL}, nil)
	n.StoreFailed(storeFailure("ALT000000042"))
	n.sends.Wait()

	c := chat.all()[0]
	for _, want := range []string{
		"DB failure: alert NOT stored", "No incident was created and CSM was not notified",
		"ALT000000042", "prometheus", "write timeout", "written; alerts-core skips this id",
		"req-9", "ingestion-7f9c", "payments", "PodCrashLoopBackOff", "a1b2",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("card missing %q:\n%s", want, c)
		}
	}

	f := storeFailure("ALT000000043")
	f.FillerWritten = false
	n.StoreFailed(f)
	n.sends.Wait()
	if !strings.Contains(chat.all()[1], "NOT written; alerts-core will wait gap_timeout") {
		t.Error("card should say the filler row is missing")
	}
}

func TestDBFailure_RateLimitedThenSummary(t *testing.T) {
	chat := newFakeChat(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	n := New(logger, []string{chat.srv.URL}, "ingestion-7f9c", settings()) // CardsPerMinute: 2
	for i := range 5 {
		n.StoreFailed(storeFailure("ALT00000000" + string(rune('1'+i))))
	}
	n.sends.Wait()
	if got := len(chat.all()); got != 2 {
		t.Fatalf("cards = %d, want 2 (cards_per_minute)", got)
	}

	n.Close(context.Background()) // flushes the interval's summary
	cards := chat.all()
	if len(cards) != 3 || !strings.Contains(cards[2], "3 more alerts NOT stored") {
		t.Errorf("want one summary for the 3 suppressed failures, got %d cards:\n%s", len(cards), strings.Join(cards, "\n"))
	}
}

func TestChatFailure_LogsTheFullAlert(t *testing.T) {
	chat := newFakeChat(t)
	chat.status = http.StatusInternalServerError
	var logs bytes.Buffer
	var logMu sync.Mutex
	logger := slog.New(slog.NewTextHandler(&lockedWriter{w: &logs, mu: &logMu}, nil))
	n, _ := newNotifier(t, []string{chat.srv.URL}, logger)

	n.StoreFailed(storeFailure("ALT000000042"))
	n.sends.Wait()
	logMu.Lock()
	out := logs.String()
	logMu.Unlock()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "PodCrashLoopBackOff") || !strings.Contains(out, "ALT000000042") {
		t.Errorf("a failed DB-failure card should log the full alert at ERROR, got:\n%s", out)
	}
}

func TestNoWebhooks_PostsNothing(t *testing.T) {
	n, _ := newNotifier(t, nil, nil)
	n.Rejected(rejection("aws", "x"))
	n.StoreFailed(storeFailure("ALT000000001"))
	n.sends.Wait()
}

func TestPostsToEveryWebhook(t *testing.T) {
	a, b := newFakeChat(t), newFakeChat(t)
	n, _ := newNotifier(t, []string{a.srv.URL, b.srv.URL}, nil)
	n.Rejected(rejection("aws", "x"))
	n.sends.Wait()
	if len(a.all()) != 1 || len(b.all()) != 1 {
		t.Errorf("cards = %d and %d, want 1 each", len(a.all()), len(b.all()))
	}
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
