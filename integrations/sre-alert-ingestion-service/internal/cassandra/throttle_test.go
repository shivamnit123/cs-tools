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

package cassandra

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gocql/gocql"
)

// requestErr stands in for gocql's unexported error frames.
type requestErr struct {
	code int
	msg  string
}

func (e requestErr) Code() int       { return e.code }
func (e requestErr) Message() string { return e.msg }
func (e requestErr) Error() string   { return e.msg }

var _ gocql.RequestError = requestErr{}

const cosmos429 = "Request rate is large. More Request Units may be needed, so no changes were made. " +
	"Please retry this request later. ActivityId: 1f2e, Request Charge: 0, RetryAfterMs=107, Additional details='Response status code does not indicate success: TooManyRequests (429)'"

func TestIsThrottled(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"overloaded code":    {requestErr{gocql.ErrCodeOverloaded, "server overloaded"}, true},
		"wrapped overloaded": {fmt.Errorf("insert ALT1: %w", requestErr{gocql.ErrCodeOverloaded, "x"}), true},
		"cosmos message":     {fmt.Errorf("insert ALT1: %w", errors.New(cosmos429)), true},
		"write timeout":      {requestErr{gocql.ErrCodeWriteTimeout, "timeout"}, false},
		"plain error":        {errors.New("connection refused"), false},
		"nil":                {nil, false},
	}
	for name, tc := range cases {
		if got := IsThrottled(tc.err); got != tc.want {
			t.Errorf("%s: IsThrottled = %v, want %v", name, got, tc.want)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	if got := RetryAfter(errors.New(cosmos429)); got != 107*time.Millisecond {
		t.Errorf("RetryAfter = %v, want 107ms", got)
	}
	if got := RetryAfter(requestErr{gocql.ErrCodeOverloaded, "overloaded"}); got != DefaultRetryAfter {
		t.Errorf("RetryAfter without a hint = %v, want %v", got, DefaultRetryAfter)
	}
	if got := RetryAfter(nil); got != DefaultRetryAfter {
		t.Errorf("RetryAfter(nil) = %v, want %v", got, DefaultRetryAfter)
	}
}
