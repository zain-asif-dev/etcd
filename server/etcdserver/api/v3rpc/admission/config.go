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

// Package admission implements server-side request admission control for the
// etcd v3 gRPC API. It provides per-endpoint-group token-bucket rate limiting,
// priority-based load shedding driven by in-flight concurrency and Raft apply
// lag, and emits ResourceExhausted gRPC errors with retry-after metadata so
// clients can back off intelligently.
package admission

import (
	"fmt"
	"math"
)

const (
	// DefaultBurstFactor is the default multiplier applied to a rate limit to
	// derive the token-bucket burst size.
	DefaultBurstFactor = 1.5

	// DefaultOverloadThreshold is the default fraction of MaxConcurrentStreams
	// at which lower-priority requests start being shed.
	DefaultOverloadThreshold = 0.8

	// applyLagShedWrites is the committed-applied index gap above which write
	// requests are shed. It mirrors maxGapBetweenApplyAndCommitIndex in
	// server/etcdserver so admission control engages before the Raft layer's
	// own ErrTooManyRequests fires.
	applyLagShedWrites = 5000

	// applyLagShedReads is the committed-applied index gap above which read
	// requests are also shed (writes are already shed at this point).
	applyLagShedReads = 7500

	// overloadRetryAfterMs is the retry hint returned when a request is shed
	// due to overload (as opposed to rate limiting, which computes a precise
	// hint from the token bucket).
	overloadRetryAfterMs = 1000
)

// Config holds admission-control tuning parameters. A zero-value Config
// disables all limiting (every request is admitted).
type Config struct {
	// Enabled gates the entire admission controller. When false the
	// interceptors are not registered and there is zero per-request overhead.
	Enabled bool

	// DryRun, when true, evaluates rate-limiting and scheduling decisions
	// normally but never actually rejects a request. Would-be rejections are
	// instead recorded in the admission_dry_run_rejected_total metric.
	DryRun bool

	// RateLimitReads is the maximum sustained read requests per second
	// (Range, read-only Txn, lease queries). 0 means unlimited.
	RateLimitReads uint

	// RateLimitWrites is the maximum sustained write requests per second
	// (Put, DeleteRange, mutating Txn, LeaseGrant/Revoke, etc). 0 means
	// unlimited.
	RateLimitWrites uint

	// BurstFactor is multiplied by the per-group rate limit to derive the
	// token-bucket burst size. Must be >= 1.0.
	BurstFactor float64

	// OverloadThreshold is the fraction (0,1] of MaxConcurrentStreams at which
	// the scheduler begins shedding lower-priority requests. It is also used
	// to derive the maximum concurrent stream-RPC count.
	OverloadThreshold float64

	// MaxConcurrentStreams is the gRPC server's per-connection stream limit.
	// It is used together with OverloadThreshold to compute the in-flight
	// shedding threshold and the stream-RPC concurrency cap.
	MaxConcurrentStreams uint32
}

// Validate returns an error if the configuration is internally inconsistent.
func (c *Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.BurstFactor < 1.0 {
		return fmt.Errorf("admission: rate-limit-burst-factor must be >= 1.0, got %v", c.BurstFactor)
	}
	if c.OverloadThreshold <= 0 || c.OverloadThreshold > 1.0 {
		return fmt.Errorf("admission: overload-threshold must be in (0, 1], got %v", c.OverloadThreshold)
	}
	return nil
}

// inflightThreshold returns the number of concurrently executing unary RPCs
// above which the scheduler begins shedding load. Returns 0 (no threshold)
// when MaxConcurrentStreams is effectively unbounded.
func (c *Config) inflightThreshold() int64 {
	if c.MaxConcurrentStreams == 0 || c.MaxConcurrentStreams == math.MaxUint32 {
		return 0
	}
	t := int64(float64(c.MaxConcurrentStreams) * c.OverloadThreshold)
	if t < 1 {
		t = 1
	}
	return t
}

// streamLimit returns the maximum number of concurrent stream RPCs (Watch,
// LeaseKeepAlive, Snapshot) the controller will admit. Returns 0 (no limit)
// when MaxConcurrentStreams is effectively unbounded.
func (c *Config) streamLimit() int64 {
	return c.inflightThreshold()
}
