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
	"log/slog"
	"time"

	"github.com/wso2-open-operations/cs-tools/entity-service/internal/repository"
)

// SLAEngineRecomputeWorker periodically recomputes every source='CSM' "sla"
// row's business_elapsed_percentage and flips it to BREACHED once elapsed
// time reaches its policy's duration (repository.SLAEngineRepository.
// RecomputeActive) -- this is what makes GET /sla-status show a
// live-updating CSM clock, and what lets csm-notification-service's own
// poll of that endpoint alert on a CSM-native breach, exactly as it already
// does for ServiceNow-synced rows. Modeled on CRNoticeDrainer's own Run
// loop (internal/service/cr_notice_drainer.go) -- same shutdown-by-context-
// cancellation shape, minus that drainer's batch/backlog concept: a single
// UPDATE touches every active row per tick, so there is no partial-batch
// case to loop again immediately for.
type SLAEngineRecomputeWorker struct {
	Repo     repository.SLAEngineRepository
	Interval time.Duration
}

// NewSLAEngineRecomputeWorker constructs the worker. interval <= 0 falls
// back to a 45s default -- frequent enough that a 50%/75%/100% tier
// crossing is visible to csm-notification-service's own poll well within
// its own alerting cadence, without adding meaningful load for a table this
// engine expects to stay small.
func NewSLAEngineRecomputeWorker(repo repository.SLAEngineRepository, interval time.Duration) *SLAEngineRecomputeWorker {
	if interval <= 0 {
		interval = 45 * time.Second
	}
	return &SLAEngineRecomputeWorker{Repo: repo, Interval: interval}
}

// Run recomputes on every tick until ctx is cancelled.
func (w *SLAEngineRecomputeWorker) Run(ctx context.Context) {
	slog.InfoContext(ctx, "sla engine: recompute worker started", "interval", w.Interval)
	for {
		n, err := w.Repo.RecomputeActive(ctx)
		if err != nil {
			// Keep going -- a transient database error must not take the
			// worker down for the life of the process; the next tick tries
			// again.
			slog.ErrorContext(ctx, "sla engine: recompute failed", "err", err)
		} else if n > 0 {
			slog.InfoContext(ctx, "sla engine: recomputed active clocks", "count", n)
		}
		select {
		case <-ctx.Done():
			slog.InfoContext(ctx, "sla engine: recompute worker stopped")
			return
		case <-time.After(w.Interval):
		}
	}
}
