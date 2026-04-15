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
	"fmt"
	"strconv"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
)

// Controller is the admission-control entry point. It owns the per-group
// rate limiters and the priority scheduler, and exposes gRPC unary and
// stream interceptors that gate requests before they reach the etcd server
// handlers.
type Controller struct {
	lg      *zap.Logger
	limiter *groupLimiter
	sched   *scheduler
	cfg     Config
}

// AdmissionStatus is a point-in-time snapshot of the admission controller's
// runtime state.
type AdmissionStatus struct {
	Enabled              bool
	DryRun               bool
	ReadTokensAvailable  float64
	WriteTokensAvailable float64
	InflightRequests     int64
}

// NewController constructs an admission controller. rs may be nil, in which
// case apply-lag backpressure is disabled (useful for tests).
func NewController(lg *zap.Logger, rs RaftStatus, cfg Config) *Controller {
	if lg == nil {
		lg = zap.NewNop()
	}
	c := &Controller{
		lg:      lg,
		limiter: newGroupLimiter(cfg),
		sched:   newScheduler(rs, cfg),
		cfg:     cfg,
	}
	lg.Info(
		"admission control enabled",
		zap.Bool("dry-run", cfg.DryRun),
		zap.Uint("rate-limit-reads", cfg.RateLimitReads),
		zap.Uint("rate-limit-writes", cfg.RateLimitWrites),
		zap.Float64("burst-factor", cfg.BurstFactor),
		zap.Float64("overload-threshold", cfg.OverloadThreshold),
		zap.Int64("inflight-threshold", cfg.inflightThreshold()),
		zap.Int64("stream-limit", cfg.streamLimit()),
	)
	return c
}

// Status returns a snapshot of the controller's current runtime state.
func (c *Controller) Status() AdmissionStatus {
	return AdmissionStatus{
		Enabled:              c.cfg.Enabled,
		DryRun:               c.cfg.DryRun,
		ReadTokensAvailable:  c.limiter.tokens(GroupRead),
		WriteTokensAvailable: c.limiter.tokens(GroupWrite),
		InflightRequests:     c.sched.inflightCount(),
	}
}

// ResetLimiters refills all token-bucket rate limiters to their full burst
// capacity and clears the in-flight request counter. Intended for operational
// recovery after a burst of rejections.
func (c *Controller) ResetLimiters() {
	c.limiter = newGroupLimiter(c.cfg)
	c.sched.resetInflight()
	c.lg.Info("admission control limiters reset")
}

// String returns a human-readable one-line summary of the controller's
// configuration and live state, suitable for debug logging.
func (c *Controller) String() string {
	return fmt.Sprintf(
		"admission(enabled=%t dry_run=%t read_rps=%d write_rps=%d burst=%.1f overload=%.1f inflight=%d)",
		c.cfg.Enabled,
		c.cfg.DryRun,
		c.cfg.RateLimitReads,
		c.cfg.RateLimitWrites,
		c.cfg.BurstFactor,
		c.cfg.OverloadThreshold,
		c.sched.inflightCount(),
	)
}

// UnaryInterceptor returns a gRPC unary server interceptor that enforces
// admission control. Requests in the health group bypass all checks.
func (c *Controller) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		cls := classify(info.FullMethod, req)

		if cls.group == GroupHealth {
			c.recordAdmitted(cls, start)
			return handler(ctx, req)
		}

		// Priority-based shedding first: if the server is overloaded there
		// is no point spending a rate-limit token.
		release, reason := c.sched.admit(ctx, cls.priority)
		if reason != shedNone {
			if !c.cfg.DryRun {
				c.recordRejected(cls, string(reason), start)
				return nil, c.reject(ctx, rpctypes.ErrGRPCServerOverloaded, overloadRetryAfterMs)
			}
			dryRunRejectedRequests.WithLabelValues(reasonOverloaded).Inc()
		}
		if release != nil {
			defer release()
		}

		ok, retry := c.limiter.allow(cls.group)
		rateLimitUtilization.WithLabelValues(cls.group.String()).Set(c.limiter.utilization(cls.group))
		if !ok {
			if !c.cfg.DryRun {
				c.recordRejected(cls, reasonRateLimited, start)
				return nil, c.reject(ctx, rpctypes.ErrGRPCRequestRateLimited, retryAfterMs(retry))
			}
			dryRunRejectedRequests.WithLabelValues(reasonRateLimited).Inc()
		}

		c.recordAdmitted(cls, start)
		inflightRequests.WithLabelValues(cls.group.String()).Inc()
		defer inflightRequests.WithLabelValues(cls.group.String()).Dec()
		return handler(ctx, req)
	}
}

// StreamInterceptor returns a gRPC stream server interceptor that enforces
// the stream concurrency limit.
func (c *Controller) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		cls := classifyStream(info.FullMethod)

		if cls.group == GroupHealth {
			c.recordAdmitted(cls, start)
			return handler(srv, ss)
		}

		release, reason := c.sched.admitStream(cls.priority)
		if reason != shedNone {
			if !c.cfg.DryRun {
				c.recordRejected(cls, reasonStreamLimit, start)
				setRetryAfterTrailer(ss.Context(), overloadRetryAfterMs)
				return rpctypes.ErrGRPCServerOverloaded
			}
			dryRunRejectedRequests.WithLabelValues(reasonOverloaded).Inc()
		}
		if release != nil {
			defer release()
		}

		c.recordAdmitted(cls, start)
		inflightRequests.WithLabelValues(cls.group.String()).Inc()
		defer inflightRequests.WithLabelValues(cls.group.String()).Dec()
		return handler(srv, ss)
	}
}

func (c *Controller) reject(ctx context.Context, err error, retryMs int) error {
	setRetryAfterTrailer(ctx, retryMs)
	return err
}

func (c *Controller) recordAdmitted(cls endpointClass, start time.Time) {
	admittedRequests.WithLabelValues(cls.group.String(), cls.priority.String()).Inc()
	admissionLatency.Observe(time.Since(start).Seconds())
}

func (c *Controller) recordRejected(cls endpointClass, reason string, start time.Time) {
	rejectedRequests.WithLabelValues(cls.group.String(), reason).Inc()
	admissionLatency.Observe(time.Since(start).Seconds())
}

func setRetryAfterTrailer(ctx context.Context, ms int) {
	if ms <= 0 {
		return
	}
	md := metadata.Pairs(rpctypes.MetadataRetryAfterMsKey, strconv.Itoa(ms))
	// Best-effort: SetTrailer fails if the RPC has already completed, which
	// cannot happen here since we have not invoked the handler.
	_ = grpc.SetTrailer(ctx, md)
}

func retryAfterMs(d time.Duration) int {
	ms := int(d / time.Millisecond)
	if ms < 1 {
		ms = 1
	}
	return ms
}
