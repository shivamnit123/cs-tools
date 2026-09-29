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

// Package chat posts Google Chat cards for the two things only a person can act on here: a
// webhook we rejected (so the sender can be fixed), and an alert we accepted but couldn't
// store (so no incident was created). Card layout follows sre-alert-core-service's cards.
//
// Posting is asynchronous and rate limited, so it never slows a request or a writer. When
// Chat itself fails, the details are logged at ERROR instead.
package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"sre-alert-ingestion-service/internal/allocator"
	"sre-alert-ingestion-service/internal/server"
	"sre-alert-ingestion-service/internal/textutil"
)

// Settings tunes the cards; see config.toml.example.
type Settings struct {
	// RejectWindow: the first rejection per vendor + error class in each window posts a card,
	// up to maxRejectCards per window on this replica; the rest are counted and reported on the next card.
	RejectWindow time.Duration
	// BodyPreviewChars bounds how much of a rejected body the card shows.
	BodyPreviewChars int
	// CardsPerMinute caps DB-failure cards; beyond it, one summary is posted per interval.
	CardsPerMinute int
	// SummaryInterval is the DB-failure rate-limit window (one minute in production).
	SummaryInterval time.Duration
	HTTPTimeout     time.Duration
}

// Notifier implements server.RejectNotifier and allocator.FailureNotifier.
type Notifier struct {
	logger   *slog.Logger
	urls     []string
	replica  string
	settings Settings
	http     *http.Client
	now      func() time.Time

	mu      sync.Mutex
	rejects map[string]*rejectState
	global  globalRejects
	db      dbState

	sends sync.WaitGroup
	stop  chan struct{}
	done  chan struct{}
}

type rejectState struct {
	lastCard   time.Time
	suppressed int
}

// globalRejects caps rejected cards across all vendors per RejectWindow.
type globalRejects struct {
	windowStart     time.Time
	sent            int
	suppressed      int
	suppressedSince time.Time
}

// maxRejectCards is the most rejected-webhook cards one replica posts per RejectWindow, all vendors together.
const maxRejectCards = 10

type dbState struct {
	sent            int // cards posted in the current interval
	suppressed      int
	suppressedSince time.Time
}

