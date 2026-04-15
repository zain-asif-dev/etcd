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
	"sync/atomic"
)

// RaftStatus exposes the minimal Raft state the scheduler needs to derive an
// apply-lag backpressure signal. *etcdserver.EtcdServer satisfies this.
type RaftStatus interface {
	CommittedIndex() uint64
	AppliedIndex() uint64
}

// shedReason explains why the scheduler rejected a request.
type shedReason string

const (
	shedNone     shedReason = ""
	shedInflight shedReason = "overload_inflight"
	shedApplyLag shedReason = "overload_apply_lag"
)

// scheduler implements priority-based load shedding. It tracks the number of
// in-flight unary RPCs and consults Raft apply lag; when either signal
// crosses its threshold, requests are shed in priority order (Normal first,
// then High; Critical is never shed).
type scheduler struct {
	rs RaftStatus

	inflight     atomic.Int64
	inflightHigh int64 // shed Normal above this; 0 disables
	inflightHard int64 // shed High above this; 0 disables

	streams     atomic.Int64
	streamLimit int64 // 0 disables
}

func newScheduler(rs RaftStatus, cfg Config) *scheduler {
	s := &scheduler{
		rs:          rs,
		streamLimit: cfg.streamLimit(),
	}
	if t := cfg.inflightThreshold(); t > 0 {
		s.inflightHigh = t
		// Hard ceiling: the full configured stream count. Between High and
		// Hard, only Normal-priority requests are shed.
		s.inflightHard = int64(cfg.MaxConcurrentStreams)
	}
	return s
}

// admit decides whether a unary request at the given priority may proceed.
// On success the caller MUST call the returned release func when the handler
// completes. On rejection release is nil.
func (s *scheduler) admit(ctx context.Context, prio Priority) (release func(), reason shedReason) {
	prio = s.effectivePriority(ctx, prio)

	if prio == PriorityCritical {
		s.inflight.Add(1)
		return s.release, shedNone
	}

	if r := s.applyLagShed(prio); r != shedNone {
		return nil, r
	}

	cur := s.inflight.Add(1)
	if s.inflightHigh > 0 {
		if prio >= PriorityNormal && cur > s.inflightHigh {
			s.inflight.Add(-1)
			return nil, shedInflight
		}
		if prio >= PriorityHigh && cur > s.inflightHard {
			s.inflight.Add(-1)
			return nil, shedInflight
		}
	}
	return s.release, shedNone
}

func (s *scheduler) release() { s.inflight.Add(-1) }

// admitStream decides whether a new stream RPC may be opened. On success the
// caller MUST call the returned release func when the stream handler returns.
func (s *scheduler) admitStream(prio Priority) (release func(), reason shedReason) {
	if prio == PriorityCritical {
		s.streams.Add(1)
		return s.releaseStream, shedNone
	}
	cur := s.streams.Add(1)
	if s.streamLimit > 0 && cur > s.streamLimit {
		s.streams.Add(-1)
		return nil, shedInflight
	}
	return s.releaseStream, shedNone
}

func (s *scheduler) releaseStream() { s.streams.Add(-1) }

func (s *scheduler) applyLagShed(prio Priority) shedReason {
	if s.rs == nil {
		return shedNone
	}
	ci := s.rs.CommittedIndex()
	ai := s.rs.AppliedIndex()
	if ci <= ai {
		return shedNone
	}
	gap := ci - ai
	if prio >= PriorityNormal && gap > applyLagShedWrites {
		return shedApplyLag
	}
	if prio >= PriorityHigh && gap > applyLagShedReads {
		return shedApplyLag
	}
	return shedNone
}

// effectivePriority applies any context-derived priority boost.
//
// TODO(admission): boost priority for intra-cluster peer requests once a
// reliable peer-vs-client signal is available on the v3 gRPC listener. Peer
// traffic currently uses the raft HTTP transport, not this gRPC path, so
// there is no clean signal today. The hook is kept so the scheduler tests and
// callers do not need to change when peer detection lands.
func (s *scheduler) effectivePriority(_ context.Context, prio Priority) Priority {
	return prio
}

// inflightCount returns the current number of in-flight unary RPCs.
func (s *scheduler) inflightCount() int64 { return s.inflight.Load() }

// resetInflight forcibly clears the in-flight unary counter. Intended for
// operational recovery via Controller.ResetLimiters; concurrent releases of
// requests admitted before the reset may briefly drive the counter negative.
func (s *scheduler) resetInflight() { s.inflight.Store(0) }

// streamCount returns the current number of open stream RPCs.
func (s *scheduler) streamCount() int64 { return s.streams.Load() }
