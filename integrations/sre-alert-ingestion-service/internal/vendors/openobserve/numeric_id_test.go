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

package openobserve

import (
	"fmt"
	"testing"
)

func TestTransform_NumericIDStaysExact(t *testing.T) {
	for _, id := range []string{"148502937", "7010000000000000123"} {
		alert, err := Transform([]byte(fmt.Sprintf(`{"short_description":"s","correlation_id":%s}`, id)), Config{})
		if err != nil {
			t.Fatalf("id %s: %v", id, err)
		}
		if got := alert.UniqueIdentifier; got != id {
			t.Errorf("unique_identifier = %q, want %q", got, id)
		}
	}
}
