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
	"crypto/sha256"
	"encoding/base64"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/pbkdf2"
)

func TestNew_UnknownModeFails(t *testing.T) {
	if _, err := New("none", nil); err == nil {
		t.Error("\"none\" should no longer be a valid mode")
	}
	if _, err := New("basic", nil); err == nil {
		t.Error("unknown mode should fail at startup")
	}
}

func TestParseCredentials_Bearer(t *testing.T) {
	token := base64.StdEncoding.EncodeToString([]byte("alice:s3cr3t"))
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	username, secret, ok := parseCredentials(r)
	if !ok || username != "alice" || secret != "s3cr3t" {
		t.Errorf("parseCredentials = (%q, %q, %v), want (alice, s3cr3t, true)", username, secret, ok)
	}
}

func TestParseCredentials_Basic(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.SetBasicAuth("alice", "s3cr3t")

	username, secret, ok := parseCredentials(r)
	if !ok || username != "alice" || secret != "s3cr3t" {
		t.Errorf("parseCredentials = (%q, %q, %v), want (alice, s3cr3t, true)", username, secret, ok)
	}
}

func TestParseCredentials_Malformed(t *testing.T) {
	cases := []string{
		"",
		"Bearer not-base64!!",
		"Bearer " + base64.StdEncoding.EncodeToString([]byte("no-colon")),
		"Bogus scheme",
	}
	for _, header := range cases {
		r := httptest.NewRequest("POST", "/", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		if _, _, ok := parseCredentials(r); ok {
			t.Errorf("parseCredentials(%q) should fail", header)
		}
	}
}

func TestVerifySecret(t *testing.T) {
	salt := []byte("0123456789abcdef")
	iterations := 100
	hash := pbkdf2.Key([]byte("correct-secret"), salt, iterations, keyLen, sha256.New)
	saltB64 := base64.StdEncoding.EncodeToString(salt)
	hashB64 := base64.StdEncoding.EncodeToString(hash)

	if !verifySecret("correct-secret", saltB64, hashB64, iterations) {
		t.Error("correct secret should verify")
	}
	if verifySecret("wrong-secret", saltB64, hashB64, iterations) {
		t.Error("wrong secret should not verify")
	}
	if verifySecret("correct-secret", "not-base64!!", hashB64, iterations) {
		t.Error("malformed salt should not verify")
	}
}
