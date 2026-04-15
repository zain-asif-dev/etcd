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
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"go.uber.org/zap/zaptest/observer"
)

const (
	mPut    = "/etcdserverpb.KV/Put"
	mRange  = "/etcdserverpb.KV/Range"
	mHealth = "/grpc.health.v1.Health/Check"
)

func newTestLimiter(t *testing.T, cfg Config, clk *fakeClock) *Limiter {
	if clk == nil {
		clk = newFakeClock()
	}
	return newLimiter(cfg, zaptest.NewLogger(t), clk.now)
}

func TestLimiterDisabled(t *testing.T) {
	l := newTestLimiter(t, Config{Enabled: false}, nil)
	for i := 0; i < 1000; i++ {
		ok, _, _ := l.Allow(mPut, "1.2.3.4:5")
		require.True(t, ok)
	}
}

func TestLimiterExemptMethods(t *testing.T) {
	l := newTestLimiter(t, Config{
		Enabled: true, RequestsPerSecond: 1, BurstSize: 1,
		PerClientRPS: 1, MaxTrackedClients: 10, ReadWriteRatio: 1,
	}, nil)
	// Drain the only token with a write.
	ok, _, _ := l.Allow(mPut, "1.1.1.1:1")
	require.True(t, ok)
	ok, _, _ = l.Allow(mPut, "1.1.1.1:1")
	require.False(t, ok)
	// Health check is exempt and must always pass.
	for i := 0; i < 10; i++ {
		ok, _, _ = l.Allow(mHealth, "1.1.1.1:1")
		require.True(t, ok, "health check should never be rate limited")
	}
}

func TestLimiterReadWriteRatio(t *testing.T) {
	// capacity=2, ratio=2 → write costs 1, read costs 0.5.
	cfg := Config{
		Enabled: true, RequestsPerSecond: 100, BurstSize: 2,
		PerClientRPS: 100, MaxTrackedClients: 10, ReadWriteRatio: 2.0,
	}

	clk := newFakeClock()
	lw := newTestLimiter(t, cfg, clk)
	// Two writes drain the bucket.
	ok, _, _ := lw.Allow(mPut, "1.1.1.1:1")
	require.True(t, ok)
	ok, _, _ = lw.Allow(mPut, "1.1.1.1:1")
	require.True(t, ok)
	ok, _, _ = lw.Allow(mPut, "1.1.1.1:1")
	require.False(t, ok, "third write should be rejected")

	clk2 := newFakeClock()
	lr := newTestLimiter(t, cfg, clk2)
	// Four reads (cost 0.5 each) fit in capacity 2.
	for i := 0; i < 4; i++ {
		ok, _, _ = lr.Allow(mRange, "1.1.1.1:1")
		require.Truef(t, ok, "read #%d should be allowed", i)
	}
	ok, _, _ = lr.Allow(mRange, "1.1.1.1:1")
	require.False(t, ok, "fifth read should be rejected")
}

func TestLimiterPerClientIsolation(t *testing.T) {
	cfg := Config{
		Enabled: true, RequestsPerSecond: 1000, BurstSize: 1000,
		PerClientRPS: 2, MaxTrackedClients: 10, ReadWriteRatio: 1,
	}
	l := newTestLimiter(t, cfg, nil)

	// Exhaust client A.
	ok, _, _ := l.Allow(mPut, "10.0.0.1:100")
	require.True(t, ok)
	ok, _, _ = l.Allow(mPut, "10.0.0.1:200") // different port, same IP
	require.True(t, ok)
	ok, _, reason := l.Allow(mPut, "10.0.0.1:300")
	require.False(t, ok)
	require.Equal(t, "per-client", reason)

	// Client B unaffected.
	ok, _, _ = l.Allow(mPut, "10.0.0.2:100")
	require.True(t, ok)
}

func TestLimiterGlobalExhaustion(t *testing.T) {
	cfg := Config{
		Enabled: true, RequestsPerSecond: 100, BurstSize: 3,
		PerClientRPS: 100, MaxTrackedClients: 10, ReadWriteRatio: 1,
	}
	l := newTestLimiter(t, cfg, nil)

	// Three different clients each well under their per-client limit.
	for i := 0; i < 3; i++ {
		ok, _, _ := l.Allow(mPut, fmt.Sprintf("10.0.0.%d:1", i))
		require.True(t, ok)
	}
	ok, _, reason := l.Allow(mPut, "10.0.0.99:1")
	require.False(t, ok)
	require.Equal(t, "global", reason)
}

