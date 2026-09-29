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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gocql/gocql"
)

// DefaultRetryAfter is used when a throttled error doesn't say how long to wait.
const DefaultRetryAfter = 500 * time.Millisecond

var retryAfterMs = regexp.MustCompile(`RetryAfterMs=(\d+)`)

// IsThrottled reports whether Cosmos DB rejected the request for exceeding its RU budget.
// A throttled request was not applied, so it is safe to retry.
func IsThrottled(err error) bool {
	if err == nil {
		return false
	}
	var re gocql.RequestError
	if errors.As(err, &re) && re.Code() == gocql.ErrCodeOverloaded {
		return true
	}
	return strings.Contains(err.Error(), "Request rate is large")
}

// RetryAfter is the wait Cosmos DB asks for in a throttled error, or DefaultRetryAfter.
func RetryAfter(err error) time.Duration {
	if err == nil {
		return DefaultRetryAfter
	}
	if m := retryAfterMs.FindStringSubmatch(err.Error()); m != nil {
		if ms, perr := strconv.Atoi(m[1]); perr == nil {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return DefaultRetryAfter
}
