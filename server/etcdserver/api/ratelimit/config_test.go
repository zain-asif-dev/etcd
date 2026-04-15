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
	"testing"

	"github.com/stretchr/testify/require"
)

func validConfig() Config {
	return Config{
		Enabled:           true,
		RequestsPerSecond: 100,
		BurstSize:         10,
		PerClientRPS:      10,
		MaxTrackedClients: 100,
		ReadWriteRatio:    2.0,
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mut     func(*Config)
		wantErr bool
	}{
		{"disabled-anything-ok", func(c *Config) { *c = Config{} }, false},
		{"valid", func(c *Config) {}, false},
		{"zero-rps", func(c *Config) { c.RequestsPerSecond = 0 }, true},
		{"zero-burst", func(c *Config) { c.BurstSize = 0 }, true},
		{"zero-per-client", func(c *Config) { c.PerClientRPS = 0 }, true},
		{"zero-max-clients", func(c *Config) { c.MaxTrackedClients = 0 }, true},
		{"zero-ratio", func(c *Config) { c.ReadWriteRatio = 0 }, true},
		{"negative-ratio", func(c *Config) { c.ReadWriteRatio = -1 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.mut(&c)
			err := c.Validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
