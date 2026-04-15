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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLimiterUnlimited(t *testing.T) {
	l := newGroupLimiter(Config{RateLimitReads: 0, RateLimitWrites: 0, BurstFactor: 1.5})
	for i := 0; i < 10000; i++ {
		ok, _ := l.allow(GroupRead)
		require.True(t, ok)
		ok, _ = l.allow(GroupWrite)
		require.True(t, ok)
	}
	assert.Equal(t, float64(0), l.utilization(GroupRead))
	assert.Equal(t, float64(0), l.utilization(GroupWrite))
}

func TestLimiterHealthAndStreamNeverLimited(t *testing.T) {
	l := newGroupLimiter(Config{RateLimitReads: 1, RateLimitWrites: 1, BurstFactor: 1.0})
	for i := 0; i < 1000; i++ {
		ok, _ := l.allow(GroupHealth)
		require.True(t, ok)
		ok, _ = l.allow(GroupStream)
		require.True(t, ok)
	}
}

func TestLimiterBurstThenReject(t *testing.T) {
	// 100 qps, burst factor 2.0 => burst 200.
	l := newGroupLimiter(Config{RateLimitWrites: 100, BurstFactor: 2.0})

	allowed := 0
	for i := 0; i < 300; i++ {
		ok, _ := l.allow(GroupWrite)
		if ok {
			allowed++
		}
	}
	// Should admit ~burst, perhaps a token or two more depending on timing.
	assert.GreaterOrEqual(t, allowed, 200)
	assert.Less(t, allowed, 210)

	// Next call must be rejected with a non-zero retry hint.
	ok, retry := l.allow(GroupWrite)
	require.False(t, ok)
	assert.Greater(t, retry, time.Duration(0))
	assert.LessOrEqual(t, retry, 20*time.Millisecond, "retry hint should be ~1/qps")
}

func TestLimiterIndependentBuckets(t *testing.T) {
	l := newGroupLimiter(Config{RateLimitReads: 1, RateLimitWrites: 0, BurstFactor: 1.0})

	// Drain reads.
	ok, _ := l.allow(GroupRead)
	require.True(t, ok)
	ok, _ = l.allow(GroupRead)
	require.False(t, ok)

	// Writes are unlimited and unaffected.
	for i := 0; i < 100; i++ {
		ok, _ = l.allow(GroupWrite)
		require.True(t, ok)
	}
}

func TestLimiterRefill(t *testing.T) {
	l := newGroupLimiter(Config{RateLimitWrites: 1000, BurstFactor: 1.0})

	// Drain.
	for {
		ok, _ := l.allow(GroupWrite)
		if !ok {
			break
		}
	}
	ok, _ := l.allow(GroupWrite)
	require.False(t, ok)

	// Wait for at least one token to refill (1ms at 1000 qps; use 20ms for safety).
	time.Sleep(20 * time.Millisecond)

	ok, _ = l.allow(GroupWrite)
	require.True(t, ok, "expected at least one token to refill")
}

func TestLimiterUtilization(t *testing.T) {
	l := newGroupLimiter(Config{RateLimitWrites: 100, BurstFactor: 1.0})

	u := l.utilization(GroupWrite)
	assert.InDelta(t, 0.0, u, 0.05)

	for i := 0; i < 50; i++ {
		l.allow(GroupWrite)
	}
	u = l.utilization(GroupWrite)
	assert.Greater(t, u, 0.3)
	assert.Less(t, u, 0.7)

	for i := 0; i < 100; i++ {
		l.allow(GroupWrite)
	}
	u = l.utilization(GroupWrite)
	assert.InDelta(t, 1.0, u, 0.05)
}

func TestNewBucketBurstSize(t *testing.T) {
	tests := []struct {
		qps       uint
		factor    float64
		wantBurst int
	}{
		{0, 1.5, 0},
		{10, 1.5, 15},
		{10, 1.0, 10},
		{1, 1.0, 1},
		{3, 1.5, 5}, // ceil(4.5)
	}
	for _, tt := range tests {
		b := newBucket(tt.qps, tt.factor)
		if tt.qps == 0 {
			continue
		}
		assert.Equal(t, tt.wantBurst, b.Burst(), "qps=%d factor=%v", tt.qps, tt.factor)
	}
}
