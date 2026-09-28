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

package lease

import (
	"testing"
	"time"
)

// newTestLease builds a Lease with no live Cassandra session, for local leader/validUntil bookkeeping.
func newTestLease(ttl time.Duration) *Lease {
	return &Lease{owner: "test-owner", ttl: ttl}
}

func TestIsLeaderFalseWhenNeverAcquired(t *testing.T) {
	l := newTestLease(15 * time.Second)
	if l.IsLeader() {
		t.Fatal("expected IsLeader to be false before any successful acquire")
	}
}

func TestIsLeaderTrueWithinValidUntil(t *testing.T) {
	l := newTestLease(15 * time.Second)
	renewStart := time.Now()
	l.leader.Store(true)
	l.validUntil.Store(renewStart.Add(l.ttl - l.leaseMargin()).UnixNano())

	if !l.IsLeader() {
		t.Fatal("expected IsLeader to be true immediately after a successful renew")
	}
}

func TestIsLeaderFalseAfterHungRenew(t *testing.T) {
	l := newTestLease(15 * time.Second)
	renewStart := time.Now().Add(-20 * time.Second) // a renew that started long enough ago that validUntil has elapsed
	l.leader.Store(true)
	l.validUntil.Store(renewStart.Add(l.ttl - l.leaseMargin()).UnixNano())

	if l.IsLeader() {
		t.Fatal("expected IsLeader to be false once the locally-computed validUntil deadline has passed, even though the cached leader flag is still true")
	}
}

func TestIsLeaderFalseAfterRelease(t *testing.T) {
	l := newTestLease(15 * time.Second)
	l.leader.Store(true)
	l.validUntil.Store(time.Now().Add(time.Minute).UnixNano())

	l.leader.Store(false)
	l.validUntil.Store(0)

	if l.IsLeader() {
		t.Fatal("expected IsLeader to be false after leader/validUntil are cleared, matching Release's behavior")
	}
}

func TestLeaseMarginIsBoundedByTTL(t *testing.T) {
	l := newTestLease(20 * time.Second)
	margin := l.leaseMargin()
	if margin <= 0 || margin >= l.ttl {
		t.Fatalf("leaseMargin() = %v, want strictly between 0 and ttl (%v)", margin, l.ttl)
	}
}

func TestIdentityIsUniquePerCall(t *testing.T) {
	a := Identity()
	b := Identity()
	if a == b {
		t.Fatalf("Identity() returned the same value twice: %q", a)
	}
	if a == "" || b == "" {
		t.Fatal("Identity() must never be empty")
	}
}
