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

// availdiff compares the Postgres-backed /availabilities and
// /availability-history against the live ServiceNow-backed dashboard API.
//
// Dev-only. It drives the real repository and service against the real dev
// database, then fetches the same cloud from the dashboard backend that
// ServiceNow still serves, and reports every field that differs.
//
//	DSN='postgres://…' LIVE='https://app-common-…azurewebsites.net' \
//	go run ./cmd/availdiff
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/wso2-open-operations/cs-tools/entity-service/internal/repository"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/service"
)

var clouds = []string{"asgardeo", "choreo", "choreo-eu", "bijira", "devant", "moesif", "agent-manager"}

func main() {
	pool, err := pgxpool.New(context.Background(), os.Getenv("DSN"))
	if err != nil {
		panic(err)
	}
	defer pool.Close()
	svc := service.NewCloudStatusDashboardService(repository.NewCloudStatusDashboardRepository(pool))
	ctx := context.Background()
	live := os.Getenv("LIVE")

	total, diffs := 0, 0

	fmt.Println("################ /availabilities ################")
	for _, c := range clouds {
		got, err := svc.Availabilities(ctx, c)
		if err != nil {
			fmt.Printf("%-14s PORT ERROR %v\n", c, err)
			diffs++
			continue
		}
		want, err := fetch(live + "/api/v1/availabilities?cloud=" + c)
		if err != nil {
			fmt.Printf("%-14s LIVE ERROR %v\n", c, err)
			diffs++
			continue
		}
		n, d := compare(c, normalise(got), want)
		total += n
		diffs += d
	}

	fmt.Println("\n################ /availability-history ################")
	for _, c := range clouds {
		got, err := svc.AvailabilityHistory(ctx, c)
		if err != nil {
			fmt.Printf("%-14s PORT ERROR %v\n", c, err)
			diffs++
			continue
		}
		want, err := fetch(live + "/api/v1/history/availabilities?cloud=" + c)
		if err != nil {
			fmt.Printf("%-14s LIVE ERROR %v\n", c, err)
			diffs++
			continue
		}
		n, d := compareHistory(c, normalise(got), want)
		total += n
		diffs += d
	}

	fmt.Printf("\n==== %d values compared, %d differences ====\n", total, diffs)
	if diffs > 0 {
		os.Exit(1)
	}
}

// normalise round-trips through JSON so the comparison sees exactly the bytes
// the handler would write -- including AvailabilityFigure's custom marshal.
func normalise(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}

