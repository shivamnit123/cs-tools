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

// Package lease elects one active processor across replicas via a time-bounded LWT lease, avoiding duplicate incidents.
package lease

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/gocql/gocql"
)

// leaseTable and leaseKey are fixed: only one processor lease row exists for the whole deployment.
const (
	leaseTable = "processor_lease"
	leaseKey   = "poller"
)

// epoch marks the lease row free, used both when seeding and when a leader releases it.
var epoch = time.Unix(0, 0).UTC()

// Lease is a single-holder, time-bounded lock; on holder death it expires after ttl and a standby steals it.
type Lease struct {
	session *gocql.Session
	logger  *slog.Logger
	owner   string
	ttl     time.Duration
	leader  atomic.Bool
	// validUntil is a local deadline closing the window where a hung renew leaves leader stale during a steal.
	validUntil atomic.Int64 // UnixNano; 0 means "never valid".
}

// Identity returns a hostname+random id so concurrent replicas each get a unique owner value.
func Identity() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "pod"
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	return host + "-" + hex.EncodeToString(b[:])
}

// New seeds the free lease row if absent; the returned Lease starts as non-leader until the first Run tick.
func New(session *gocql.Session, logger *slog.Logger, owner string, ttl time.Duration) (*Lease, error) {
	l := &Lease{session: session, logger: logger, owner: owner, ttl: ttl}
	if err := l.seed(context.Background()); err != nil {
		return nil, err
	}
	return l, nil
}

// seed inserts a free lease row so the first acquiring replica can steal it via a normal CAS write.
func (l *Lease) seed(ctx context.Context) error {
	_, err := l.session.Query(
		fmt.Sprintf(`INSERT INTO %s (name, owner, expires_at) VALUES (?, '', ?) IF NOT EXISTS`, leaseTable),
		leaseKey, epoch,
	).WithContext(ctx).MapScanCAS(map[string]any{})
	if err != nil {
		return fmt.Errorf("seed lease: %w", err)
	}
	return nil
}

// IsLeader also checks a local validUntil deadline, since the cached leader flag alone can stay stale after a hung renew.
func (l *Lease) IsLeader() bool {
	if !l.leader.Load() {
		return false
	}
	validUntil := l.validUntil.Load()
	return validUntil != 0 && time.Now().UnixNano() < validUntil
}

// Run renews the lease every renewInterval, which must stay well under ttl so one missed renewal doesn't lose it.
func (l *Lease) Run(ctx context.Context, renewInterval time.Duration) {
	ticker := time.NewTicker(renewInterval)
	defer ticker.Stop()

	l.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.tick(ctx)
		}
	}
}

// tick attempts acquire-or-renew and logs when leadership status flips.
func (l *Lease) tick(ctx context.Context) {
	wasLeader := l.leader.Load()
	renewStart := time.Now()
	isLeader, err := l.tryAcquireOrRenew(ctx, renewStart)
	if err != nil {
		// Stand down on error; row still names us until expiry, so a blip costs time, not correctness.
		l.leader.Store(false)
		l.validUntil.Store(0)
		l.logger.Warn("lease acquire/renew failed, standing down", "error", err)
		return
	}
	switch {
	case isLeader && !wasLeader:
		l.logger.Info("acquired lease, now active processor", "owner", l.owner)
	case !isLeader && wasLeader:
		l.logger.Info("lost lease, now standby", "owner", l.owner)
	}
}

// tryAcquireOrRenew renews, steals an expired lease, or backs off; validUntil uses renewStart to reflect worst-case call duration.
func (l *Lease) tryAcquireOrRenew(ctx context.Context, renewStart time.Time) (bool, error) {
	var owner string
	var expiresAt time.Time
	if err := l.session.Query(
		fmt.Sprintf(`SELECT owner, expires_at FROM %s WHERE name = ?`, leaseTable), leaseKey,
	).WithContext(ctx).Scan(&owner, &expiresAt); err != nil {
		return false, fmt.Errorf("read lease: %w", err)
	}

	now := time.Now().UTC()
	newExpiry := now.Add(l.ttl)

	switch {
	case owner == l.owner:
		return l.cas(ctx, l.owner, newExpiry, renewStart)
	case expiresAt.After(now):
		// A live lease held by someone else keeps this replica on standby until it expires or frees.
		l.leader.Store(false)
		l.validUntil.Store(0)
		return false, nil
	default:
		// Expired or freed: steal it, guarding on the observed owner since Cosmos LWT only supports equality.
		return l.cas(ctx, owner, newExpiry, renewStart)
	}
}

// leaseMargin gives validUntil a safety buffer before the row could actually expire on Cosmos's side.
func (l *Lease) leaseMargin() time.Duration {
	return l.ttl / 4
}

// cas swaps owner only if expectedOwner still matches, so a concurrent writer's CAS wins instead of being overwritten.
func (l *Lease) cas(ctx context.Context, expectedOwner string, newExpiry, renewStart time.Time) (bool, error) {
	applied, err := l.session.Query(
		fmt.Sprintf(`UPDATE %s SET owner = ?, expires_at = ? WHERE name = ? IF owner = ?`, leaseTable),
		l.owner, newExpiry, leaseKey, expectedOwner,
	).WithContext(ctx).MapScanCAS(map[string]any{})
	if err != nil {
		return false, fmt.Errorf("cas lease: %w", err)
	}
	l.leader.Store(applied)
	if applied {
		l.validUntil.Store(renewStart.Add(l.ttl - l.leaseMargin()).UnixNano())
	} else {
		l.validUntil.Store(0)
	}
	return applied, nil
}

// Release frees the lease so a standby can take over immediately instead of waiting out the ttl.
func (l *Lease) Release(ctx context.Context) error {
	l.leader.Store(false)
	l.validUntil.Store(0)
	_, err := l.session.Query(
		fmt.Sprintf(`UPDATE %s SET owner = '', expires_at = ? WHERE name = ? IF owner = ?`, leaseTable),
		epoch, leaseKey, l.owner,
	).WithContext(ctx).MapScanCAS(map[string]any{})
	if err != nil {
		return fmt.Errorf("release lease: %w", err)
	}
	return nil
}
