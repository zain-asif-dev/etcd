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
	"context"
	"errors"
	"math"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
)

func newTestController(t *testing.T, rs RaftStatus, cfg Config) *Controller {
	cfg.Enabled = true
	if cfg.BurstFactor == 0 {
		cfg.BurstFactor = 1.0
	}
	if cfg.OverloadThreshold == 0 {
		cfg.OverloadThreshold = 1.0
	}
	return NewController(zaptest.NewLogger(t), rs, cfg)
}

func callUnary(c *Controller, method string, req any) (handlerCalled bool, err error) {
	ic := c.UnaryInterceptor()
	_, err = ic(context.Background(), req, &grpc.UnaryServerInfo{FullMethod: method},
		func(ctx context.Context, req any) (any, error) {
			handlerCalled = true
			return "ok", nil
		})
	return handlerCalled, err
}

func TestUnaryAdmitsWithinLimit(t *testing.T) {
	c := newTestController(t, nil, Config{RateLimitWrites: 1000})
	called, err := callUnary(c, "/etcdserverpb.KV/Put", &pb.PutRequest{})
	require.NoError(t, err)
	assert.True(t, called)
}

func TestUnaryRejectsRateLimited(t *testing.T) {
	c := newTestController(t, nil, Config{RateLimitWrites: 1, BurstFactor: 1.0})

	called, err := callUnary(c, "/etcdserverpb.KV/Put", &pb.PutRequest{})
	require.NoError(t, err)
	require.True(t, called)

	called, err = callUnary(c, "/etcdserverpb.KV/Put", &pb.PutRequest{})
	require.False(t, called, "handler must not be invoked when rate limited")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.ResourceExhausted, st.Code())
	assert.True(t, errors.Is(err, rpctypes.ErrGRPCRequestRateLimited) || err.Error() == rpctypes.ErrGRPCRequestRateLimited.Error())
}

func TestUnaryDryRunAllowsRateLimited(t *testing.T) {
	c := newTestController(t, nil, Config{DryRun: true, RateLimitWrites: 1, BurstFactor: 1.0})

	before := testutil.ToFloat64(dryRunRejectedRequests.WithLabelValues(reasonRateLimited))

	// First call consumes the only token; subsequent calls would be
	// rate-limited but must still be allowed through in dry-run mode.
	for i := 0; i < 5; i++ {
		called, err := callUnary(c, "/etcdserverpb.KV/Put", &pb.PutRequest{})
		require.NoError(t, err, "dry-run must never reject")
		require.True(t, called, "handler must be invoked in dry-run mode")
	}

	after := testutil.ToFloat64(dryRunRejectedRequests.WithLabelValues(reasonRateLimited))
	assert.Equal(t, before+4, after, "expected 4 dry-run rate_limited rejections")
}

func TestUnaryHealthBypassesAllLimits(t *testing.T) {
	rs := &fakeRaftStatus{committed: applyLagShedReads * 2, applied: 0}
	c := newTestController(t, rs, Config{RateLimitWrites: 1, RateLimitReads: 1, BurstFactor: 1.0})

	for i := 0; i < 100; i++ {
		called, err := callUnary(c, "/etcdserverpb.Maintenance/Status", &pb.StatusRequest{})
		require.NoError(t, err)
		require.True(t, called)
	}
}

func TestUnaryOverloadShedsBeforeRateLimit(t *testing.T) {
	rs := &fakeRaftStatus{committed: applyLagShedWrites + 100, applied: 0}
	c := newTestController(t, rs, Config{RateLimitWrites: 1000})

	called, err := callUnary(c, "/etcdserverpb.KV/Put", &pb.PutRequest{})
	require.False(t, called)
	require.Error(t, err)
	st, _ := status.FromError(err)
	assert.Equal(t, codes.ResourceExhausted, st.Code())
	assert.Equal(t, rpctypes.ErrGRPCServerOverloaded.Error(), err.Error())

	// Reads are still admitted at this lag level.
	called, err = callUnary(c, "/etcdserverpb.KV/Range", &pb.RangeRequest{})
	require.NoError(t, err)
	require.True(t, called)
}

func TestUnaryInflightReleasedOnError(t *testing.T) {
	c := newTestController(t, nil, Config{})
	ic := c.UnaryInterceptor()
	_, err := ic(context.Background(), &pb.PutRequest{},
		&grpc.UnaryServerInfo{FullMethod: "/etcdserverpb.KV/Put"},
		func(ctx context.Context, req any) (any, error) {
			return nil, errors.New("boom")
		})
	require.Error(t, err)
	assert.EqualValues(t, 0, c.sched.inflightCount(), "inflight must be released when handler errors")
}

type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeStream) Context() context.Context { return f.ctx }

