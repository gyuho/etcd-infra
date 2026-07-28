package scenarios

import (
	"fmt"
	"path"

	clientv3 "go.etcd.io/etcd/client/v3"

	"git.tbd/etcd-infra/pkg/randutil"
)

// Common test constants.
const (
	// leasingValueBar is a standard test value used in leasing tests.
	leasingValueBar = "bar"
)

func createKV(pfx, k, v string) keyValue {
	return keyValue{k: path.Join(pfx, k), v: v}
}

type keyValue struct {
	k string
	v string
}

type clientResponse struct {
	key     string
	putResp *clientv3.PutResponse

	getRevRequested int64
	getResp         *clientv3.GetResponse

	err error
}

func createRandCmps(pfx string, rss []*clientv3.PutResponse) ([]clientv3.Cmp, bool) {
	cmps := make([]clientv3.Cmp, 0, len(rss))
	for range rss {
		idx := randutil.Intn(len(rss))
		if rss[idx] == nil || rss[idx].Header == nil {
			return nil, false
		}
		k := fmt.Sprintf("%s%d", pfx, idx)
		rev := rss[idx].Header.Revision
		var cmp clientv3.Cmp
		switch randutil.Intn(4) {
		case 0:
			cmp = clientv3.Compare(clientv3.CreateRevision(k), ">", rev-1)
		case 1:
			cmp = clientv3.Compare(clientv3.Version(k), "=", 1)
		case 2:
			cmp = clientv3.Compare(clientv3.CreateRevision(k), "=", rev)
		case 3:
			cmp = clientv3.Compare(clientv3.CreateRevision(k), "!=", rev+1)
		}
		cmps = append(cmps, cmp)
	}
	cmps = cmps[:randutil.Intn(len(rss))]
	if randutil.Intn(2) == 0 {
		return cmps, true
	}
	idx := randutil.Intn(len(rss))
	cmps = append(cmps, clientv3.Compare(clientv3.Version(fmt.Sprintf("%s%d", pfx, idx)), "=", 0))

	return cmps, false
}
