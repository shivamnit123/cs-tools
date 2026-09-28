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

// Package csm is a client for csm-integration-service, duplicated per-service since Go modules here don't share internal/ packages.
package csm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"alert-core-service/internal/apierror"
)

// tokenFetchTimeout is the HTTP client timeout for token-endpoint requests.
var tokenFetchTimeout = 10 * time.Second

type ctxKey string

const correlationIDKey ctxKey = "x-csm-correlation-id" // #nosec G101 -- context map key, not a credential

// WithCorrelationID returns a copy of ctx carrying the id forwarded as X-CSM-Correlation-ID.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey, id)
}

func correlationIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(correlationIDKey).(string)
	return v
}

type Config struct {
	BaseURL      string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

// Client authenticates via OAuth2 client-credentials grant; tokens are acquired and refreshed automatically.
type Client struct {
	http    *http.Client
	baseURL string
}

// NewClient constructs a Client using the OAuth2 client-credentials grant.
func NewClient(cfg Config) *Client {
	cc := clientcredentials.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		TokenURL:     cfg.TokenURL,
		Scopes:       cfg.Scopes,
	}

	tokenHTTPClient := &http.Client{
		Timeout:   tokenFetchTimeout,
		Transport: &httpsOnlyTransport{},
	}
	tokenHTTPClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	tokenCtx := context.WithValue(context.Background(), oauth2.HTTPClient, tokenHTTPClient)
	httpClient := cc.Client(tokenCtx)
	httpClient.Timeout = 25 * time.Second
	httpClient.Transport = &httpsOnlyTransport{base: httpClient.Transport}
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &Client{
		http:    httpClient,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
	}
}

// do executes an authenticated request; the caller owns the returned response body slice.
func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	var reqBody io.Reader
	if len(body) > 0 {
		reqBody = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("csm: build request %s %s: %w", method, path, err)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if id := correlationIDFromContext(ctx); id != "" {
		req.Header.Set("X-CSM-Correlation-ID", id)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("csm: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		const maxErrBody = 256
		excerpt, err := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		if err != nil {
			return nil, fmt.Errorf("csm: read error response body: %w", err)
		}
		return nil, &apierror.Error{StatusCode: resp.StatusCode, Body: string(excerpt)}
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("csm: read response body: %w", err)
	}

	return respBody, nil
}

// httpsOnlyTransport blocks non-HTTPS requests since this client always carries a secret or bearer token.
type httpsOnlyTransport struct {
	base http.RoundTripper
}

func (t *httpsOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || req.URL.Scheme != "https" {
		return nil, fmt.Errorf("csm: refusing non-HTTPS endpoint %s", req.URL.Redacted())
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}
