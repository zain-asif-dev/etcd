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
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeClock lets tests control the bucket's notion of "now".
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(0, 0)} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestTokenBucketTake(t *testing.T) {
	tests := []struct {
		name     string
		rate     float64
		capacity float64
		takes    []float64
		wantOK   []bool
	}{
		{
			name:     "burst then reject",
			rate:     10,
			capacity: 3,
			takes:    []float64{1, 1, 1, 1},
			wantOK:   []bool{true, true, true, false},
		},
		{
			name:     "fractional cost",
			rate:     10,
			capacity: 1,
			takes:    []float64{0.5, 0.5, 0.5},
			wantOK:   []bool{true, true, false},
		},
		{
			name:     "cost exceeds capacity",
			rate:     10,
			capacity: 2,
			takes:    []float64{3},
			wantOK:   []bool{false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := newFakeClock()
			b := newTokenBucket(tt.rate, tt.capacity, clk.now)
			for i, cost := range tt.takes {
				ok, _ := b.take(cost)
				require.Equalf(t, tt.wantOK[i], ok, "take #%d cost=%v", i, cost)
			}
		})
	}
}

func TestTokenBucketRetryAfter(t *testing.T) {
	clk := newFakeClock()
	b := newTokenBucket(10, 1, clk.now) // 10 tokens/sec, capacity 1
	ok, _ := b.take(1)
	require.True(t, ok)
	ok, wait := b.take(1)
	require.False(t, ok)
	// deficit = 1 token at 10/sec → 100ms
	require.InDelta(t, 100*time.Millisecond, wait, float64(2*time.Millisecond))
}

func TestTokenBucketRefill(t *testing.T) {
	clk := newFakeClock()
	b := newTokenBucket(10, 10, clk.now)

	// Drain.
	for i := 0; i < 10; i++ {
		ok, _ := b.take(1)
		require.True(t, ok)
	}
	ok, _ := b.take(1)
	require.False(t, ok, "bucket should be empty")

	// Advance 500ms → 5 tokens.
	clk.advance(500 * time.Millisecond)
	require.InDelta(t, 5.0, b.remaining(), 0.001)

	// Advance far past capacity → capped at 10.
	clk.advance(time.Hour)
	require.InDelta(t, 10.0, b.remaining(), 0.001)
}

func TestTokenBucketZeroBurst(t *testing.T) {
	clk := newFakeClock()
	b := newTokenBucket(5, 0, clk.now)
	ok, wait := b.take(1)
	require.False(t, ok, "zero-capacity bucket must reject")
	// deficit 1 at rate 5 → 200ms
	require.InDelta(t, 200*time.Millisecond, wait, float64(2*time.Millisecond))

	// Even after time passes a zero-capacity bucket holds nothing.
	clk.advance(time.Second)
	require.Equal(t, 0.0, b.remaining())
}

func TestTokenBucketZeroRateRetryAfter(t *testing.T) {
	clk := newFakeClock()
	b := newTokenBucket(0, 1, clk.now)
	ok, _ := b.take(1)
	require.True(t, ok)
	ok, wait := b.take(1)
	require.False(t, ok)
	require.Equal(t, time.Second, wait, "zero-rate bucket returns nominal 1s back-off")
}

func TestTokenBucketConcurrent(t *testing.T) {
	const (
		goroutines = 100
		perG       = 100
		capacity   = 1000
	)
	clk := newFakeClock()
	b := newTokenBucket(0, capacity, clk.now) // rate 0 → no refill, exact accounting

	var allowed atomic.Int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				if ok, _ := b.take(1); ok {
					allowed.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	require.EqualValues(t, capacity, allowed.Load(),
		"exactly capacity tokens must be granted across all goroutines")
	require.Equal(t, 0.0, b.remaining())
}

// FuzzTokenBucketTake throws arbitrary (rate, burst, cost) triples at the
// bucket to ensure take/remaining never panic on any valid non-pathological
// input.
func FuzzTokenBucketTake(f *testing.F) {
	f.Add(10.0, 5.0, 1.0)
	f.Add(0.0, 0.0, 0.0)
	f.Add(1.0, 1.0, 2.0)
	f.Add(1e6, 1e6, 0.5)

	f.Fuzz(func(t *testing.T, rate, burst, cost float64) {
		// Reject pathological inputs: negatives, absurdly large values, and
		// non-finite floats which the config layer would never produce.
		for _, v := range []float64{rate, burst, cost} {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1e9 {
				t.Skip()
			}
		}

		b := newTokenBucket(rate, burst, time.Now)

		ok, wait := b.take(cost)
		if ok {
			require.Equal(t, time.Duration(0), wait)
		} else {
			require.GreaterOrEqual(t, wait, time.Duration(0),
				"retry-after must be non-negative (rate=%v burst=%v cost=%v)", rate, burst, cost)
		}

		rem := b.remaining()
		require.Falsef(t, math.IsNaN(rem), "remaining() returned NaN (rate=%v burst=%v cost=%v)", rate, burst, cost)
		require.GreaterOrEqualf(t, rem, 0.0, "remaining() must be non-negative (rate=%v burst=%v cost=%v)", rate, burst, cost)
		require.LessOrEqualf(t, rem, burst, "remaining() must not exceed capacity (rate=%v burst=%v cost=%v)", rate, burst, cost)
	})
}
