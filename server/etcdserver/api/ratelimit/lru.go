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
	"container/list"
	"sync"
)

type clientEntry struct {
	key    string
	bucket *tokenBucket
}

// clientTracker is a fixed-capacity LRU map from client key (IP) to a
// per-client token bucket. When capacity is exceeded the least-recently-used
// entry is evicted, bounding memory regardless of how many distinct clients
// connect.
type clientTracker struct {
	mu       sync.Mutex
	capacity int
	ll       *list.List
	items    map[string]*list.Element
	newBkt   func() *tokenBucket
}

func newClientTracker(capacity int, newBkt func() *tokenBucket) *clientTracker {
	if capacity < 1 {
		capacity = 1
	}
	return &clientTracker{
		capacity: capacity,
		ll:       list.New(),
		items:    make(map[string]*list.Element),
		newBkt:   newBkt,
	}
}

// get returns the bucket for key, creating it if necessary, and marks the key
// as most-recently-used.
func (t *clientTracker) get(key string) *tokenBucket {
	t.mu.Lock()
	defer t.mu.Unlock()

	if el, ok := t.items[key]; ok {
		t.ll.MoveToFront(el)
		return el.Value.(*clientEntry).bucket
	}

	ent := &clientEntry{key: key, bucket: t.newBkt()}
	el := t.ll.PushFront(ent)
	t.items[key] = el

	for t.ll.Len() > t.capacity {
		back := t.ll.Back()
		if back == nil {
			break
		}
		t.ll.Remove(back)
		delete(t.items, back.Value.(*clientEntry).key)
	}
	return ent.bucket
}

func (t *clientTracker) len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ll.Len()
}
