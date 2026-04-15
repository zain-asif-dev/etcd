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
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/server/v3/etcdserver/api/ratelimit"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

func newRateLimitedCluster(t *testing.T, burst, rps, perClient int, ratio float64) *integration.Cluster {
	return integration.NewCluster(t, &integration.ClusterConfig{
		Size:                       1,
		RateLimitEnabled:           true,
		RateLimitRequestsPerSecond: rps,
		RateLimitBurstSize:         burst,
		RateLimitPerClientRPS:      perClient,
		RateLimitMaxTrackedClients: 100,
		RateLimitReadWriteRatio:    ratio,
	})
}

// TestV3RateLimitAllowsWithinLimit verifies that with rate limiting enabled
// and a generous burst, a small number of requests well under the limit all
// succeed.
func TestV3RateLimitAllowsWithinLimit(t *testing.T) {
	integration.BeforeTest(t)

	clus := newRateLimitedCluster(t, 100, 100, 100, 1.0)
	defer clus.Terminate(t)

	kvc := integration.ToGRPC(clus.Client(0)).KV
	for i := 0; i < 5; i++ {
		_, err := kvc.Put(t.Context(),
			&pb.PutRequest{Key: []byte(fmt.Sprintf("k%d", i)), Value: []byte("v")})
		require.NoErrorf(t, err, "put #%d should succeed within limit", i)
	}
}

// TestV3RateLimitRejectsExcess verifies that requests within the burst
// succeed, that excess requests receive ResourceExhausted with an informative
// message, and that the retry-after trailer carries a reasonable value.
func TestV3RateLimitRejectsExcess(t *testing.T) {
	integration.BeforeTest(t)

	const burst = 5
	clus := newRateLimitedCluster(t, burst, burst, burst, 1.0)
	defer clus.Terminate(t)

	kvc := integration.ToGRPC(clus.Client(0)).KV

	var allowed, rejected int
	var firstRejectErr error
	var trailer metadata.MD
	for i := 0; i < 20; i++ {
		var tr metadata.MD
		_, err := kvc.Put(t.Context(),
			&pb.PutRequest{Key: []byte(fmt.Sprintf("k%d", i)), Value: []byte("v")},
			grpc.Trailer(&tr))
		if err == nil {
			allowed++
			continue
		}
		require.Equal(t, codes.ResourceExhausted, status.Code(err),
			"rejection must use ResourceExhausted, got %v", err)
		if firstRejectErr == nil {
			firstRejectErr = err
			trailer = tr
		}
		rejected++
	}

	require.Greater(t, allowed, 0, "some requests must succeed")
	require.LessOrEqual(t, allowed, burst+2, "no more than ~burst requests should succeed (allow small refill jitter)")
	require.Greater(t, rejected, 0, "some requests must be rejected")

	require.ErrorContains(t, firstRejectErr, "rate limit exceeded")
	require.ErrorContains(t, firstRejectErr, "retry after")

	vals := trailer.Get(ratelimit.RetryAfterMetadataKey)
	require.NotEmpty(t, vals, "retry-after trailer must be set")
	ra, perr := strconv.ParseFloat(vals[0], 64)
	require.NoError(t, perr)
	require.Greater(t, ra, 0.0)
	require.LessOrEqual(t, ra, 1.5, "retry-after hint should be reasonable for rps=%d", burst)
}

// TestV3RateLimitReadWriteRatio verifies that with a read-write ratio of 2.0
// roughly twice as many reads are admitted as writes from the same bucket.
func TestV3RateLimitReadWriteRatio(t *testing.T) {
	const burst = 6
	const rps = 1 // slow refill so the burst dominates the test window

	run := func(t *testing.T, op func(kv pb.KVClient) error) int {
		integration.BeforeTest(t)
		clus := newRateLimitedCluster(t, burst, rps, 1000, 2.0)
		defer clus.Terminate(t)
		kv := integration.ToGRPC(clus.Client(0)).KV
		return countAllowed(t, 30, func() error { return op(kv) })
	}

	var writes, reads int
	t.Run("writes", func(t *testing.T) {
		writes = run(t, func(kv pb.KVClient) error {
			_, err := kv.Put(t.Context(), &pb.PutRequest{Key: []byte("k"), Value: []byte("v")})
			return err
		})
	})
	t.Run("reads", func(t *testing.T) {
		reads = run(t, func(kv pb.KVClient) error {
			_, err := kv.Range(t.Context(), &pb.RangeRequest{Key: []byte("k")})
			return err
		})
	})

	require.Greaterf(t, reads, writes,
		"reads (%d) should exceed writes (%d) under ratio=2.0", reads, writes)
	// Reads cost 0.5 each so we expect ~2x writes; allow generous slack for
	// real-clock refill during the loop.
	require.GreaterOrEqualf(t, float64(reads), 1.5*float64(writes),
		"reads (%d) should be at least 1.5x writes (%d)", reads, writes)
}

// TestV3RateLimitRecovery verifies that after the bucket is exhausted and
// time passes, requests are admitted again.
func TestV3RateLimitRecovery(t *testing.T) {
	integration.BeforeTest(t)

	clus := newRateLimitedCluster(t, 3, 10, 10, 1.0)
	defer clus.Terminate(t)
	kvc := integration.ToGRPC(clus.Client(0)).KV

	// Drain.
	var sawReject bool
	for i := 0; i < 30; i++ {
		_, err := kvc.Put(t.Context(), &pb.PutRequest{Key: []byte("k"), Value: []byte("v")})
		if err != nil {
			require.Equal(t, codes.ResourceExhausted, status.Code(err))
			sawReject = true
			break
		}
	}
	require.True(t, sawReject, "expected at least one rejection")

	// Wait long enough for the bucket to refill (capacity 3 at 10/s → 300ms;
	// per-client capacity 10 at 10/s → 1s).
	time.Sleep(1100 * time.Millisecond)

	_, err := kvc.Put(t.Context(), &pb.PutRequest{Key: []byte("k"), Value: []byte("v")})
	require.NoError(t, err, "request should succeed after bucket recovery")
}

// TestV3RateLimitDisabledNoEffect verifies that with rate limiting disabled
// (the default) a burst far exceeding any configured limit is admitted.
func TestV3RateLimitDisabledNoEffect(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)
	kvc := integration.ToGRPC(clus.Client(0)).KV

	for i := 0; i < 100; i++ {
		_, err := kvc.Put(t.Context(), &pb.PutRequest{Key: []byte("k"), Value: []byte("v")})
		require.NoError(t, err)
	}
}

func countAllowed(t *testing.T, n int, op func() error) int {
	t.Helper()
	allowed := 0
	for i := 0; i < n; i++ {
		if err := op(); err == nil {
			allowed++
		} else {
			require.Equal(t, codes.ResourceExhausted, status.Code(err))
		}
	}
	return allowed
}
