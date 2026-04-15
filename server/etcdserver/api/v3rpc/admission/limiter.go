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

package admission

import (
	"math"
	"time"

	"golang.org/x/time/rate"
)

// groupLimiter applies independent token buckets to each endpoint group.
// Health endpoints are never limited.
type groupLimiter struct {
	read  *rate.Limiter
	write *rate.Limiter
}

func newGroupLimiter(cfg Config) *groupLimiter {
	return &groupLimiter{
		read:  newBucket(cfg.RateLimitReads, cfg.BurstFactor),
		write: newBucket(cfg.RateLimitWrites, cfg.BurstFactor),
	}
}

func newBucket(qps uint, burstFactor float64) *rate.Limiter {
	if qps == 0 {
		return rate.NewLimiter(rate.Inf, 0)
	}
	burst := int(math.Ceil(float64(qps) * burstFactor))
	if burst < 1 {
		burst = 1
	}
	return rate.NewLimiter(rate.Limit(qps), burst)
}

// allow performs a non-blocking token-bucket check for the given group. It
// returns true if the request may proceed. When it returns false,
// retryAfter holds the duration the client should wait before retrying.
func (l *groupLimiter) allow(g EndpointGroup) (ok bool, retryAfter time.Duration) {
	lim := l.bucket(g)
	if lim == nil || lim.Limit() == rate.Inf {
		return true, 0
	}
	if lim.Allow() {
		return true, 0
	}
	// Compute a retry hint without consuming a token.
	r := lim.Reserve()
	d := r.Delay()
	r.Cancel()
	return false, d
}

// tokens returns the current number of available tokens for the given group,
// or +Inf if the group is unlimited.
func (l *groupLimiter) tokens(g EndpointGroup) float64 {
	lim := l.bucket(g)
	if lim == nil || lim.Limit() == rate.Inf {
		return math.Inf(1)
	}
	return lim.Tokens()
}

// utilization returns the fraction [0,1] of the burst capacity currently
// consumed for the given group, or 0 if the group is unlimited.
func (l *groupLimiter) utilization(g EndpointGroup) float64 {
	lim := l.bucket(g)
	if lim == nil || lim.Limit() == rate.Inf {
		return 0
	}
	b := float64(lim.Burst())
	if b == 0 {
		return 0
	}
	u := 1 - lim.Tokens()/b
	if u < 0 {
		return 0
	}
	if u > 1 {
		return 1
	}
	return u
}

func (l *groupLimiter) bucket(g EndpointGroup) *rate.Limiter {
	switch g {
	case GroupRead:
		return l.read
	case GroupWrite:
		return l.write
	default:
		return nil
	}
}