func TestLimiterRecovery(t *testing.T) {
	clk := newFakeClock()
	cfg := Config{
		Enabled: true, RequestsPerSecond: 10, BurstSize: 1,
		PerClientRPS: 10, MaxTrackedClients: 10, ReadWriteRatio: 1,
	}
	l := newTestLimiter(t, cfg, clk)

	ok, _, _ := l.Allow(mPut, "1.1.1.1:1")
	require.True(t, ok)
	ok, wait, _ := l.Allow(mPut, "1.1.1.1:1")
	require.False(t, ok)
	require.Greater(t, wait, time.Duration(0))

	clk.advance(200 * time.Millisecond) // 2 tokens at rate=10
	ok, _, _ = l.Allow(mPut, "1.1.1.1:1")
	require.True(t, ok, "limiter should recover after refill")
}

func TestLimiterTrackerStaysBounded(t *testing.T) {
	const maxClients = 5
	l := newTestLimiter(t, Config{
		Enabled: true, RequestsPerSecond: 100000, BurstSize: 100000,
		PerClientRPS: 100000, MaxTrackedClients: maxClients, ReadWriteRatio: 1,
	}, nil)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, _, _ := l.Allow(mPut, fmt.Sprintf("10.0.0.%d:1234", i))
			require.True(t, ok)
		}(i)
	}
	wg.Wait()

	got := testutil.ToFloat64(rateLimitClients)
	require.LessOrEqualf(t, got, float64(maxClients),
		"etcd_server_ratelimit_current_clients gauge (%v) must not exceed MaxTrackedClients (%d)",
		got, maxClients)
}

func TestLimiterWarnLogging(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	clk := newFakeClock()
	l := newLimiter(Config{
		Enabled: true, RequestsPerSecond: 1, BurstSize: 1,
		PerClientRPS: 1, MaxTrackedClients: 10, ReadWriteRatio: 1,
	}, zap.New(core), clk.now)

	// First call drains the single token; second is rejected and must log.
	ok, _, _ := l.Allow(mPut, "10.0.0.1:1234")
	require.True(t, ok)
	ok, _, _ = l.Allow(mPut, "10.0.0.1:1234")
	require.False(t, ok)

	entries := logs.FilterMessage("rate limit exceeded").All()
	require.NotEmpty(t, entries, "expected a 'rate limit exceeded' warn log")

	e := entries[0]
	require.Equal(t, zap.WarnLevel, e.Level)
	require.Equal(t, "rate limit exceeded", e.Message)

	fields := e.ContextMap()
	require.Equal(t, "10.0.0.1:1234", fields["client-addr"])
	require.Equal(t, mPut, fields["method"])
	require.Contains(t, fields, "reason")
	require.Contains(t, fields, "retry-after-ms")
}

func TestLimiterUnknownMethodTreatedAsWrite(t *testing.T) {
	cfg := Config{
		Enabled: true, RequestsPerSecond: 100, BurstSize: 1,
		PerClientRPS: 100, MaxTrackedClients: 10, ReadWriteRatio: 4.0,
	}
	l := newTestLimiter(t, cfg, nil)
	ok, _, _ := l.Allow("/some.Unknown/Method", "1.1.1.1:1")
	require.True(t, ok)
	ok, _, _ = l.Allow("/some.Unknown/Method", "1.1.1.1:1")
	require.False(t, ok, "unknown method costs 1 token (write), so second call rejected")
}

func BenchmarkLimiterAllow(b *testing.B) {
	const addr = "10.0.0.1:1234"

	b.Run("disabled", func(b *testing.B) {
		l := New(Config{Enabled: false}, zap.NewNop())
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			l.Allow(mPut, addr)
		}
	})

	enabledCfg := Config{
		Enabled: true, RequestsPerSecond: 10000, BurstSize: 10000,
		PerClientRPS: 10000, MaxTrackedClients: 10, ReadWriteRatio: 1,
	}

	b.Run("enabled", func(b *testing.B) {
		l := New(enabledCfg, zap.NewNop())
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			l.Allow(mPut, addr)
		}
	})

	b.Run("enabled_parallel", func(b *testing.B) {
		l := New(enabledCfg, zap.NewNop())
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				l.Allow(mPut, addr)
			}
		})
	})
}

func TestHostOnly(t *testing.T) {
	require.Equal(t, "1.2.3.4", hostOnly("1.2.3.4:5678"))
	require.Equal(t, "::1", hostOnly("[::1]:8080"))
	require.Equal(t, "unix-socket", hostOnly("unix-socket"))
	require.Equal(t, "unknown", hostOnly(""))
}

func TestEndpointName(t *testing.T) {
	require.Equal(t, "Range", endpointName("/etcdserverpb.KV/Range"))
	require.Equal(t, "Put", endpointName("/etcdserverpb.KV/Put"))
	require.Equal(t, "noslash", endpointName("noslash"))
}
