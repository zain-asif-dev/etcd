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
	"net"
	"time"

	"go.uber.org/zap"
)

// Limiter applies global and per-client token-bucket rate limiting to gRPC
// requests. It is safe for concurrent use.
type Limiter struct {
	cfg     Config
	lg      *zap.Logger
	global  *tokenBucket
	clients *clientTracker
	now     func() time.Time
}

// New constructs a Limiter from cfg. If cfg.Enabled is false the returned
// limiter admits every request without doing any work.
func New(cfg Config, lg *zap.Logger) *Limiter {
	return newLimiter(cfg, lg, time.Now)
}

func newLimiter(cfg Config, lg *zap.Logger, now func() time.Time) *Limiter {
	if lg == nil {
		lg = zap.NewNop()
	}
	l := &Limiter{cfg: cfg, lg: lg, now: now}
	if !cfg.Enabled {
		return l
	}
	l.global = newTokenBucket(float64(cfg.RequestsPerSecond), float64(cfg.BurstSize), now)
	perClientRate := float64(cfg.PerClientRPS)
	l.clients = newClientTracker(cfg.MaxTrackedClients, func() *tokenBucket {
		return newTokenBucket(perClientRate, perClientRate, now)
	})
	return l
}

// Allow decides whether the request identified by fullMethod from clientAddr
// may proceed. It returns ok=true when admitted; otherwise retryAfter is a
// hint for how long the client should wait and reason is one of "global" or
// "per-client".
func (l *Limiter) Allow(fullMethod, clientAddr string) (ok bool, retryAfter time.Duration, reason string) {
	if !l.cfg.Enabled {
		return true, 0, ""
	}

	start := l.now()
	ep := endpointName(fullMethod)
	defer func() {
		rateLimitCheckDuration.Observe(l.now().Sub(start).Seconds())
		rateLimitTokens.Set(l.global.remaining())
		rateLimitClients.Set(float64(l.clients.len()))
	}()

	kind := classify(fullMethod)
	if kind == opExempt {
		rateLimitRequests.WithLabelValues(ep, decisionAllowed).Inc()
		return true, 0, ""
	}

	cost := 1.0
	if kind == opRead && l.cfg.ReadWriteRatio > 0 {
		cost = 1.0 / l.cfg.ReadWriteRatio
	}

	ip := hostOnly(clientAddr)
	if ok, wait := l.clients.get(ip).take(cost); !ok {
		rateLimitRequests.WithLabelValues(ep, decisionRejectedClient).Inc()
		l.lg.Warn("rate limit exceeded",
			zap.String("client-addr", clientAddr),
			zap.String("method", fullMethod),
			zap.String("reason", "per-client"),
			zap.Int64("retry-after-ms", wait.Milliseconds()),
		)
		return false, wait, "per-client"
	}
	if ok, wait := l.global.take(cost); !ok {
		rateLimitRequests.WithLabelValues(ep, decisionRejectedGlobal).Inc()
		l.lg.Warn("rate limit exceeded",
			zap.String("client-addr", clientAddr),
			zap.String("method", fullMethod),
			zap.String("reason", "global"),
			zap.Int64("retry-after-ms", wait.Milliseconds()),
		)
		return false, wait, "global"
	}

	rateLimitRequests.WithLabelValues(ep, decisionAllowed).Inc()
	return true, 0, ""
}

// hostOnly strips the port from a "host:port" address. If the input has no
// port it is returned unchanged.
func hostOnly(addr string) string {
	if addr == "" {
		return "unknown"
	}
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}