// New starts the DB-failure summary loop. Empty urls logs a warning and posts nothing (local
// dev); rejections and failures are still logged by their callers.
func New(logger *slog.Logger, urls []string, replica string, s Settings) *Notifier {
	if len(urls) == 0 {
		logger.Warn("FALLBACK_CHAT_WEBHOOK_URLS not set; rejected-webhook and DB-failure cards are disabled")
	}
	n := &Notifier{
		logger:   logger,
		urls:     urls,
		replica:  replica,
		settings: s,
		http:     &http.Client{Timeout: s.HTTPTimeout},
		now:      time.Now,
		rejects:  map[string]*rejectState{},
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go n.summaryLoop()
	return n
}

// Rejected posts the rejected-webhook card, at most once per vendor + error class per
// RejectWindow and maxRejectCards per RejectWindow on this replica.
func (n *Notifier) Rejected(r server.Rejection) {
	now := n.now().UTC()
	window := n.settings.RejectWindow
	key := r.Vendor + "|" + errorClass(r.Error)

	n.mu.Lock()
	n.pruneRejects(now, key)
	if now.Sub(n.global.windowStart) >= window {
		n.global.windowStart, n.global.sent = now, 0
	}
	st := n.rejects[key]
	if st != nil && now.Sub(st.lastCard) < window {
		st.suppressed++
		n.mu.Unlock()
		return
	}
	if n.global.sent >= maxRejectCards {
		n.countGlobal(1, now)
		n.mu.Unlock()
		return
	}
	var c rejectCounts
	if st != nil {
		c.same, c.sameSince = st.suppressed, st.lastCard
	}
	c.other, c.otherSince = n.global.suppressed, n.global.suppressedSince
	n.global.suppressed = 0
	n.global.sent++
	n.rejects[key] = &rejectState{lastCard: now}
	n.mu.Unlock()

	n.post(rejectedCard(r, now, n.replica, n.settings.BodyPreviewChars, c),
		"rejected-webhook card not delivered", "vendor", r.Vendor, "request_id", r.RequestID, "error", r.Error)
}

// pruneRejects drops entries older than RejectWindow, except keep's; their uncounted
// rejections move to the global count so the next card still reports them.
func (n *Notifier) pruneRejects(now time.Time, keep string) {
	for key, st := range n.rejects {
		if key != keep && now.Sub(st.lastCard) >= n.settings.RejectWindow {
			if st.suppressed > 0 {
				n.countGlobal(st.suppressed, st.lastCard)
			}
			delete(n.rejects, key)
		}
	}
}

func (n *Notifier) countGlobal(count int, since time.Time) {
	if n.global.suppressed == 0 || since.Before(n.global.suppressedSince) {
		n.global.suppressedSince = since
	}
	n.global.suppressed += count
}

// errorClass is the error text before the first ":", so parser details don't split the limit.
func errorClass(msg string) string {
	class, _, _ := strings.Cut(msg, ":")
	return strings.TrimSpace(class)
}

// StoreFailed posts the DB-failure card, up to CardsPerMinute per interval; the rest are
// rolled into one summary card at the end of the interval.
func (n *Notifier) StoreFailed(f allocator.StoreFailure) {
	n.mu.Lock()
	if n.db.sent >= n.settings.CardsPerMinute {
		if n.db.suppressed == 0 {
			n.db.suppressedSince = n.now().UTC()
		}
		n.db.suppressed++
		n.mu.Unlock()
		return
	}
	n.db.sent++
	n.mu.Unlock()

	// The full alert goes in the fallback log line: if Chat fails too, the log is all that's left.
	n.post(dbFailureCard(f, n.now().UTC(), n.replica), "DB-failure card not delivered; alert NOT stored",
		"vendor", f.Vendor, "alt_id", f.AltID, "request_id", f.RequestID, "alert", f.Alert, "store_error", f.Err)
}

func (n *Notifier) summaryLoop() {
	defer close(n.done)
	ticker := time.NewTicker(n.settings.SummaryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			n.flushSummary()
		case <-n.stop:
			n.flushSummary()
			return
		}
	}
}

func (n *Notifier) flushSummary() {
	n.mu.Lock()
	count, since := n.db.suppressed, n.db.suppressedSince
	n.db = dbState{}
	n.mu.Unlock()
	if count > 0 {
		n.post(dbSummaryCard(count, since, n.replica), "DB-failure summary card not delivered",
			"suppressed_failures", count, "since", since)
	}
}

// Close posts any pending DB-failure summary and waits for in-flight posts or ctx.
func (n *Notifier) Close(ctx context.Context) {
	close(n.stop)
	select {
	case <-n.done:
	case <-ctx.Done():
		return
	}
	sent := make(chan struct{})
	go func() {
		n.sends.Wait()
		close(sent)
	}()
	select {
	case <-sent:
	case <-ctx.Done():
	}
}

// post sends card to every webhook in the background, logging failLog + attrs at ERROR if a
// webhook doesn't accept it.
func (n *Notifier) post(card map[string]any, failLog string, attrs ...any) {
	if len(n.urls) == 0 {
		return
	}
	body, err := json.Marshal(card)
	if err != nil {
		n.logger.Error(failLog, append(attrs, "chat_error", err)...)
		return
	}
	for _, url := range n.urls {
		n.sends.Add(1)
		go func() {
			defer n.sends.Done()
			if err := n.send(url, body); err != nil {
				n.logger.Error(failLog, append(attrs, "chat_error", err)...)
			}
		}()
	}
}

func (n *Notifier) send(url string, body []byte) error {
	resp, err := n.http.Post(url, "application/json; charset=UTF-8", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("chat returned %d", resp.StatusCode)
	}
	return nil
}

const timeLayout = "2006-01-02 15:04:05 UTC"

// rejectCounts are the rejections a card reports on top of its own: for the same vendor +
// error class, and any others held back by the overall cap.
type rejectCounts struct {
	same       int
	sameSince  time.Time
	other      int
	otherSince time.Time
}