func TestStreamLimit(t *testing.T) {
	c := newTestController(t, nil, Config{MaxConcurrentStreams: 2, OverloadThreshold: 0.5}) // limit = 1
	ic := c.StreamInterceptor()

	block := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- ic(nil, &fakeStream{ctx: context.Background()},
			&grpc.StreamServerInfo{FullMethod: "/etcdserverpb.Watch/Watch"},
			func(srv any, stream grpc.ServerStream) error {
				<-block
				return nil
			})
	}()
	// Spin until the first stream is registered.
	require.Eventually(t, func() bool { return c.sched.streamCount() == 1 }, wait, tick)

	err := ic(nil, &fakeStream{ctx: context.Background()},
		&grpc.StreamServerInfo{FullMethod: "/etcdserverpb.Watch/Watch"},
		func(srv any, stream grpc.ServerStream) error { return nil })
	require.Error(t, err)
	st, _ := status.FromError(err)
	assert.Equal(t, codes.ResourceExhausted, st.Code())

	close(block)
	require.NoError(t, <-done)
	assert.EqualValues(t, 0, c.sched.streamCount())
}

func TestMetricsExported(t *testing.T) {
	c := newTestController(t, nil, Config{RateLimitWrites: 1, BurstFactor: 1.0})

	beforeAdmitted := testutil.ToFloat64(admittedRequests.WithLabelValues(GroupWrite.String(), PriorityNormal.String()))
	beforeRejected := testutil.ToFloat64(rejectedRequests.WithLabelValues(GroupWrite.String(), reasonRateLimited))

	_, _ = callUnary(c, "/etcdserverpb.KV/Put", &pb.PutRequest{})
	_, _ = callUnary(c, "/etcdserverpb.KV/Put", &pb.PutRequest{})

	assert.Equal(t, beforeAdmitted+1, testutil.ToFloat64(admittedRequests.WithLabelValues(GroupWrite.String(), PriorityNormal.String())))
	assert.Equal(t, beforeRejected+1, testutil.ToFloat64(rejectedRequests.WithLabelValues(GroupWrite.String(), reasonRateLimited)))
	assert.Greater(t, testutil.ToFloat64(rateLimitUtilization.WithLabelValues(GroupWrite.String())), 0.0)
}

func TestControllerStatus(t *testing.T) {
	c := newTestController(t, nil, Config{
		DryRun:          true,
		RateLimitWrites: 10,
		BurstFactor:     2.0,
	})

	st := c.Status()
	assert.True(t, st.Enabled)
	assert.True(t, st.DryRun)
	assert.True(t, math.IsInf(st.ReadTokensAvailable, 1), "reads unlimited => +Inf tokens")
	assert.InDelta(t, 20.0, st.WriteTokensAvailable, 0.5, "write bucket starts full at burst=20")
	assert.EqualValues(t, 0, st.InflightRequests)

	// Consume one write token and verify it is reflected.
	_, err := callUnary(c, "/etcdserverpb.KV/Put", &pb.PutRequest{})
	require.NoError(t, err)
	st = c.Status()
	assert.Less(t, st.WriteTokensAvailable, 20.0)
	assert.EqualValues(t, 0, st.InflightRequests, "inflight released after handler returns")
}

func TestControllerResetLimiters(t *testing.T) {
	c := newTestController(t, nil, Config{RateLimitWrites: 1, BurstFactor: 1.0})

	// Exhaust the write bucket: first call admitted, subsequent calls rejected.
	called, err := callUnary(c, "/etcdserverpb.KV/Put", &pb.PutRequest{})
	require.NoError(t, err)
	require.True(t, called)

	for i := 0; i < 5; i++ {
		called, err = callUnary(c, "/etcdserverpb.KV/Put", &pb.PutRequest{})
		require.Error(t, err, "bucket should be exhausted")
		require.False(t, called)
	}

	c.ResetLimiters()

	// After reset the bucket is full again and inflight is cleared.
	assert.EqualValues(t, 0, c.sched.inflightCount())
	called, err = callUnary(c, "/etcdserverpb.KV/Put", &pb.PutRequest{})
	require.NoError(t, err, "request must be admitted after ResetLimiters")
	require.True(t, called)
}

func TestControllerString(t *testing.T) {
	c := newTestController(t, nil, Config{
		RateLimitReads:    100,
		RateLimitWrites:   50,
		BurstFactor:       2.0,
		OverloadThreshold: 0.8,
	})

	s := c.String()
	for _, want := range []string{
		"admission(",
		"enabled=true",
		"dry_run=false",
		"read_rps=100",
		"write_rps=50",
		"burst=2.0",
		"overload=0.8",
		"inflight=0",
	} {
		assert.Contains(t, s, want)
	}
}

func TestRetryAfterMs(t *testing.T) {
	assert.Equal(t, 1, retryAfterMs(0))
	assert.Equal(t, 1, retryAfterMs(1))
	assert.Equal(t, 250, retryAfterMs(250*1e6))
}

func TestConfigValidate(t *testing.T) {
	require.NoError(t, (&Config{}).Validate())
	require.NoError(t, (&Config{Enabled: true, BurstFactor: 1.0, OverloadThreshold: 0.5}).Validate())
	require.Error(t, (&Config{Enabled: true, BurstFactor: 0.5, OverloadThreshold: 0.5}).Validate())
	require.Error(t, (&Config{Enabled: true, BurstFactor: 1.0, OverloadThreshold: 0}).Validate())
	require.Error(t, (&Config{Enabled: true, BurstFactor: 1.0, OverloadThreshold: 1.5}).Validate())
}
