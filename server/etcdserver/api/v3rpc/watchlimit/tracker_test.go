// Copyright 2026 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package watchlimit

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"go.etcd.io/etcd/server/v3/config"
)

func TestWatchResourceTrackerRegister(t *testing.T) {
	type op struct {
		clientID string
		register bool // false => unregister
		wantErr  error
	}
	type want struct {
		total   uint64
		clients map[string]uint
	}
	tests := []struct {
		name string
		cfg  config.WatchLimitConfig
		ops  []op
		want want
	}{
		{
			name: "register increments per-client and total",
			ops: []op{
				{clientID: "a", register: true},
				{clientID: "a", register: true},
				{clientID: "b", register: true},
			},
			want: want{total: 3, clients: map[string]uint{"a": 2, "b": 1}},
		},
		{
			name: "unregister decrements and removes entry at zero",
			ops: []op{
				{clientID: "a", register: true},
				{clientID: "a", register: true},
				{clientID: "b", register: true},
				{clientID: "a", register: false},
				{clientID: "a", register: false},
			},
			want: want{total: 1, clients: map[string]uint{"b": 1}},
		},
		{
			name: "per-client limit returns ErrGRPCTooManyWatchesPerClient",
			cfg:  config.WatchLimitConfig{MaxWatchesPerClient: 2},
			ops: []op{
				{clientID: "a", register: true},
				{clientID: "a", register: true},
				{clientID: "a", register: true, wantErr: rpctypes.ErrGRPCTooManyWatchesPerClient},
				// A different client is unaffected by a's per-client cap.
				{clientID: "b", register: true},
			},
			want: want{total: 3, clients: map[string]uint{"a": 2, "b": 1}},
		},
		{
			name: "global limit returns ErrGRPCTooManyWatches",
			cfg:  config.WatchLimitConfig{MaxWatchesTotal: 2},
			ops: []op{
				{clientID: "a", register: true},
				{clientID: "b", register: true},
				{clientID: "c", register: true, wantErr: rpctypes.ErrGRPCTooManyWatches},
			},
			want: want{total: 2, clients: map[string]uint{"a": 1, "b": 1}},
		},
		{
			name: "unregister unknown client is a safe no-op",
			ops: []op{
				{clientID: "ghost", register: false},
				{clientID: "ghost", register: false},
				{clientID: "a", register: true},
			},
			want: want{total: 1, clients: map[string]uint{"a": 1}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			tr := NewWatchResourceTracker(&cfg)

			for i, o := range tt.ops {
				if o.register {
					err := tr.Register(o.clientID)
					if !errors.Is(err, o.wantErr) {
						t.Fatalf("op %d: Register(%q) error = %v, want %v", i, o.clientID, err, o.wantErr)
					}
				} else {
					tr.Unregister(o.clientID)
				}
			}

			if got := tr.Total(); got != tt.want.total {
				t.Errorf("Total() = %d, want %d", got, tt.want.total)
			}
			for id, wantN := range tt.want.clients {
				if got := tr.ClientCount(id); got != wantN {
					t.Errorf("ClientCount(%q) = %d, want %d", id, got, wantN)
				}
			}
			// Verify map entries that should have been removed are gone.
			tr.mu.RLock()
			for id := range tr.clients {
				if _, ok := tt.want.clients[id]; !ok {
					t.Errorf("clients map still contains %q, want removed", id)
				}
			}
			if len(tr.clients) != len(tt.want.clients) {
				t.Errorf("len(clients) = %d, want %d", len(tr.clients), len(tt.want.clients))
			}
			tr.mu.RUnlock()
		})
	}
}

func TestWatchResourceTrackerConcurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency test in short mode")
	}

	const (
		goroutines = 10
		iters      = 1000
	)
	tr := NewWatchResourceTracker(&config.WatchLimitConfig{})

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			clientID := fmt.Sprintf("client-%d", id)
			for i := 0; i < iters; i++ {
				if err := tr.Register(clientID); err != nil {
					t.Errorf("Register(%q): unexpected error %v", clientID, err)
					return
				}
				// Interleave reads to exercise the RWMutex read path.
				_ = tr.Total()
				_ = tr.ClientCount(clientID)
				tr.Unregister(clientID)
			}
		}(g)
	}
	wg.Wait()

	if got := tr.Total(); got != 0 {
		t.Fatalf("Total() after symmetric register/unregister = %d, want 0", got)
	}
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	if len(tr.clients) != 0 {
		t.Fatalf("clients map after symmetric register/unregister has %d entries, want 0", len(tr.clients))
	}
}