func rejectedCard(r server.Rejection, now time.Time, replica string, previewChars int, c rejectCounts) map[string]any {
	details := line("HTTP status", fmt.Sprint(r.Status)) +
		line("Error", r.Error) +
		line("Route", r.Route) +
		line("Request id", r.RequestID) +
		line("Time", now.Format(timeLayout)) +
		line("Replica", replica) +
		line("Remote address", r.RemoteAddr) +
		line("Content type", r.ContentType) +
		line("Body size", fmt.Sprintf("%d bytes", r.BodySize))
	if c.same > 0 {
		details += line("Also rejected", fmt.Sprintf("+%d more since %s", c.same, c.sameSince.Format(timeLayout)))
	}
	if c.other > 0 {
		details += line("Other rejections", fmt.Sprintf("+%d more since %s", c.other, c.otherSince.Format(timeLayout)))
	}
	preview, truncated := textutil.Truncate(string(r.Body), previewChars)
	if truncated || int64(len(r.Body)) < r.BodySize { // Body may already be just the preview
		preview += " …"
	}
	if preview == "" {
		preview = "(empty body)"
	}
	return cardsV2("rejected-"+r.RequestID,
		"<font color='#f70707'><b>Rejected webhook: "+html.EscapeString(r.Vendor)+"</b></font>",
		fmt.Sprintf("HTTP %d | no alert id claimed, nothing stored", r.Status),
		section("", details),
		section(fmt.Sprintf("Body preview (first %d chars)", previewChars), html.EscapeString(preview)),
	)
}

func dbFailureCard(f allocator.StoreFailure, now time.Time, replica string) map[string]any {
	filler := "written; alerts-core skips this id"
	if !f.FillerWritten {
		filler = "NOT written; alerts-core will wait gap_timeout on this id"
	}
	a := f.Alert
	description, _ := textutil.Truncate(a.Description, 500)
	return cardsV2("db-failure-"+f.AltID,
		"<font color='#f70707'><b>DB failure: alert NOT stored</b></font>",
		html.EscapeString(f.Vendor)+" | "+f.AltID,
		section("", "<b>No incident was created and CSM was not notified.</b><br>"+
			line("Vendor", f.Vendor)+
			line("Alert id", f.AltID)+
			line("Error", errString(f.Err))+
			line("Filler row", filler)+
			line("Request id", f.RequestID)+
			line("Replica", replica)+
			line("Time", now.Format(timeLayout))),
		section("Alert",
			line("Service", a.Service)+
				line("Metric name", a.MetricName)+
				line("Severity", a.Severity)+
				line("Category", a.Category)+
				line("Environment", a.Environment)+
				line("Source", a.Source)+
				line("Unique identifier", a.UniqueIdentifier)+
				line("Description", description)),
	)
}

func dbSummaryCard(count int, since time.Time, replica string) map[string]any {
	return cardsV2(fmt.Sprintf("db-failure-summary-%d", since.Unix()),
		"<font color='#f70707'><b>DB failures: "+fmt.Sprint(count)+" more alerts NOT stored</b></font>",
		"since "+since.Format(timeLayout),
		section("", fmt.Sprintf("<b>%d more alerts</b> could not be stored since %s, beyond the per-minute card limit. "+
			"No incidents were created for them. Full details are in the logs of replica %s.",
			count, since.Format(timeLayout), html.EscapeString(replica))),
	)
}

// line renders one "<b>Label:</b> value" row, escaping the value (it may come from a vendor).
func line(label, value string) string {
	if value == "" {
		value = "-"
	}
	return "<b>" + label + ":</b> " + html.EscapeString(value) + "<br>"
}

func section(header, text string) map[string]any {
	s := map[string]any{"widgets": []map[string]any{{"textParagraph": map[string]any{"text": text}}}}
	if header != "" {
		s["header"] = header
	}
	return s
}

func cardsV2(id, title, subtitle string, sections ...map[string]any) map[string]any {
	return map[string]any{
		"cardsV2": []map[string]any{{
			"cardId": id,
			"card": map[string]any{
				"header":   map[string]any{"title": title, "subtitle": subtitle},
				"sections": sections,
			},
		}},
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
