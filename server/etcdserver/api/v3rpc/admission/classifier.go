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

package admission

import (
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

// EndpointGroup categorises an RPC for rate-limiting and scheduling purposes.
type EndpointGroup int

const (
	// GroupHealth covers status, health-check and cluster-introspection RPCs.
	// These are never rate-limited or shed: operators must always be able to
	// observe an overloaded server.
	GroupHealth EndpointGroup = iota
	// GroupRead covers linearizable/serializable reads and read-only Txns.
	GroupRead
	// GroupWrite covers all mutating RPCs that go through Raft.
	GroupWrite
	// GroupStream covers long-lived bidirectional/server streams.
	GroupStream
)

func (g EndpointGroup) String() string {
	switch g {
	case GroupHealth:
		return "health"
	case GroupRead:
		return "read"
	case GroupWrite:
		return "write"
	case GroupStream:
		return "stream"
	default:
		return "unknown"
	}
}

// Priority is the scheduling priority of a request. Lower values are more
// important and are shed last.
type Priority int

const (
	PriorityCritical Priority = iota
	PriorityHigh
	PriorityNormal
)

func (p Priority) String() string {
	switch p {
	case PriorityCritical:
		return "critical"
	case PriorityHigh:
		return "high"
	case PriorityNormal:
		return "normal"
	default:
		return "unknown"
	}
}

type endpointClass struct {
	group    EndpointGroup
	priority Priority
}

// methodClasses maps gRPC FullMethod strings to their endpoint group and
// scheduling priority. Unknown methods default to GroupWrite/PriorityNormal
// (fail safe: treat the unknown as the most restricted class).
var methodClasses = map[string]endpointClass{
	// Health / status / introspection — never shed.
	"/etcdserverpb.Maintenance/Status":  {GroupHealth, PriorityCritical},
	"/etcdserverpb.Maintenance/Alarm":   {GroupHealth, PriorityCritical},
	"/etcdserverpb.Maintenance/Hash":    {GroupHealth, PriorityCritical},
	"/etcdserverpb.Maintenance/HashKV":  {GroupHealth, PriorityCritical},
	"/etcdserverpb.Cluster/MemberList":  {GroupHealth, PriorityCritical},
	"/etcdserverpb.Auth/AuthStatus":     {GroupHealth, PriorityCritical},
	"/grpc.health.v1.Health/Check":      {GroupHealth, PriorityCritical},
	"/grpc.health.v1.Health/Watch":      {GroupHealth, PriorityCritical},
	"/etcdserverpb.Maintenance/Version": {GroupHealth, PriorityCritical},

	// Reads.
	"/etcdserverpb.KV/Range":              {GroupRead, PriorityHigh},
	"/etcdserverpb.Lease/LeaseTimeToLive": {GroupRead, PriorityHigh},
	"/etcdserverpb.Lease/LeaseLeases":     {GroupRead, PriorityHigh},

	// Writes.
	"/etcdserverpb.KV/Put":                       {GroupWrite, PriorityNormal},
	"/etcdserverpb.KV/DeleteRange":               {GroupWrite, PriorityNormal},
	"/etcdserverpb.KV/Compact":                   {GroupWrite, PriorityNormal},
	"/etcdserverpb.Lease/LeaseGrant":             {GroupWrite, PriorityNormal},
	"/etcdserverpb.Lease/LeaseRevoke":            {GroupWrite, PriorityNormal},
	"/etcdserverpb.Cluster/MemberAdd":            {GroupWrite, PriorityNormal},
	"/etcdserverpb.Cluster/MemberRemove":         {GroupWrite, PriorityNormal},
	"/etcdserverpb.Cluster/MemberUpdate":         {GroupWrite, PriorityNormal},
	"/etcdserverpb.Cluster/MemberPromote":        {GroupWrite, PriorityNormal},
	"/etcdserverpb.Maintenance/Defragment":       {GroupWrite, PriorityNormal},
	"/etcdserverpb.Maintenance/MoveLeader":       {GroupWrite, PriorityNormal},
	"/etcdserverpb.Maintenance/Downgrade":        {GroupWrite, PriorityNormal},
	"/etcdserverpb.Auth/AuthEnable":              {GroupWrite, PriorityNormal},
	"/etcdserverpb.Auth/AuthDisable":             {GroupWrite, PriorityNormal},
	"/etcdserverpb.Auth/Authenticate":            {GroupWrite, PriorityNormal},
	"/etcdserverpb.Auth/UserAdd":                 {GroupWrite, PriorityNormal},
	"/etcdserverpb.Auth/UserGet":                 {GroupWrite, PriorityNormal},
	"/etcdserverpb.Auth/UserList":                {GroupWrite, PriorityNormal},
	"/etcdserverpb.Auth/UserDelete":              {GroupWrite, PriorityNormal},
	"/etcdserverpb.Auth/UserChangePassword":      {GroupWrite, PriorityNormal},
	"/etcdserverpb.Auth/UserGrantRole":           {GroupWrite, PriorityNormal},
	"/etcdserverpb.Auth/UserRevokeRole":          {GroupWrite, PriorityNormal},
	"/etcdserverpb.Auth/RoleAdd":                 {GroupWrite, PriorityNormal},
	"/etcdserverpb.Auth/RoleGet":                 {GroupWrite, PriorityNormal},
	"/etcdserverpb.Auth/RoleList":                {GroupWrite, PriorityNormal},
	"/etcdserverpb.Auth/RoleDelete":              {GroupWrite, PriorityNormal},
	"/etcdserverpb.Auth/RoleGrantPermission":     {GroupWrite, PriorityNormal},
	"/etcdserverpb.Auth/RoleRevokePermission":    {GroupWrite, PriorityNormal},
	"/etcdserverpb.Maintenance/SerializableHash": {GroupWrite, PriorityNormal},

	// Streams.
	"/etcdserverpb.Watch/Watch":          {GroupStream, PriorityHigh},
	"/etcdserverpb.Lease/LeaseKeepAlive": {GroupStream, PriorityHigh},
	"/etcdserverpb.Maintenance/Snapshot": {GroupStream, PriorityHigh},
}

// classify returns the endpoint group and priority for a unary RPC. The req
// is consulted only for /etcdserverpb.KV/Txn so that read-only transactions
// are treated as reads.
func classify(fullMethod string, req any) endpointClass {
	if fullMethod == "/etcdserverpb.KV/Txn" {
		if txn, ok := req.(*pb.TxnRequest); ok && isReadOnlyTxn(txn) {
			return endpointClass{GroupRead, PriorityHigh}
		}
		return endpointClass{GroupWrite, PriorityNormal}
	}
	if c, ok := methodClasses[fullMethod]; ok {
		return c
	}
	return endpointClass{GroupWrite, PriorityNormal}
}

// classifyStream returns the endpoint group and priority for a stream RPC.
func classifyStream(fullMethod string) endpointClass {
	if c, ok := methodClasses[fullMethod]; ok {
		return c
	}
	return endpointClass{GroupStream, PriorityHigh}
}

func isReadOnlyTxn(txn *pb.TxnRequest) bool {
	for _, op := range txn.Success {
		if !isReadOnlyOp(op) {
			return false
		}
	}
	for _, op := range txn.Failure {
		if !isReadOnlyOp(op) {
			return false
		}
	}
	return true
}

func isReadOnlyOp(op *pb.RequestOp) bool {
	switch r := op.Request.(type) {
	case *pb.RequestOp_RequestRange:
		return true
	case *pb.RequestOp_RequestTxn:
		return isReadOnlyTxn(r.RequestTxn)
	default:
		return false
	}
}
