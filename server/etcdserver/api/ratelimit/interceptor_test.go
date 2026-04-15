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
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func ctxWithPeer(addr string) context.Context {
	tcp, _ := net.ResolveTCPAddr("tcp", addr)
	return peer.NewContext(context.Background(), &peer.Peer{Addr: tcp})
}

func TestUnaryInterceptor(t *testing.T) {
	clk := newFakeClock()
	l := newLimiter(Config{
		Enabled: true, RequestsPerSecond: 1, BurstSize: 1,
		PerClientRPS: 1, MaxTrackedClients: 10, ReadWriteRatio: 1,
	}, zaptest.NewLogger(t), clk.now)

	ic := l.UnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: mPut}

	calls := 0
	handler := func(ctx context.Context, req any) (any, error) {
		calls++
		return "ok", nil
	}

	resp, err := ic(ctxWithPeer("127.0.0.1:1111"), nil, info, handler)
	require.NoError(t, err)
	require.Equal(t, "ok", resp)
	require.Equal(t, 1, calls)

	resp, err = ic(ctxWithPeer("127.0.0.1:1111"), nil, info, handler)
	require.Nil(t, resp)
	require.Equal(t, 1, calls, "handler must not be called when rate limited")
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.ResourceExhausted, st.Code())
	require.Contains(t, st.Message(), "rate limit exceeded")
	require.Contains(t, st.Message(), "retry after")
}

func TestUnaryInterceptorDisabledPassThrough(t *testing.T) {
	l := New(Config{Enabled: false}, zaptest.NewLogger(t))
	ic := l.UnaryInterceptor()
	calls := 0
	handler := func(ctx context.Context, req any) (any, error) {
		calls++
		return nil, nil
	}
	for i := 0; i < 100; i++ {
		_, err := ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: mPut}, handler)
		require.NoError(t, err)
	}
	require.Equal(t, 100, calls)
}

type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
	hdr metadata.MD
}

func (s *fakeStream) Context() context.Context    { return s.ctx }
func (s *fakeStream) SetHeader(md metadata.MD) error {
	s.hdr = metadata.Join(s.hdr, md)
	return nil
}

func TestStreamInterceptor(t *testing.T) {
	clk := newFakeClock()
	l := newLimiter(Config{
		Enabled: true, RequestsPerSecond: 1, BurstSize: 1,
		PerClientRPS: 1, MaxTrackedClients: 10, ReadWriteRatio: 1,
	}, zaptest.NewLogger(t), clk.now)

	ic := l.StreamInterceptor()
	// Use a write-classified unary-style method to ensure cost=1 (Watch is exempt).
	info := &grpc.StreamServerInfo{FullMethod: "/etcdserverpb.Lease/LeaseGrant"}

	calls := 0
	handler := func(srv any, ss grpc.ServerStream) error {
		calls++
		return nil
	}

	ss1 := &fakeStream{ctx: ctxWithPeer("127.0.0.1:1")}
	require.NoError(t, ic(nil, ss1, info, handler))
	require.Equal(t, 1, calls)

	ss2 := &fakeStream{ctx: ctxWithPeer("127.0.0.1:1")}
	err := ic(nil, ss2, info, handler)
	require.Equal(t, 1, calls, "handler must not be called when rate limited")
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.NotEmpty(t, ss2.hdr.Get(RetryAfterMetadataKey), "retry-after header should be set")
}
