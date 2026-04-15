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

import "github.com/prometheus/client_golang/prometheus"

const (
	decisionAllowed        = "allowed"
	decisionRejectedGlobal = "rejected_global"
	decisionRejectedClient = "rejected_client"
)

var (
	rateLimitRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "etcd",
			Subsystem: "server",
			Name:      "ratelimit_requests_total",
			Help:      "The total number of gRPC requests evaluated by the rate limiter, by endpoint and decision.",
		},
		[]string{"endpoint", "decision"},
	)

	rateLimitClients = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "etcd",
		Subsystem: "server",
		Name:      "ratelimit_current_clients",
		Help:      "The current number of distinct client IPs tracked by the rate limiter.",
	})

	rateLimitTokens = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "etcd",
		Subsystem: "server",
		Name:      "ratelimit_tokens_remaining",
		Help:      "The number of tokens currently remaining in the global rate-limit bucket.",
	})

	rateLimitCheckDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "etcd",
		Subsystem: "server",
		Name:      "ratelimit_rejection_duration_seconds",
		Help:      "Latency added by the rate-limit admission check, in seconds.",
		// 100ns .. ~26ms
		Buckets: prometheus.ExponentialBuckets(1e-7, 4, 10),
	})
)

func init() {
	prometheus.MustRegister(rateLimitRequests)
	prometheus.MustRegister(rateLimitClients)
	prometheus.MustRegister(rateLimitTokens)
	prometheus.MustRegister(rateLimitCheckDuration)
}
