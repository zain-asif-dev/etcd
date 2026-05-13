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
	"testing"

	"go.etcd.io/etcd/server/v3/config"
)

func TestClassifyWatch(t *testing.T) {
	tests := []struct {
		name string
		key  string
		cfg  *config.WatchLimitConfig
		want WatchPriority
	}{
		{
			name: "critical prefix match",
			key:  "/leases/foo",
			cfg: &config.WatchLimitConfig{
				CriticalKeyPrefixes:    []string{"/leases/"},
				LowPriorityKeyPrefixes: []string{"/events/"},
			},
			want: WatchPriorityCritical,
		},
		{
			name: "low priority prefix match",
			key:  "/events/pod-123",
			cfg: &config.WatchLimitConfig{
				CriticalKeyPrefixes:    []string{"/leases/"},
				LowPriorityKeyPrefixes: []string{"/events/"},
			},
			want: WatchPriorityLow,
		},
		{
			name: "no prefix match returns normal",
			key:  "/registry/pods/default/foo",
			cfg: &config.WatchLimitConfig{
				CriticalKeyPrefixes:    []string{"/leases/"},
				LowPriorityKeyPrefixes: []string{"/events/"},
			},
			want: WatchPriorityNormal,
		},
		{
			name: "matches both critical and low - critical wins",
			key:  "/shared/key",
			cfg: &config.WatchLimitConfig{
				CriticalKeyPrefixes:    []string{"/shared/"},
				LowPriorityKeyPrefixes: []string{"/shared/"},
			},
			want: WatchPriorityCritical,
		},
		{
			name: "nil config returns normal",
			key:  "/anything",
			cfg:  nil,
			want: WatchPriorityNormal,
		},
		{
			name: "empty prefix slices return normal",
			key:  "/anything",
			cfg: &config.WatchLimitConfig{
				CriticalKeyPrefixes:    []string{},
				LowPriorityKeyPrefixes: []string{},
			},
			want: WatchPriorityNormal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyWatch([]byte(tt.key), tt.cfg)
			if got != tt.want {
				t.Errorf("ClassifyWatch(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}
