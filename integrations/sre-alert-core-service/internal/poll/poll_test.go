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
	"testing"

	"alert-core-service/internal/engine"
)

func TestContiguousCompleted(t *testing.T) {
	cases := []struct {
		name     string
		outcomes []engine.Outcome
		want     int
	}{
		{"all processed", []engine.Outcome{engine.Processed, engine.Processed, engine.Processed}, 3},
		{"retry stops the run immediately", []engine.Outcome{engine.Retry, engine.Processed}, 0},
		{"gap in the middle stops at the retry", []engine.Outcome{engine.Processed, engine.Retry, engine.Processed}, 1},
		{"failed counts as completed", []engine.Outcome{engine.Processed, engine.Failed, engine.Processed}, 3},
		{"empty", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := contiguousCompleted(tc.outcomes); got != tc.want {
				t.Errorf("contiguousCompleted(%v) = %d, want %d", tc.outcomes, got, tc.want)
			}
		})
	}
}

func TestShardIsStableForSameFingerprint(t *testing.T) {
	const workers = 8
	fp := "some-fingerprint"
	first := shard(fp, workers)
	for range 100 {
		if got := shard(fp, workers); got != first {
			t.Fatalf("shard(%q, %d) = %d, want stable %d", fp, workers, got, first)
		}
	}
	if first < 0 || first >= workers {
		t.Fatalf("shard(%q, %d) = %d, out of range", fp, workers, first)
	}
}
