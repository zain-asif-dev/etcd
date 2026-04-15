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
	"context"
	"net"
	"testing"

	"google.golang.org/grpc/peer"
)

func TestClientIdentityFromContext(t *testing.T) {
	tcpAddr := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 54321}
	unixAddr := &net.UnixAddr{Name: "/var/run/etcd.sock", Net: "unix"}

	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{
			name: "tcp peer strips ephemeral port",
			ctx:  peer.NewContext(context.Background(), &peer.Peer{Addr: tcpAddr}),
			want: "peer:10.0.0.1",
		},
		{
			name: "unix socket with no port keeps full path",
			ctx:  peer.NewContext(context.Background(), &peer.Peer{Addr: unixAddr}),
			want: "peer:" + unixAddr.String(),
		},
		{
			name: "no peer info returns unknown",
			ctx:  context.Background(),
			want: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClientIdentityFromContext(tt.ctx)
			if got != tt.want {
				t.Errorf("ClientIdentityFromContext() = %q, want %q", got, tt.want)
			}
		})
	}
}
