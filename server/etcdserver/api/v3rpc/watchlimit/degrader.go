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
	"sync/atomic"

	"go.uber.org/zap"

	"go.etcd.io/etcd/server/v3/config"
)

// DegradationLevel describes how aggressively the watch server should shed
// load in response to memory pressure from buffered watch events.
type DegradationLevel int32

const (
	// DegradeNone indicates normal operation; no degradation is applied.
	DegradeNone DegradationLevel = 0
	// DegradeReduceBatch indicates the server should reduce the batch size
	// of events sent to clients to slow buffer growth.
	DegradeReduceBatch DegradationLevel = 1
	// DegradePauseAndCancel indicates the server should pause low-priority
	// watches and begin cancelling the longest-idle non-critical watches.
	DegradePauseAndCancel DegradationLevel = 2
)

// String returns a human-readable name for the level, suitable for use in
// structured log fields.
func (l DegradationLevel) String() string {
	switch l {
	case DegradeNone:
		return "none"
	case DegradeReduceBatch:
		return "reduce-batch"
	case DegradePauseAndCancel:
		return "pause-and-cancel"
	default:
		return "unknown"
	}
}

// Degrader evaluates estimated watch-event memory usage against the
// configured soft limit and reports the current DegradationLevel.
type Degrader struct {
	cfg *config.WatchLimitConfig
	lg  *zap.Logger

	level atomic.Int32
}

// NewDegrader returns a Degrader configured from cfg. A nil cfg is treated
// as a zero-value config (degradation disabled). A nil logger is replaced
// with a no-op logger.
func NewDegrader(cfg *config.WatchLimitConfig, lg *zap.Logger) *Degrader {
	if cfg == nil {
		cfg = &config.WatchLimitConfig{}
	}
	if lg == nil {
		lg = zap.NewNop()
	}
	return &Degrader{cfg: cfg, lg: lg}
}

// CheckMemoryPressure evaluates estimatedBytes against the configured soft
// limit and returns the resulting DegradationLevel.
//
// Thresholds (as a fraction of MemorySoftLimitBytes):
//
//	[0,   70%)  -> DegradeNone
//	[70%, 90%)  -> DegradeReduceBatch
//	[90%, +inf) -> DegradePauseAndCancel
//
// If MemorySoftLimitBytes is zero (or negative) the feature is disabled and
// DegradeNone is always returned. The returned level is also stored
// atomically and observable via CurrentLevel; level transitions are logged
// at warn.
func (d *Degrader) CheckMemoryPressure(estimatedBytes uint64) DegradationLevel {
	newLevel := d.computeLevel(estimatedBytes)
	old := DegradationLevel(d.level.Swap(int32(newLevel)))
	if old != newLevel {
		d.lg.Warn("watch memory-pressure degradation level changed",
			zap.String("from", old.String()),
			zap.String("to", newLevel.String()),
			zap.Uint64("estimated-bytes", estimatedBytes),
			zap.Int64("soft-limit-bytes", d.cfg.MemorySoftLimitBytes),
		)
	}
	return newLevel
}

func (d *Degrader) computeLevel(estimatedBytes uint64) DegradationLevel {
	limit := d.cfg.MemorySoftLimitBytes
	if limit <= 0 {
		return DegradeNone
	}
	// Compute thresholds at 70% and 90% of the soft limit. Integer
	// arithmetic is sufficient here; the limit is operator-supplied and not
	// expected to be near overflow.
	ulimit := uint64(limit)
	reduceAt := ulimit * 7 / 10
	pauseAt := ulimit * 9 / 10
	switch {
	case estimatedBytes >= pauseAt:
		return DegradePauseAndCancel
	case estimatedBytes >= reduceAt:
		return DegradeReduceBatch
	default:
		return DegradeNone
	}
}

// CurrentLevel returns the most recently computed DegradationLevel. Safe for
// concurrent readers.
func (d *Degrader) CurrentLevel() DegradationLevel {
	return DegradationLevel(d.level.Load())
}
