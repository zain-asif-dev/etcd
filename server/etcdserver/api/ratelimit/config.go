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

// Package ratelimit implements a token-bucket based, per-client and global
// request rate limiter for etcd's gRPC API server.
package ratelimit

import "fmt"

// Config holds rate limiter settings. The zero value (Enabled=false) disables
// rate limiting entirely.
type Config struct {
	// Enabled turns the limiter on. When false all requests are admitted and
	// the interceptors short-circuit immediately.
	Enabled bool

	// RequestsPerSecond is the global token-bucket refill rate applied across
	// all clients and endpoints.
	RequestsPerSecond int

	// BurstSize is the global token-bucket capacity (maximum burst).
	BurstSize int

	// PerClientRPS is the per-client token-bucket refill rate. Each tracked
	// client IP gets its own bucket with capacity equal to PerClientRPS.
	PerClientRPS int

	// MaxTrackedClients bounds the number of distinct client IPs tracked at
	// once. When exceeded, the least-recently-used client entry is evicted.
	MaxTrackedClients int

	// ReadWriteRatio controls the relative cost of read vs write operations.
	// Reads consume 1/ReadWriteRatio tokens, writes consume 1 token. A value
	// of 2.0 therefore allows roughly twice as many reads as writes.
	ReadWriteRatio float64
}

// Validate returns an error if the configuration is internally inconsistent.
// It is a no-op when Enabled is false.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.RequestsPerSecond <= 0 {
		return fmt.Errorf("ratelimit: requests-per-second must be > 0, got %d", c.RequestsPerSecond)
	}
	if c.BurstSize <= 0 {
		return fmt.Errorf("ratelimit: burst-size must be > 0, got %d", c.BurstSize)
	}
	if c.PerClientRPS <= 0 {
		return fmt.Errorf("ratelimit: per-client-rps must be > 0, got %d", c.PerClientRPS)
	}
	if c.MaxTrackedClients <= 0 {
		return fmt.Errorf("ratelimit: max-tracked-clients must be > 0, got %d", c.MaxTrackedClients)
	}
	if c.ReadWriteRatio <= 0 {
		return fmt.Errorf("ratelimit: read-write-ratio must be > 0, got %v", c.ReadWriteRatio)
	}
	return nil
}
