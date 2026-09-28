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

package poll

import (
	"time"

	"alert-core-service/internal/engine"
)

// gapTracker maps an alert seq to when this leader first saw it not found, backing GapTimeout.
// Every missing id in the window ages at once, so a run of missing ids costs one GapTimeout in
// total, not one per id. Not safe for concurrent use.
type gapTracker map[int64]time.Time

// pruneThrough drops entries at or below cursor; those ids are behind the pipeline now.
func (g gapTracker) pruneThrough(cursor int64) {
	for seq := range g {
		if seq <= cursor {
			delete(g, seq)
		}
	}
}

// windowDecision is what processWindow does with one window of prepared slots.
type windowDecision struct {
	outcomes []engine.Outcome
	// readStop is the first index deferred to the next cycle; ready slots before it are handled now.
	readStop int
	// skipped are the seqs given up on because they were missing for at least GapTimeout.
	skipped []int64
}

// decideWindow updates gaps from slots, then walks the window from base in id order: ready ids
// are handled, terminal (Failed) ids and ids missing for at least gapTimeout are skipped, and the
// walk stops at a newer missing id or a read error. Read errors never start, reset or trigger a
// skip, so a failing read can't silently drop an alert. gapTimeout <= 0 disables skipping.
func decideWindow(slots []prepared, base int64, now time.Time, gapTimeout time.Duration, gaps gapTracker) windowDecision {
	for i, s := range slots {
		seq := base + int64(i)
		switch {
		case s.ready, s.outcome == engine.Failed:
			delete(gaps, seq) // the row exists
		case s.notFound:
			if _, seen := gaps[seq]; !seen {
				gaps[seq] = now
			}
		}
	}

	d := windowDecision{outcomes: make([]engine.Outcome, len(slots)), readStop: len(slots)}
	for i, s := range slots {
		if s.ready {
			continue
		}
		if s.outcome != engine.Retry {
			d.outcomes[i] = s.outcome // Failed: terminal, skip past it
			continue
		}
		seq := base + int64(i)
		if since, seen := gaps[seq]; s.notFound && seen && gapTimeout > 0 && now.Sub(since) >= gapTimeout {
			d.outcomes[i] = engine.Failed
			d.skipped = append(d.skipped, seq)
			continue
		}
		d.readStop = i
		break
	}
	for i := d.readStop; i < len(slots); i++ {
		d.outcomes[i] = engine.Retry // deferred to the next cycle
	}
	return d
}
