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

import "strings"

type opKind int

const (
	opWrite opKind = iota
	opRead
	opExempt
)

// methodKinds classifies known etcd gRPC full-method names. Anything not
// listed is treated as a write (most conservative).
//
// Exempt methods are never rate limited so that operators can always
// authenticate, watch, keep leases alive, take snapshots and run health
// checks even when the cluster is otherwise saturated.
var methodKinds = map[string]opKind{
	// KV
	"/etcdserverpb.KV/Range":       opRead,
	"/etcdserverpb.KV/Put":         opWrite,
	"/etcdserverpb.KV/DeleteRange": opWrite,
	"/etcdserverpb.KV/Txn":         opWrite,
	"/etcdserverpb.KV/Compact":     opWrite,

	// Lease
	"/etcdserverpb.Lease/LeaseGrant":      opWrite,
	"/etcdserverpb.Lease/LeaseRevoke":     opWrite,
	"/etcdserverpb.Lease/LeaseTimeToLive": opRead,
	"/etcdserverpb.Lease/LeaseLeases":     opRead,
	"/etcdserverpb.Lease/LeaseKeepAlive":  opExempt,

	// Cluster
	"/etcdserverpb.Cluster/MemberList":    opRead,
	"/etcdserverpb.Cluster/MemberAdd":     opWrite,
	"/etcdserverpb.Cluster/MemberRemove":  opWrite,
	"/etcdserverpb.Cluster/MemberUpdate":  opWrite,
	"/etcdserverpb.Cluster/MemberPromote": opWrite,

	// Maintenance
	"/etcdserverpb.Maintenance/Status":     opRead,
	"/etcdserverpb.Maintenance/Hash":       opRead,
	"/etcdserverpb.Maintenance/HashKV":     opRead,
	"/etcdserverpb.Maintenance/Alarm":      opRead,
	"/etcdserverpb.Maintenance/Defragment": opWrite,
	"/etcdserverpb.Maintenance/MoveLeader": opWrite,
	"/etcdserverpb.Maintenance/Downgrade":  opWrite,
	"/etcdserverpb.Maintenance/Snapshot":   opExempt,

	// Watch
	"/etcdserverpb.Watch/Watch": opExempt,

	// Auth
	"/etcdserverpb.Auth/Authenticate":           opExempt,
	"/etcdserverpb.Auth/AuthStatus":             opRead,
	"/etcdserverpb.Auth/UserGet":                opRead,
	"/etcdserverpb.Auth/UserList":               opRead,
	"/etcdserverpb.Auth/RoleGet":                opRead,
	"/etcdserverpb.Auth/RoleList":               opRead,
	"/etcdserverpb.Auth/AuthEnable":             opWrite,
	"/etcdserverpb.Auth/AuthDisable":            opWrite,
	"/etcdserverpb.Auth/UserAdd":                opWrite,
	"/etcdserverpb.Auth/UserDelete":             opWrite,
	"/etcdserverpb.Auth/UserChangePassword":     opWrite,
	"/etcdserverpb.Auth/UserGrantRole":          opWrite,
	"/etcdserverpb.Auth/UserRevokeRole":         opWrite,
	"/etcdserverpb.Auth/RoleAdd":                opWrite,
	"/etcdserverpb.Auth/RoleDelete":             opWrite,
	"/etcdserverpb.Auth/RoleGrantPermission":    opWrite,
	"/etcdserverpb.Auth/RoleRevokePermission":   opWrite,

	// Health
	"/grpc.health.v1.Health/Check": opExempt,
	"/grpc.health.v1.Health/Watch": opExempt,
}

func classify(fullMethod string) opKind {
	if k, ok := methodKinds[fullMethod]; ok {
		return k
	}
	return opWrite
}

// endpointName returns the short method name (e.g. "Range") for use as a
// Prometheus label.
func endpointName(fullMethod string) string {
	if i := strings.LastIndex(fullMethod, "/"); i >= 0 && i < len(fullMethod)-1 {
		return fullMethod[i+1:]
	}
	return fullMethod
}
