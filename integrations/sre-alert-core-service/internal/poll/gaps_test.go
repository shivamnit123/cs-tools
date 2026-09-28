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
	"context"
	"slices"
	"testing"
	"time"

	"alert-core-service/internal/engine"
)

var (
	t0  = time.Unix(1_700_000_000, 0)
	gap = 10 * time.Minute
)

const base = int64(100)

func ready() prepared    { return prepared{ready: true, outcome: engine.Processed} }
func missing() prepared  { return prepared{outcome: engine.Retry, notFound: true} }
func readErr() prepared  { return prepared{outcome: engine.Retry} }
func terminal() prepared { return prepared{outcome: engine.Failed} }

func TestDecideWindow_SingleMissingHeadSkippedOnlyAfterGapTimeout(t *testing.T) {
	gaps := gapTracker{}
	slots := []prepared{missing(), ready()}

	d := decideWindow(slots, base, t0, gap, gaps)
	if d.readStop != 0 || len(d.skipped) != 0 {
		t.Fatalf("first sighting: readStop=%d skipped=%v, want 0 and none", d.readStop, d.skipped)
	}
	d = decideWindow(slots, base, t0.Add(gap-time.Second), gap, gaps)
	if d.readStop != 0 || len(d.skipped) != 0 {
		t.Fatalf("before gap_timeout: readStop=%d skipped=%v, want 0 and none", d.readStop, d.skipped)
	}
	d = decideWindow(slots, base, t0.Add(gap), gap, gaps)
	if d.readStop != 2 || !slices.Equal(d.skipped, []int64{base}) || d.outcomes[0] != engine.Failed {
		t.Fatalf("after gap_timeout: readStop=%d skipped=%v outcomes=%v", d.readStop, d.skipped, d.outcomes)
	}
}

func TestDecideWindow_ConsecutiveMissingSkippedTogether(t *testing.T) {
	gaps := gapTracker{}
	slots := []prepared{missing(), missing(), missing(), ready()}
	decideWindow(slots, base, t0, gap, gaps)

	d := decideWindow(slots, base, t0.Add(gap), gap, gaps)
	if !slices.Equal(d.skipped, []int64{base, base + 1, base + 2}) {
		t.Fatalf("skipped = %v, want all three in one decision", d.skipped)
	}
	if got := contiguousCompleted(d.outcomes[:3]); got != 3 {
		t.Errorf("completed prefix = %d, want 3", got)
	}
	if d.readStop != 4 {
		t.Errorf("readStop = %d, want 4 so the ready id after the gap is handled now", d.readStop)
	}
}

func TestDecideWindow_MixedWindow(t *testing.T) {
	// [missing-old, ready, missing-old, missing-new, ready]: skip, handle, skip, stop.
	gaps := gapTracker{base: t0, base + 2: t0}
	slots := []prepared{missing(), ready(), missing(), missing(), ready()}

	d := decideWindow(slots, base, t0.Add(gap), gap, gaps)
	if d.readStop != 3 {
		t.Fatalf("readStop = %d, want 3 (stop at the newly missing id)", d.readStop)
	}
	if !slices.Equal(d.skipped, []int64{base, base + 2}) {
		t.Errorf("skipped = %v, want [%d %d]", d.skipped, base, base+2)
	}
	want := []engine.Outcome{engine.Failed, engine.Processed, engine.Failed, engine.Retry, engine.Retry}
	if !slices.Equal(d.outcomes, want) {
		t.Errorf("outcomes = %v, want %v", d.outcomes, want)
	}
	if got := gaps[base+3]; !got.Equal(t0.Add(gap)) {
		t.Errorf("newly missing id's timer = %v, want it started now", got)
	}
}

func TestDecideWindow_ReadErrorBlocksAndNeverSkips(t *testing.T) {
	// An old entry must not turn a read error into a skip, and a read error must not start a timer.
	gaps := gapTracker{base: t0}
	slots := []prepared{readErr(), readErr(), ready()}

	d := decideWindow(slots, base, t0.Add(10*gap), gap, gaps)
	if d.readStop != 0 || len(d.skipped) != 0 {
		t.Fatalf("readStop=%d skipped=%v, want 0 and none", d.readStop, d.skipped)
	}
	if got := gaps[base]; !got.Equal(t0) {
		t.Errorf("read error changed the existing timer to %v", got)
	}
	if _, ok := gaps[base+1]; ok {
		t.Error("read error started a timer")
	}
}

func TestDecideWindow_TimerStartsAtFirstSightingOnly(t *testing.T) {
	gaps := gapTracker{}
	slots := []prepared{missing()}
	decideWindow(slots, base, t0, gap, gaps)
	decideWindow(slots, base, t0.Add(time.Minute), gap, gaps)
	if got := gaps[base]; !got.Equal(t0) {
		t.Errorf("timer = %v, want the first sighting %v", got, t0)
	}
}

func TestDecideWindow_RowAppearingClearsTimer(t *testing.T) {
	gaps := gapTracker{base: t0, base + 1: t0}
	decideWindow([]prepared{ready(), terminal()}, base, t0.Add(gap), gap, gaps)
	if len(gaps) != 0 {
		t.Errorf("gaps = %v, want empty once the rows exist", gaps)
	}
}

func TestDecideWindow_TerminalRowSkippedAsBefore(t *testing.T) {
	d := decideWindow([]prepared{terminal(), ready()}, base, t0, gap, gapTracker{})
	if d.readStop != 2 || d.outcomes[0] != engine.Failed || len(d.skipped) != 0 {
		t.Errorf("readStop=%d outcomes=%v skipped=%v", d.readStop, d.outcomes, d.skipped)
	}
}

func TestDecideWindow_GapTimeoutZeroNeverSkips(t *testing.T) {
	gaps := gapTracker{base: t0}
	d := decideWindow([]prepared{missing()}, base, t0.Add(1000*time.Hour), 0, gaps)
	if d.readStop != 0 || len(d.skipped) != 0 {
		t.Errorf("readStop=%d skipped=%v, want no skip when gap_timeout is 0", d.readStop, d.skipped)
	}
}

func TestGapTracker_PruneThrough(t *testing.T) {
	gaps := gapTracker{base: t0, base + 1: t0, base + 5: t0}
	gaps.pruneThrough(base + 1)
	if _, ok := gaps[base+5]; len(gaps) != 1 || !ok {
		t.Errorf("gaps = %v, want only %d left", gaps, base+5)
	}
}

type fixedLeader bool

func (l fixedLeader) IsLeader() bool { return bool(l) }

func TestCycle_NotLeaderClearsTimers(t *testing.T) {
	p := &Poller{leader: fixedLeader(false), gaps: gapTracker{base: t0}}
	p.cycle(context.Background())
	if len(p.gaps) != 0 {
		t.Errorf("gaps = %v, want cleared when not the leader", p.gaps)
	}
}
