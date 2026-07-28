//nolint:testpackage // Tests use package internals and shared resources.
package scenarios

import (
	"path"
	"testing"

	"github.com/stretchr/testify/require"
	etcdserverpb "go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	"git.tbd/etcd-infra/pkg/randutil"
)

func TestCreateKV(t *testing.T) {
	t.Parallel()
	kv := createKV("prefix", "key", "value")
	require.Equal(t, path.Join("prefix", "key"), kv.k)
	require.Equal(t, "value", kv.v)
}

func TestCreateRandCmps(t *testing.T) {
	t.Parallel()
	rss := []*clientv3.PutResponse{
		{Header: &etcdserverpb.ResponseHeader{Revision: 2}},
		{Header: &etcdserverpb.ResponseHeader{Revision: 5}},
	}

	seen := map[bool]bool{}
	for seed := int64(1); seed <= 50 && len(seen) < 2; seed++ {
		randutil.SetSeed(seed)
		cmps, then := createRandCmps("key", rss)
		require.LessOrEqual(t, len(cmps), len(rss))
		seen[then] = true
	}
	require.Len(t, seen, 2)
}
