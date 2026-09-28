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
	"net/http/httptest"
	"testing"
)

func TestNew_None(t *testing.T) {
	a, err := New("none")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := a.Authenticate(httptest.NewRequest("POST", "/", nil), "aws"); err != nil {
		t.Errorf("none should accept every request, got %v", err)
	}
}

func TestNew_UnknownModeFails(t *testing.T) {
	if _, err := New("basic"); err == nil {
		t.Error("unknown mode should fail at startup")
	}
}
