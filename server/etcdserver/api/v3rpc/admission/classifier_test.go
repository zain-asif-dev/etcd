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
	"testing"

	"github.com/stretchr/testify/assert"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

func TestClassifyKnownMethods(t *testing.T) {
	tests := []struct {
		method string
		group  EndpointGroup
		prio   Priority
	}{
		{"/etcdserverpb.Maintenance/Status", GroupHealth, PriorityCritical},
		{"/etcdserverpb.Cluster/MemberList", GroupHealth, PriorityCritical},
		{"/grpc.health.v1.Health/Check", GroupHealth, PriorityCritical},
		{"/etcdserverpb.KV/Range", GroupRead, PriorityHigh},
		{"/etcdserverpb.Lease/LeaseTimeToLive", GroupRead, PriorityHigh},
		{"/etcdserverpb.KV/Put", GroupWrite, PriorityNormal},
		{"/etcdserverpb.KV/DeleteRange", GroupWrite, PriorityNormal},
		{"/etcdserverpb.Lease/LeaseGrant", GroupWrite, PriorityNormal},
		{"/etcdserverpb.Auth/AuthEnable", GroupWrite, PriorityNormal},
	}
	for _, tt := range tests {
		c := classify(tt.method, nil)
		assert.Equal(t, tt.group, c.group, tt.method)
		assert.Equal(t, tt.prio, c.priority, tt.method)
	}
}

func TestClassifyUnknownDefaultsToWrite(t *testing.T) {
	c := classify("/etcdserverpb.Future/NewMethod", nil)
	assert.Equal(t, GroupWrite, c.group)
	assert.Equal(t, PriorityNormal, c.priority)
}

func TestClassifyTxnReadOnly(t *testing.T) {
	roTxn := &pb.TxnRequest{
		Success: []*pb.RequestOp{
			{Request: &pb.RequestOp_RequestRange{RequestRange: &pb.RangeRequest{Key: []byte("a")}}},
		},
		Failure: []*pb.RequestOp{
			{Request: &pb.RequestOp_RequestRange{RequestRange: &pb.RangeRequest{Key: []byte("b")}}},
		},
	}
	c := classify("/etcdserverpb.KV/Txn", roTxn)
	assert.Equal(t, GroupRead, c.group)
	assert.Equal(t, PriorityHigh, c.priority)
}

func TestClassifyTxnMutating(t *testing.T) {
	rwTxn := &pb.TxnRequest{
		Success: []*pb.RequestOp{
			{Request: &pb.RequestOp_RequestPut{RequestPut: &pb.PutRequest{Key: []byte("a")}}},
		},
	}
	c := classify("/etcdserverpb.KV/Txn", rwTxn)
	assert.Equal(t, GroupWrite, c.group)
	assert.Equal(t, PriorityNormal, c.priority)
}

func TestClassifyTxnNestedMutating(t *testing.T) {
	nested := &pb.TxnRequest{
		Success: []*pb.RequestOp{
			{Request: &pb.RequestOp_RequestTxn{RequestTxn: &pb.TxnRequest{
				Success: []*pb.RequestOp{
					{Request: &pb.RequestOp_RequestDeleteRange{RequestDeleteRange: &pb.DeleteRangeRequest{Key: []byte("a")}}},
				},
			}}},
		},
	}
	c := classify("/etcdserverpb.KV/Txn", nested)
	assert.Equal(t, GroupWrite, c.group)
}

func TestClassifyStream(t *testing.T) {
	c := classifyStream("/etcdserverpb.Watch/Watch")
	assert.Equal(t, GroupStream, c.group)
	assert.Equal(t, PriorityHigh, c.priority)

	c = classifyStream("/grpc.health.v1.Health/Watch")
	assert.Equal(t, GroupHealth, c.group)

	c = classifyStream("/unknown/Stream")
	assert.Equal(t, GroupStream, c.group)
}

func TestEndpointGroupString(t *testing.T) {
	assert.Equal(t, "health", GroupHealth.String())
	assert.Equal(t, "read", GroupRead.String())
	assert.Equal(t, "write", GroupWrite.String())
	assert.Equal(t, "stream", GroupStream.String())
}
