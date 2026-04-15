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

package integration

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

// TestV3AdmissionDisabledByDefault verifies that with admission control
// disabled (the default), no requests are throttled.
func TestV3AdmissionDisabledByDefault(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	kvc := integration.ToGRPC(clus.RandClient()).KV
	for i := 0; i < 50; i++ {
		_, err := kvc.Put(t.Context(), &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar")})
		require.NoError(t, err)
	}
}

// TestV3AdmissionWriteRateLimit verifies that write requests beyond the
// configured rate are rejected with ResourceExhausted, the rate-limited
// error message, and a retry-after trailer.
func TestV3AdmissionWriteRateLimit(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{
		Size:                    1,
		AdmissionControlEnabled: true,
		RateLimitWrites:         5,
		RateLimitBurstFactor:    1.0,
	})
	defer clus.Terminate(t)

	kvc := integration.ToGRPC(clus.RandClient()).KV

	var admitted, rejected int
	var lastTrailer metadata.MD
	for i := 0; i < 50; i++ {
		var trailer metadata.MD
		_, err := kvc.Put(t.Context(),
			&pb.PutRequest{Key: []byte("foo"), Value: []byte("bar")},
			grpc.Trailer(&trailer))
		if err == nil {
			admitted++
			continue
		}
		st, ok := status.FromError(err)
		require.True(t, ok, "expected gRPC status error, got %v", err)
		require.Equal(t, codes.ResourceExhausted, st.Code())
		require.Equal(t, rpctypes.ErrorDesc(rpctypes.ErrGRPCRequestRateLimited), st.Message())
		rejected++
		lastTrailer = trailer
	}

	assert.GreaterOrEqual(t, admitted, 1, "at least one request should be admitted")
	assert.GreaterOrEqual(t, rejected, 1, "at least one request should be rate-limited")

	// Verify retry-after trailer on a rejected request.
	require.NotNil(t, lastTrailer)
	vals := lastTrailer.Get(rpctypes.MetadataRetryAfterMsKey)
	require.NotEmpty(t, vals, "expected %q trailer on rejected request", rpctypes.MetadataRetryAfterMsKey)
	ms, err := strconv.Atoi(vals[0])
	require.NoError(t, err)
	assert.Greater(t, ms, 0)
}

// TestV3AdmissionHealthBypass verifies that Maintenance/Status is never
// rejected even when read and write rate limits are exhausted.
func TestV3AdmissionHealthBypass(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{
		Size:                    1,
		AdmissionControlEnabled: true,
		RateLimitReads:          1,
		RateLimitWrites:         1,
		RateLimitBurstFactor:    1.0,
	})
	defer clus.Terminate(t)

	cli := clus.RandClient()
	kvc := integration.ToGRPC(cli).KV
	mvc := integration.ToGRPC(cli).Maintenance

	// Exhaust read and write buckets.
	for i := 0; i < 10; i++ {
		kvc.Put(t.Context(), &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar")})
		kvc.Range(t.Context(), &pb.RangeRequest{Key: []byte("foo")})
	}

	// Status must always succeed.
	for i := 0; i < 20; i++ {
		_, err := mvc.Status(t.Context(), &pb.StatusRequest{})
		require.NoError(t, err, "Status must never be rate-limited")
	}
}

// TestV3AdmissionReadWriteIndependent verifies that exhausting the write
// bucket does not affect reads.
func TestV3AdmissionReadWriteIndependent(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{
		Size:                    1,
		AdmissionControlEnabled: true,
		RateLimitWrites:         1,
		RateLimitBurstFactor:    1.0,
	})
	defer clus.Terminate(t)

	kvc := integration.ToGRPC(clus.RandClient()).KV

	// Drain writes.
	var sawWriteReject bool
	for i := 0; i < 20; i++ {
		_, err := kvc.Put(t.Context(), &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar")})
		if err != nil {
			sawWriteReject = true
		}
	}
	require.True(t, sawWriteReject)

	// Reads (unlimited) must still succeed.
	for i := 0; i < 20; i++ {
		_, err := kvc.Range(t.Context(), &pb.RangeRequest{Key: []byte("foo"), Serializable: true})
		require.NoError(t, err)
	}
}

// TestV3AdmissionRefill verifies that after waiting, previously
// rate-limited requests succeed again.
func TestV3AdmissionRefill(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{
		Size:                    1,
		AdmissionControlEnabled: true,
		RateLimitWrites:         50,
		RateLimitBurstFactor:    1.0,
	})
	defer clus.Terminate(t)

	kvc := integration.ToGRPC(clus.RandClient()).KV

	// Drain.
	for {
		_, err := kvc.Put(t.Context(), &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar")})
		if err != nil {
			break
		}
	}

	// Wait long enough for at least one token (20ms at 50qps; use 200ms).
	time.Sleep(200 * time.Millisecond)

	_, err := kvc.Put(t.Context(), &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar")})
	require.NoError(t, err, "expected at least one token to refill after sleep")
}
