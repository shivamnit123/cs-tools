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

// Command mockdashboard stands in for a WSO2 cloud status dashboard so the
// cloud_status_webhooks task can be exercised end to end without posting to a
// real status page.
//
// It is a development tool and is never deployed. It exists because the
// alternative -- pointing the first run at a live dashboard -- risks putting a
// wrong event on a page customers read, and because the receiving service
// accepts a malformed body with a 200 and silently ignores it, so a bad
// payload produces no error anywhere. This prints what arrived, loudly.
//
//	go run ./cmd/mockdashboard -port 9110 -secret s3cr3t
//
// then point the task at it:
//
//	CLOUD_STATUS_ENABLED=true
//	CLOUD_STATUS_WEBHOOK_URLS='{"asgardeo":"http://127.0.0.1:9110","choreo":"http://127.0.0.1:9110"}'
//	CLOUD_STATUS_WEBHOOK_SECRETS='{"default":"s3cr3t"}'
//
// Plain http is fine here and nowhere else: internal/httpsec allows it for
// loopback addresses only.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"
)

func main() {
	port := flag.Int("port", 9110, "port to listen on")
	secret := flag.String("secret", "", "expected X-Webhook-Signature; empty accepts any")
	status := flag.Int("status", 200, "HTTP status to return, for exercising the retry path")
	flag.Parse()

	var count int
	http.HandleFunc("/api/v1/webhook", func(w http.ResponseWriter, r *http.Request) {
		count++
		raw, _ := io.ReadAll(r.Body)
		fmt.Printf("\n─── request %d  %s ───────────────────────────────\n", count, time.Now().Format(time.RFC3339))
		fmt.Printf("  %s %s\n", r.Method, r.URL.Path)

		sig := r.Header.Get("X-Webhook-Signature")
		switch {
		case *secret == "":
			fmt.Printf("  X-Webhook-Signature: %q (not checked)\n", sig)
		case sig == *secret:
			fmt.Printf("  X-Webhook-Signature: OK\n")
		default:
			fmt.Printf("  X-Webhook-Signature: *** MISMATCH *** got %q, want %q\n", sig, *secret)
		}

		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			fmt.Printf("  BODY IS NOT JSON: %s\n", string(raw))
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Field-by-field, sorted, with the expected set called out. The whole
		// point of this tool is noticing a field that is missing, misspelled,
		// or unexpectedly present -- which is exactly what a real dashboard
		// would accept silently.
		keys := make([]string, 0, len(body))
		for k := range body {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Printf("  body (%d fields):\n", len(body))
		for _, k := range keys {
			marker := "   "
			if k != "event" && k != "timestamp" && k != "cloud" {
				marker = " ??"
			}
			fmt.Printf("  %s %-12s %v\n", marker, k, body[k])
		}
		for _, want := range []string{"event", "timestamp", "cloud"} {
			if _, ok := body[want]; !ok {
				fmt.Printf("    !! MISSING %s\n", want)
			}
		}

		w.WriteHeader(*status)
	})

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	fmt.Printf("mock cloud status dashboard on http://%s/api/v1/webhook (returning %d)\n", addr, *status)
	// #nosec G114 -- a loopback-only development tool, never deployed.
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
