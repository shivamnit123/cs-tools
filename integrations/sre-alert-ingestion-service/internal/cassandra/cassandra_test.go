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

import "testing"

func TestFormatID(t *testing.T) {
	cases := map[int64]string{1: "ALT000000001", 123: "ALT000000123", 999999999: "ALT999999999"}
	for seq, want := range cases {
		if got := FormatID(seq); got != want {
			t.Errorf("FormatID(%d) = %s, want %s", seq, got, want)
		}
	}
}

func TestConfigFromEnv_UsernameDefaultsToAccountName(t *testing.T) {
	t.Setenv("CASSANDRA_CONTACT_POINT", "myaccount.cassandra.cosmos.azure.com")
	t.Setenv("CASSANDRA_KEYSPACE", "ks")
	t.Setenv("CASSANDRA_KEY", "secret")
	t.Setenv("CASSANDRA_USERNAME", "")
	t.Setenv("CASSANDRA_PORT", "")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Username != "myaccount" || cfg.Port != 10350 {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestConfigFromEnv_RequiresKey(t *testing.T) {
	t.Setenv("CASSANDRA_CONTACT_POINT", "a.b")
	t.Setenv("CASSANDRA_KEYSPACE", "ks")
	t.Setenv("CASSANDRA_KEY", "")
	if _, err := ConfigFromEnv(); err == nil {
		t.Error("missing CASSANDRA_KEY should fail")
	}
}
