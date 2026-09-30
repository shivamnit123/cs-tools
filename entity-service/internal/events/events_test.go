// Copyright (c) 2026 WSO2 LLC. (https://www.wso2.com).
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

package events

import (
	"encoding/json"
	"strings"
	"testing"
)

// csm-notification-service decodes the resend marker as "isResend"; a
// different name here silently turns every portal resend into an ordinary
// invitation that its duplicate guard then skips.
func TestProjectContactInvitedPayload_ResendWireName(t *testing.T) {
	raw, err := json.Marshal(ProjectContactInvitedPayload{Resend: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"isResend":true`) {
		t.Errorf("payload = %s, want the resend marker as \"isResend\"", raw)
	}

	raw, err = json.Marshal(ProjectContactInvitedPayload{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "esend") {
		t.Errorf("payload = %s, an ordinary invitation must omit the resend marker", raw)
	}
}
