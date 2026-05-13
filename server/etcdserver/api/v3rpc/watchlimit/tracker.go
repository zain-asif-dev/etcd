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

// Package watchlimit implements per-client and server-wide resource
// tracking and admission control for the v3 Watch API.
package watchlimit

import (
	"sync"
	"sync/atomic"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"go.etcd.io/etcd/server/v3/config"
)

// WatchResourceTracker tracks the number of active watches per client
// identity and the server-wide total, enforcing the limits configured in
// config.WatchLimitConfig.
//
// Per-client counts are protected by an RWMutex; the server-wide total is
// maintained as an atomic so that read-mostly callers (metrics, degradation
// loop) can sample it without taking the lock.
type WatchResourceTracker struct {
	cfg *config.WatchLimitConfig

	// total is the server-wide number of active watches.
	total atomic.Uint64

	mu sync.RWMutex
	// clients maps client identity (auth user or peer IP) to its active
	// watch count. Entries are removed when the count returns to zero.
	clients map[string]uint
}

// NewWatchResourceTracker returns a tracker that enforces the given limits.
// A nil cfg is treated as a zero-value config, i.e. all limits disabled.
func NewWatchResourceTracker(cfg *config.WatchLimitConfig) *WatchResourceTracker {
	if cfg == nil {
		cfg = &config.WatchLimitConfig{}
	}
	return &WatchResourceTracker{
		cfg:     cfg,
		clients: make(map[string]uint),
	}
}

// Register records a new watch for clientID. It returns
// rpctypes.ErrGRPCTooManyWatchesPerClient if the per-client limit would be
// exceeded, or rpctypes.ErrGRPCTooManyWatches if the server-wide limit would
// be exceeded. On error no counters are modified.
//
// When both the per-client and server-wide limits are disabled (zero) this
// is effectively a counter increment under a write lock.
func (t *WatchResourceTracker) Register(clientID string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if max := t.cfg.MaxWatchesPerClient; max > 0 && t.clients[clientID] >= max {
		WatchRegisterRejectionsTotal.WithLabelValues(clientID, RejectionReasonPerClientLimit).Inc()
		return rpctypes.ErrGRPCTooManyWatchesPerClient
	}
	if max := t.cfg.MaxWatchesTotal; max > 0 && t.total.Load() >= uint64(max) {
		WatchRegisterRejectionsTotal.WithLabelValues(clientID, RejectionReasonGlobalLimit).Inc()
		return rpctypes.ErrGRPCTooManyWatches
	}

	t.clients[clientID]++
	t.total.Add(1)
	ActiveWatchesTotal.Inc()
	ActiveWatchesPerClient.WithLabelValues(clientID).Inc()
	return nil
}

// Unregister records the removal of a watch for clientID. It is safe to call
// even if Register was never called for clientID; underflow is guarded.
func (t *WatchResourceTracker) Unregister(clientID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	c, ok := t.clients[clientID]
	if !ok {
		// Unknown client; nothing to release. Leave server-wide total
		// untouched so that per-client and global accounting stay in step.
		return
	}
	if c <= 1 {
		delete(t.clients, clientID)
	} else {
		t.clients[clientID] = c - 1
	}
	ActiveWatchesPerClient.WithLabelValues(clientID).Dec()
	// atomic.Uint64 has no checked decrement; guard against underflow so a
	// stray Unregister cannot wrap the counter.
	for {
		cur := t.total.Load()
		if cur == 0 {
			return
		}
		if t.total.CompareAndSwap(cur, cur-1) {
			ActiveWatchesTotal.Dec()
			return
		}
	}
}

// Total returns the current server-wide active watch count. Safe to call
// without holding the lock.
func (t *WatchResourceTracker) Total() uint64 {
	return t.total.Load()
}

// ClientCount returns the current active watch count for clientID.
func (t *WatchResourceTracker) ClientCount(clientID string) uint {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.clients[clientID]
}
