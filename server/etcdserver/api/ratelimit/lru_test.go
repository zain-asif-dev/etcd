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
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientTrackerEviction(t *testing.T) {
	tr := newClientTracker(3, func() *tokenBucket { return newTokenBucket(1, 1, nil) })

	a := tr.get("a")
	tr.get("b")
	tr.get("c")
	require.Equal(t, 3, tr.len())

	// "a" is LRU; touching "a" should make "b" the LRU.
	require.Same(t, a, tr.get("a"), "existing entry must be returned, not recreated")

	// Insert "d" → evicts "b".
	tr.get("d")
	require.Equal(t, 3, tr.len())

	tr.mu.Lock()
	_, hasB := tr.items["b"]
	_, hasA := tr.items["a"]
	tr.mu.Unlock()
	require.False(t, hasB, "LRU entry b should have been evicted")
	require.True(t, hasA, "recently-used entry a should remain")
}

func TestClientTrackerBounded(t *testing.T) {
	const cap = 16
	tr := newClientTracker(cap, func() *tokenBucket { return newTokenBucket(1, 1, nil) })
	for i := 0; i < 1000; i++ {
		tr.get(fmt.Sprintf("c%d", i))
		require.LessOrEqual(t, tr.len(), cap)
	}
	require.Equal(t, cap, tr.len())
}

func TestClientTrackerMinCapacity(t *testing.T) {
	tr := newClientTracker(0, func() *tokenBucket { return newTokenBucket(1, 1, nil) })
	tr.get("x")
	tr.get("y")
	require.Equal(t, 1, tr.len(), "capacity floor is 1")
}
