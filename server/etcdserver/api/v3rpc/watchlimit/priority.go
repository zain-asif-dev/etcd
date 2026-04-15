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
	"bytes"

	"go.etcd.io/etcd/server/v3/config"
)

// WatchPriority classifies a watch for the purposes of graceful degradation
// under memory pressure. Higher values are shed first; WatchPriorityCritical
// watches are never paused or cancelled by the limiter.
type WatchPriority int

const (
	// WatchPriorityCritical marks watches that must never be paused or
	// cancelled under memory pressure (e.g. lease keepalive, leader election).
	WatchPriorityCritical WatchPriority = iota
	// WatchPriorityNormal is the default priority for watches whose key does
	// not match any configured prefix.
	WatchPriorityNormal
	// WatchPriorityLow marks watches that are paused or cancelled first under
	// memory pressure.
	WatchPriorityLow
)

// ClassifyWatch returns the priority of a watch on key according to the
// configured key prefixes. CriticalKeyPrefixes take precedence over
// LowPriorityKeyPrefixes; if neither matches, WatchPriorityNormal is
// returned. A nil cfg is treated as having no configured prefixes.
func ClassifyWatch(key []byte, cfg *config.WatchLimitConfig) WatchPriority {
	if cfg != nil {
		for _, p := range cfg.CriticalKeyPrefixes {
			if bytes.HasPrefix(key, []byte(p)) {
				return WatchPriorityCritical
			}
		}
		for _, p := range cfg.LowPriorityKeyPrefixes {
			if bytes.HasPrefix(key, []byte(p)) {
				return WatchPriorityLow
			}
		}
	}
	return WatchPriorityNormal
}
