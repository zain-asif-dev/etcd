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
	"fmt"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// RetryAfterMetadataKey is the gRPC trailer/header key carrying the suggested
// number of seconds a client should wait before retrying a rate-limited
// request.
const RetryAfterMetadataKey = "etcd-ratelimit-retry-after"

// UnaryInterceptor returns a grpc.UnaryServerInterceptor that enforces the
// limiter on every unary RPC. When the limiter is disabled the returned
// interceptor is a pass-through with negligible overhead.
func (l *Limiter) UnaryInterceptor() grpc.UnaryServerInterceptor {
	if !l.cfg.Enabled {
		return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			return handler(ctx, req)
		}
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ok, wait, reason := l.Allow(info.FullMethod, peerAddr(ctx))
		if !ok {
			_ = grpc.SetTrailer(ctx, retryAfterMD(wait))
			l.logReject(info.FullMethod, peerAddr(ctx), reason, wait)
			return nil, rejectErr(reason, wait)
		}
		return handler(ctx, req)
	}
}

// StreamInterceptor returns a grpc.StreamServerInterceptor that enforces the
// limiter at stream-creation time.
func (l *Limiter) StreamInterceptor() grpc.StreamServerInterceptor {
	if !l.cfg.Enabled {
		return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			return handler(srv, ss)
		}
	}
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		addr := peerAddr(ss.Context())
		ok, wait, reason := l.Allow(info.FullMethod, addr)
		if !ok {
			_ = ss.SetHeader(retryAfterMD(wait))
			l.logReject(info.FullMethod, addr, reason, wait)
			return rejectErr(reason, wait)
		}
		return handler(srv, ss)
	}
}

func (l *Limiter) logReject(method, addr, reason string, wait time.Duration) {
	l.lg.Debug("rate limit rejected request",
		zap.String("method", method),
		zap.String("peer", addr),
		zap.String("reason", reason),
		zap.Duration("retry-after", wait),
	)
}

func peerAddr(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		return p.Addr.String()
	}
	return ""
}

func retryAfterMD(wait time.Duration) metadata.MD {
	return metadata.Pairs(RetryAfterMetadataKey, fmt.Sprintf("%.3f", wait.Seconds()))
}

func rejectErr(reason string, wait time.Duration) error {
	return status.Errorf(codes.ResourceExhausted,
		"etcdserver: request rate limit exceeded (%s), retry after %v",
		reason, wait.Round(time.Millisecond))
}
