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

// Package auth is the vendor-route auth hook. Only "none" exists today: the
// vendor URLs are public until an auth method is chosen, and the Choreo gateway rate limit is
// the only protection. Adding Basic auth or a shared-secret header later is a new
// Authenticator selected by auth.mode, not a restructure of the router.
package auth

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrUnauthorized is returned by an Authenticator that rejects a request; the router answers 401.
var ErrUnauthorized = errors.New("unauthorized")

// Authenticator decides whether a vendor webhook request may proceed. It runs after the
// router has matched the vendor and before the body is transformed.
type Authenticator interface {
	Authenticate(r *http.Request, vendor string) error
}

// None accepts every request.
type None struct{}

// Authenticate always succeeds.
func (None) Authenticate(*http.Request, string) error { return nil }

// New returns the Authenticator for auth.mode, or an error for an unknown mode so a typo in
// config.toml fails at startup instead of silently leaving the routes open.
func New(mode string) (Authenticator, error) {
	switch mode {
	case "none":
		return None{}, nil
	default:
		return nil, fmt.Errorf("unknown auth.mode %q", mode)
	}
}
