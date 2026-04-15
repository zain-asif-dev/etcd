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

package ratelimit

import (
	"sync"
	"time"
)

// tokenBucket is a classic token-bucket rate limiter. Tokens are stored as a
// float64 so that fractional costs (used for the read/write ratio) are
// supported. Refill is performed lazily on each take/remaining call.
//
// All methods are safe for concurrent use.
type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	capacity float64
	rate     float64 // tokens per second
	last     time.Time
	now      func() time.Time
}

func newTokenBucket(rate, capacity float64, now func() time.Time) *tokenBucket {
	if now == nil {
		now = time.Now
	}
	return &tokenBucket{
		tokens:   capacity,
		capacity: capacity,
		rate:     rate,
		last:     now(),
		now:      now,
	}
}

// refillLocked adds tokens accrued since b.last. Caller must hold b.mu.
func (b *tokenBucket) refillLocked() {
	now := b.now()
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.last = now
	}
}

// take attempts to consume cost tokens. On success it returns (true, 0). On
// failure it returns (false, retryAfter) where retryAfter is the estimated
// duration until enough tokens will be available.
func (b *tokenBucket) take(cost float64) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.refillLocked()

	if b.tokens >= cost {
		b.tokens -= cost
		return true, 0
	}
	deficit := cost - b.tokens
	if b.rate <= 0 {
		// No refill will ever happen; advise a nominal back-off.
		return false, time.Second
	}
	wait := time.Duration(deficit / b.rate * float64(time.Second))
	if wait < time.Millisecond {
		wait = time.Millisecond
	}
	return false, wait
}

// remaining returns the current token count after a lazy refill. Intended for
// metrics; the value is a snapshot and may be stale immediately.
func (b *tokenBucket) remaining() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refillLocked()
	return b.tokens
}
