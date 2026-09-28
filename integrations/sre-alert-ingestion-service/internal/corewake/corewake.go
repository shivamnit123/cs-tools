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

// Package corewake nudges sre-alert-core-service to poll now instead of at its next tick.
// It's best effort: alerts-core's own poll picks new alerts up regardless, so failures are
// only logged. Only alerts-core's lease holder acts on the call; the others answer 202 and
// ignore it.
package corewake

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Client sends POST <url> with at most one call in flight per replica. A wake that arrives
// while a call is in flight is coalesced into a single follow-up call, so a batch written
// mid-call is never left waiting for alerts-core's backstop poll.
type Client struct {
	logger  *slog.Logger
	url     string
	http    *http.Client
	mu      sync.Mutex
	running bool
	pending bool
	idle    *sync.Cond
}

// New returns a Client. An empty url logs a warning and makes Wake a no-op (local dev).
func New(logger *slog.Logger, url string, timeout time.Duration) *Client {
	if url == "" {
		logger.Warn("ALERT_CORE_WAKE_URL not set; alerts-core will pick alerts up on its own poll")
	}
	c := &Client{logger: logger, url: url, http: &http.Client{Timeout: timeout}}
	c.idle = sync.NewCond(&c.mu)
	return c
}

// Wake requests a wake-up without blocking.
func (c *Client) Wake() {
	if c.url == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		c.pending = true
		return
	}
	c.running = true
	go c.loop()
}

func (c *Client) loop() {
	for {
		c.send()
		c.mu.Lock()
		if !c.pending {
			c.running = false
			c.idle.Broadcast()
			c.mu.Unlock()
			return
		}
		c.pending = false
		c.mu.Unlock()
	}
}

func (c *Client) send() {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.url, http.NoBody)
	if err != nil {
		c.logger.Warn("alerts-core wake-up request invalid", "error", err)
		return
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.logger.Warn("alerts-core wake-up failed; its poll will pick the alerts up", "error", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		c.logger.Warn("alerts-core wake-up returned unexpected status", "status", resp.StatusCode)
	}
}

// Wait blocks until no call is in flight or ctx ends; used on shutdown.
func (c *Client) Wait(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		c.mu.Lock()
		for c.running {
			c.idle.Wait()
		}
		c.mu.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
