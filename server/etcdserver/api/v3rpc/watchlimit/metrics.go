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

package watchlimit

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	metricsNamespace = "etcd"
	metricsSubsystem = "watch_limiter"

	// RejectionReasonPerClientLimit is the "reason" label value used when a
	// watch is rejected because the per-client limit was reached.
	RejectionReasonPerClientLimit = "per_client_limit"
	// RejectionReasonGlobalLimit is the "reason" label value used when a
	// watch is rejected because the server-wide limit was reached.
	RejectionReasonGlobalLimit = "global_limit"
)

var (
	// ActiveWatchesTotal is the current server-wide number of active watches
	// admitted by the limiter.
	ActiveWatchesTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Subsystem: metricsSubsystem,
		Name:      "active_watches_total",
		Help:      "Current server-wide number of active watches.",
	})

	// ActiveWatchesPerClient is the current number of active watches per
	// client identity.
	ActiveWatchesPerClient = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "active_watches_per_client",
			Help:      "Current number of active watches per client identity.",
		},
		[]string{"client"},
	)

	// WatchRegisterRejectionsTotal counts watch creation requests rejected by
	// the limiter, labelled by client identity and rejection reason.
	WatchRegisterRejectionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "watch_register_rejections_total",
			Help:      "Total number of watch creation requests rejected by the limiter.",
		},
		[]string{"client", "reason"},
	)

	// WatchMemoryPressureBytes is the estimated total size in bytes of
	// buffered watch events server-wide.
	WatchMemoryPressureBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Subsystem: metricsSubsystem,
		Name:      "watch_memory_pressure_bytes",
		Help:      "Estimated total size in bytes of buffered watch events server-wide.",
	})
)
