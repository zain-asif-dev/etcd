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

	"google.golang.org/grpc/peer"
)

// unknownClientIdentity is returned when no peer information can be
// extracted from the request context.
const unknownClientIdentity = "unknown"

// ClientIdentityFromContext derives a stable client identity string from a
// gRPC request context. The peer address is used with the ephemeral port
// stripped, so all connections from the same host share a single identity
// for limiting purposes. Authenticated-user identity will be added later.
//
// Returns "peer:<host>" on success, or "unknown" if no peer information is
// available.
func ClientIdentityFromContext(ctx context.Context) string {
	pr, ok := peer.FromContext(ctx)
	if !ok || pr.Addr == nil {
		return unknownClientIdentity
	}
	addr := pr.Addr.String()
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// Address has no port (e.g. unix socket); use it verbatim.
		return "peer:" + addr
	}
	return "peer:" + host
}
