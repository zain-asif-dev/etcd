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

import "github.com/prometheus/client_golang/prometheus"

// rejectionReason label values.
const (
	reasonRateLimited = "rate_limited"
	reasonOverloaded  = "overloaded"
	reasonStreamLimit = "stream_limit"
)

var (
	admittedRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "etcd",
			Subsystem: "server",
			Name:      "admission_admitted_requests_total",
			Help:      "Total client requests admitted by the admission controller, by endpoint group and priority.",
		},
		[]string{"endpoint_group", "priority"},
	)

	rejectedRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "etcd",
			Subsystem: "server",
			Name:      "admission_rejected_requests_total",
			Help:      "Total client requests rejected by the admission controller, by endpoint group and reason.",
		},
		[]string{"endpoint_group", "reason"},
	)

	dryRunRejectedRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "etcd",
			Subsystem: "server",
			Name:      "admission_dry_run_rejected_total",
			Help:      "Total client requests that would have been rejected by the admission controller in dry-run mode, by reason.",
		},
		[]string{"reason"},
	)

	inflightRequests = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "etcd",
			Subsystem: "server",
			Name:      "admission_inflight_requests",
			Help:      "Number of requests currently executing past the admission controller, by endpoint group.",
		},
		[]string{"endpoint_group"},
	)

	rateLimitUtilization = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "etcd",
			Subsystem: "server",
			Name:      "admission_rate_limit_utilization",
			Help:      "Fraction [0,1] of the token-bucket burst currently consumed, by endpoint group.",
		},
		[]string{"endpoint_group"},
	)

	admissionLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "etcd",
			Subsystem: "server",
			Name:      "admission_latency_seconds",
			Help:      "Time spent in the admission controller per request.",
			// 1µs .. ~260ms — admission is non-blocking so this should stay
			// in the low-microsecond range.
			Buckets: prometheus.ExponentialBuckets(1e-6, 4, 10),
		},
	)
)

func init() {
	prometheus.MustRegister(admittedRequests)
	prometheus.MustRegister(rejectedRequests)
	prometheus.MustRegister(dryRunRejectedRequests)
	prometheus.MustRegister(inflightRequests)
	prometheus.MustRegister(rateLimitUtilization)
	prometheus.MustRegister(admissionLatency)
}
