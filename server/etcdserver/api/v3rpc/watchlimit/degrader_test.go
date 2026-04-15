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

	"go.uber.org/zap/zaptest"

	"go.etcd.io/etcd/server/v3/config"
)

func TestDegraderCheckMemoryPressure(t *testing.T) {
	const limit = 1000

	tests := []struct {
		name      string
		softLimit int64
		estimated uint64
		want      DegradationLevel
	}{
		{
			name:      "zero soft limit always DegradeNone",
			softLimit: 0,
			estimated: 1 << 40,
			want:      DegradeNone,
		},
		{
			name:      "below 70 percent",
			softLimit: limit,
			estimated: 699,
			want:      DegradeNone,
		},
		{
			name:      "exactly 70 percent",
			softLimit: limit,
			estimated: 700,
			want:      DegradeReduceBatch,
		},
		{
			name:      "89 percent",
			softLimit: limit,
			estimated: 890,
			want:      DegradeReduceBatch,
		},
		{
			name:      "exactly 90 percent",
			softLimit: limit,
			estimated: 900,
			want:      DegradePauseAndCancel,
		},
		{
			name:      "above the limit",
			softLimit: limit,
			estimated: 2 * limit,
			want:      DegradePauseAndCancel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.WatchLimitConfig{MemorySoftLimitBytes: tt.softLimit}
			d := NewDegrader(cfg, zaptest.NewLogger(t))
			got := d.CheckMemoryPressure(tt.estimated)
			if got != tt.want {
				t.Errorf("CheckMemoryPressure(%d) = %v, want %v", tt.estimated, got, tt.want)
			}
		})
	}
}

func TestDegraderCurrentLevel(t *testing.T) {
	cfg := &config.WatchLimitConfig{MemorySoftLimitBytes: 1000}
	d := NewDegrader(cfg, zaptest.NewLogger(t))

	if got := d.CurrentLevel(); got != DegradeNone {
		t.Fatalf("initial CurrentLevel() = %v, want %v", got, DegradeNone)
	}

	steps := []struct {
		estimated uint64
		want      DegradationLevel
	}{
		{estimated: 750, want: DegradeReduceBatch},
		{estimated: 950, want: DegradePauseAndCancel},
		{estimated: 100, want: DegradeNone},
	}
	for _, s := range steps {
		ret := d.CheckMemoryPressure(s.estimated)
		if ret != s.want {
			t.Fatalf("CheckMemoryPressure(%d) = %v, want %v", s.estimated, ret, s.want)
		}
		if got := d.CurrentLevel(); got != s.want {
			t.Fatalf("CurrentLevel() after CheckMemoryPressure(%d) = %v, want %v", s.estimated, got, s.want)
		}
	}
}
