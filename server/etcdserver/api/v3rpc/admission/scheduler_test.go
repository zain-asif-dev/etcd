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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRaftStatus struct {
	committed uint64
	applied   uint64
}

func (f *fakeRaftStatus) CommittedIndex() uint64 { return f.committed }
func (f *fakeRaftStatus) AppliedIndex() uint64   { return f.applied }

func TestSchedulerNoLimits(t *testing.T) {
	s := newScheduler(nil, Config{})
	for i := 0; i < 1000; i++ {
		rel, r := s.admit(context.Background(), PriorityNormal)
		require.Equal(t, shedNone, r)
		require.NotNil(t, rel)
	}
	assert.EqualValues(t, 1000, s.inflightCount())
}

func TestSchedulerInflightShedding(t *testing.T) {
	// threshold = 10*0.5 = 5, hard = 10.
	s := newScheduler(nil, Config{MaxConcurrentStreams: 10, OverloadThreshold: 0.5})

	var releases []func()
	admit := func(p Priority) shedReason {
		rel, r := s.admit(context.Background(), p)
		if rel != nil {
			releases = append(releases, rel)
		}
		return r
	}

	// First 5 normal requests admitted.
	for i := 0; i < 5; i++ {
		require.Equal(t, shedNone, admit(PriorityNormal))
	}
	// 6th normal exceeds soft threshold => shed.
	require.Equal(t, shedInflight, admit(PriorityNormal))

	// High priority still admitted up to hard ceiling.
	for i := 0; i < 5; i++ {
		require.Equal(t, shedNone, admit(PriorityHigh))
	}
	// 11th high exceeds hard ceiling => shed.
	require.Equal(t, shedInflight, admit(PriorityHigh))

	// Critical is never shed.
	require.Equal(t, shedNone, admit(PriorityCritical))
	assert.EqualValues(t, 11, s.inflightCount())

	// Releasing brings normal back under threshold.
	for _, rel := range releases {
		rel()
	}
	assert.EqualValues(t, 0, s.inflightCount())
	require.Equal(t, shedNone, admit(PriorityNormal))
}

func TestSchedulerApplyLagShedding(t *testing.T) {
	rs := &fakeRaftStatus{}
	s := newScheduler(rs, Config{})

	tests := []struct {
		name string
		gap  uint64
		prio Priority
		want shedReason
	}{
		{"no-lag normal", 0, PriorityNormal, shedNone},
		{"under-write-threshold normal", applyLagShedWrites, PriorityNormal, shedNone},
		{"over-write-threshold normal", applyLagShedWrites + 1, PriorityNormal, shedApplyLag},
		{"over-write-threshold high", applyLagShedWrites + 1, PriorityHigh, shedNone},
		{"over-read-threshold high", applyLagShedReads + 1, PriorityHigh, shedApplyLag},
		{"over-read-threshold critical", applyLagShedReads + 1, PriorityCritical, shedNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs.applied = 100
			rs.committed = 100 + tt.gap
			rel, r := s.admit(context.Background(), tt.prio)
			assert.Equal(t, tt.want, r)
			if rel != nil {
				rel()
			}
		})
	}
}

func TestSchedulerStreamLimit(t *testing.T) {
	s := newScheduler(nil, Config{MaxConcurrentStreams: 4, OverloadThreshold: 0.5}) // limit = 2

	rel1, r := s.admitStream(PriorityHigh)
	require.Equal(t, shedNone, r)
	rel2, r := s.admitStream(PriorityHigh)
	require.Equal(t, shedNone, r)

	_, r = s.admitStream(PriorityHigh)
	require.Equal(t, shedInflight, r)
	assert.EqualValues(t, 2, s.streamCount())

	// Critical bypasses the limit.
	relC, r := s.admitStream(PriorityCritical)
	require.Equal(t, shedNone, r)
	relC()

	rel1()
	_, r = s.admitStream(PriorityHigh)
	require.Equal(t, shedNone, r)

	rel2()
}

func TestSchedulerEffectivePriorityNoop(t *testing.T) {
	// Documents the deferred peer-detection hook: priority is unchanged.
	s := newScheduler(nil, Config{})
	assert.Equal(t, PriorityNormal, s.effectivePriority(context.Background(), PriorityNormal))
	assert.Equal(t, PriorityHigh, s.effectivePriority(context.Background(), PriorityHigh))
}
