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

package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gocql/gocql"
	"golang.org/x/crypto/pbkdf2"
)

const bearerPrefix = "Bearer "

// keyLen is the PBKDF2 derived key length in bytes; must match sre-alert-core-service's
// internal/auth.KeyLen since both read the same integration_users rows.
const keyLen = 32

// IntegrationUsers verifies vendor webhooks against alerts-core's alertintegration.integration_users
// table: any enabled, non-expired row with a matching secret authenticates the request. It does not
// tie a credential to a particular vendor -- one row may be used for any number of vendors, and any
// number of rows may exist.
type IntegrationUsers struct {
	session *gocql.Session
}

// NewIntegrationUsers wraps session for read-only credential checks.
func NewIntegrationUsers(session *gocql.Session) *IntegrationUsers {
	return &IntegrationUsers{session: session}
}

// integrationUser is the subset of integration_users needed to verify a request.
type integrationUser struct {
	SecretHash string
	Salt       string
	Iterations int
	Enabled    bool
	ExpiresAt  time.Time
}

// Authenticate accepts a request carrying a valid Authorization header (Bearer base64("user:secret")
// or Basic) naming an enabled, non-expired integration_users row with a matching secret.
func (a *IntegrationUsers) Authenticate(r *http.Request, _ string) error {
	username, secret, ok := parseCredentials(r)
	if !ok {
		return ErrUnauthorized
	}

	u, err := a.get(r.Context(), username)
	if err != nil {
		return ErrUnauthorized
	}
	if !u.Enabled {
		return ErrUnauthorized
	}
	if !verifySecret(secret, u.Salt, u.SecretHash, u.Iterations) {
		return ErrUnauthorized
	}
	if isExpired(u.ExpiresAt, time.Now()) {
		return ErrUnauthorized
	}
	return nil
}

// isExpired reports whether expiresAt is set and in the past. Cosmos DB for Cassandra
// round-trips an unset (NULL) timestamp as the Unix epoch rather than Go's true zero time.Time,
// so anything at or before the epoch is treated as unset too.
func isExpired(expiresAt, now time.Time) bool {
	if expiresAt.IsZero() || !expiresAt.After(time.Unix(0, 0)) {
		return false
	}
	return now.After(expiresAt)
}

func (a *IntegrationUsers) get(ctx context.Context, username string) (integrationUser, error) {
	var u integrationUser
	err := a.session.Query(
		`SELECT secret_hash, salt, iterations, enabled, expires_at FROM integration_users WHERE username = ?`,
		username,
	).WithContext(ctx).Scan(&u.SecretHash, &u.Salt, &u.Iterations, &u.Enabled, &u.ExpiresAt)
	if err != nil {
		return integrationUser{}, fmt.Errorf("read integration user %s: %w", username, err)
	}
	return u, nil
}

// parseCredentials extracts username/secret from either Bearer base64("<username>:<secret>") or
// Basic (decoded via net/http's BasicAuth) -- same two forms sre-alert-core-service accepts.
func parseCredentials(r *http.Request) (username, secret string, ok bool) {
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, bearerPrefix) {
		token := strings.TrimPrefix(header, bearerPrefix)
		decoded, err := base64.StdEncoding.DecodeString(token)
		if err != nil {
			return "", "", false
		}
		username, secret, found := strings.Cut(string(decoded), ":")
		if !found || username == "" || secret == "" {
			return "", "", false
		}
		return username, secret, true
	}

	username, secret, ok = r.BasicAuth()
	if !ok || username == "" || secret == "" {
		return "", "", false
	}
	return username, secret, true
}

// verifySecret recomputes the PBKDF2-HMAC-SHA256 hash for secret against the stored base64
// salt/hash and compares in constant time.
func verifySecret(secret, saltB64, hashB64 string, iterations int) bool {
	salt, err := base64.StdEncoding.DecodeString(saltB64)
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(hashB64)
	if err != nil {
		return false
	}
	got := pbkdf2.Key([]byte(secret), salt, iterations, keyLen, sha256.New)
	return subtle.ConstantTimeCompare(got, want) == 1
}