func fetch(url string) (map[string]any, error) {
	c := &http.Client{Timeout: 90 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out map[string]any
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

// compare walks the availabilities shape: region -> [ {availability, duration} ].
func compare(cloud string, got, want map[string]any) (int, int) {
	n, d := 0, 0
	for _, region := range union(got, want) {
		gw, _ := got[region].([]any)
		ww, _ := want[region].([]any)
		if gw == nil || ww == nil {
			fmt.Printf("  %-14s %-14s REGION MISSING  port=%v live=%v\n",
				cloud, region, gw != nil, ww != nil)
			d++
			continue
		}
		if len(gw) != len(ww) {
			fmt.Printf("  %-14s %-14s WINDOW COUNT port=%d live=%d\n", cloud, region, len(gw), len(ww))
			d++
			continue
		}
		for i := range gw {
			g, _ := gw[i].(map[string]any)
			w, _ := ww[i].(map[string]any)
			n++
			// json.Marshal of the decoded value compares type AND digits:
			// "100.000" and 100 do not collide.
			ga, _ := json.Marshal(g["availability"])
			wa, _ := json.Marshal(w["availability"])
			if string(ga) != string(wa) || g["duration"] != w["duration"] {
				fmt.Printf("  %-14s %-14s %-16v port=%s live=%s\n",
					cloud, region, w["duration"], ga, wa)
				d++
			}
		}
	}
	if d == 0 {
		fmt.Printf("  %-14s OK (%d values)\n", cloud, n)
	}
	return n, d
}

// compareHistory walks region -> [group{display_name, subgroups[]}].
//
// It separates two very different things:
//
//   - VALUE MISMATCHES, on a date both sides publish. These are real and
//     must be zero.
//   - SET differences. These are expected and bounded: ServiceNow's daily
//     query carries no ORDER BY, so its uniqueAvaialbility.slice(-90) drops
//     an ARBITRARY day whenever a monitor has more than 90, while the port
//     drops the oldest. Neither the order nor the victim is reproducible;
//     what is checkable is that the difference stays confined to the cap.
func compareHistory(cloud string, got, want map[string]any) (int, int) {
	n, d := 0, 0
	capOnly, capDates := 0, 0
	for _, region := range union(got, want) {
		gm := flattenHistory(got[region])
		wm := flattenHistory(want[region])
		for _, key := range unionKeys(gm, wm) {
			g, gok := gm[key]
			w, wok := wm[key]
			if !gok || !wok {
				fmt.Printf("  %-14s %-12s %-46s MONITOR MISSING port=%v live=%v\n",
					cloud, region, key, gok, wok)
				d++
				continue
			}
			n++

			// Values, on every date both sides carry.
			var mismatch []string
			for date, gv := range g {
				if wv, ok := w[date]; ok && gv != wv {
					mismatch = append(mismatch, fmt.Sprintf("%s port=%v live=%v", date, gv, wv))
				}
			}
			// Set difference.
			var onlyPort, onlyLive []string
			for date := range g {
				if _, ok := w[date]; !ok {
					onlyPort = append(onlyPort, date)
				}
			}
			for date := range w {
				if _, ok := g[date]; !ok {
					onlyLive = append(onlyLive, date)
				}
			}
			sort.Strings(mismatch)
			sort.Strings(onlyPort)
			sort.Strings(onlyLive)

			if len(mismatch) > 0 {
				show := mismatch
				if len(show) > 4 {
					show = show[:4]
				}
				fmt.Printf("  %-14s %-12s %-46s *** %d VALUE MISMATCH: %v\n",
					cloud, region, key, len(mismatch), show)
				d++
				continue
			}

			// A pure cap artefact: both sides hold exactly the cap, and the
			// sets differ only by how many each dropped.
			atCap := len(g) == 90 && len(w) == 90
			if len(onlyPort) == 0 && len(onlyLive) == 0 {
				continue
			}
			if atCap && len(onlyPort) == len(onlyLive) && len(onlyPort) <= 4 {
				capOnly++
				capDates += len(onlyPort)
				continue
			}
			fmt.Printf("  %-14s %-12s %-46s SET port=%d live=%d onlyPort=%v onlyLive=%v\n",
				cloud, region, key, len(g), len(w), onlyPort, onlyLive)
			d++
		}
	}
	if d == 0 {
		fmt.Printf("  %-14s OK (%d monitors", cloud, n)
		if capOnly > 0 {
			fmt.Printf("; %d differ only by the slice(-90) victim, %d date(s) total",
				capOnly, capDates)
		}
		fmt.Println(")")
	}
	return n, d
}

// flattenHistory turns a region's groups into "group / monitor" -> date -> value.
func flattenHistory(v any) map[string]map[string]float64 {
	out := map[string]map[string]float64{}
	groups, _ := v.([]any)
	for _, gi := range groups {
		g, _ := gi.(map[string]any)
		gname, _ := g["display_name"].(string)
		subs, _ := g["subgroups"].([]any)
		for _, si := range subs {
			s, _ := si.(map[string]any)
			sname, _ := s["display_name"].(string)
			pts := map[string]float64{}
			hist, _ := s["history"].([]any)
			for _, pi := range hist {
				p, _ := pi.(map[string]any)
				date, _ := p["date"].(string)
				val, _ := p["availability"].(float64)
				pts[date] = val
			}
			out[gname+" / "+sname] = pts
		}
	}
	return out
}

func union(a, b map[string]any) []string {
	seen := map[string]bool{}
	var out []string
	for k := range a {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for k := range b {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func unionKeys(a, b map[string]map[string]float64) []string {
	seen := map[string]bool{}
	var out []string
	for k := range a {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for k := range b {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
