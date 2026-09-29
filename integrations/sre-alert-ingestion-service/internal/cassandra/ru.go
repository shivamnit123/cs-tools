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
	"context"
	"encoding/binary"
	"math"
	"sync"

	"github.com/gocql/gocql"
)

// RUMeter sums the request charge Cosmos DB reports for calls made with its context.
type RUMeter struct {
	mu    sync.Mutex
	total float64
	seen  bool
}

// Total returns the summed charge, and whether any call reported one.
func (m *RUMeter) Total() (float64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.total, m.seen
}

type ruMeterKey struct{}

// WithRUMeter makes Store calls under ctx add their request charge to m.
func WithRUMeter(ctx context.Context, m *RUMeter) context.Context {
	return context.WithValue(ctx, ruMeterKey{}, m)
}

// recordRU adds the "RequestCharge" payload of iter, if any, to ctx's meter.
func recordRU(ctx context.Context, iter *gocql.Iter) {
	m, _ := ctx.Value(ruMeterKey{}).(*RUMeter)
	if m == nil {
		return
	}
	b := iter.GetCustomPayload()["RequestCharge"]
	if len(b) != 8 {
		return
	}
	m.mu.Lock()
	m.total += math.Float64frombits(binary.BigEndian.Uint64(b))
	m.seen = true
	m.mu.Unlock()
}
