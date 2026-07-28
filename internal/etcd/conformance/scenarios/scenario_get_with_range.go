package scenarios

import (
	"errors"
	"fmt"
	"path"
	"reflect"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunGetWithRange tests the GetWithRange scenario.
func RunGetWithRange(runner Runner) {
	logutil.S().Infow("running", "scenario", GetWithRange.String())

	result := &Result{
		Scenario:  GetWithRange.String(),
		TimeStart: testtime.Now(),
		Success:   true,
		Output:    "ok",
	}
	defer func() {
		result.RecordTimeEnd(testtime.Now())
		runner.RecordResult(*result)
	}()

	cli, err := runner.NewClient()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create client: %v", err)

		return
	}
	defer func() { _ = cli.Close() }()

	testKey := runner.GenerateRandomKey(30)

	// Clean up any existing test keys from previous runs.
	// IMPORTANT: Using "\x00" with WithFromKey() would delete ALL keys in etcd,
	// including Kubernetes data under /registry/. Always scope deletions
	// to the test prefix when running against a live cluster.
	ctx, cancel := runner.NewCtx()
	dresp, err := cli.Delete(ctx, testKey, clientv3.WithPrefix())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to cleanup test keys: %v", err)

		return
	}
	latestRev := dresp.Header.GetRevision()

	compactStart := time.Now()

	ctx, cancel = runner.NewCtx()
	_, err = cli.Compact(
		ctx,
		latestRev,
		clientv3.WithCompactPhysical(),
	)
	cancel()
	if err != nil && !errors.Is(err, rpctypes.ErrCompacted) {
		result.Success = false
		result.Output = fmt.Sprintf("failed to compact: %v", err)

		return
	}
	logutil.S().Info("discarded historical revisions", zap.Duration("took", time.Since(compactStart)))

	// Track actual revisions from each Put to handle concurrent etcd writes
	// (Kubernetes control plane may write between our operations)
	type keyRev struct {
		createRev int64
		modRev    int64
		version   int64
	}
	keyRevisions := make(map[string]*keyRev)

	keySet := []string{
		path.Join(testKey, "a"),
		path.Join(testKey, "b"),
		path.Join(testKey, "c"),
		path.Join(testKey, "c"),
		path.Join(testKey, "c"),
		path.Join(testKey, "foo"),
		path.Join(testKey, "foo/abc"),
		path.Join(testKey, "fop"),
	}
	for i, key := range keySet {
		putCtx, putCancel := runner.NewCtx()
		resp, putErr := cli.Put(putCtx, key, "")
		putCancel()
		if putErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put #%d: %v", i, putErr)

			return
		}
		rev := resp.Header.GetRevision()
		if existing, ok := keyRevisions[key]; ok {
			// Key already exists, update mod revision and version
			existing.modRev = rev
			existing.version++
		} else {
			// New key
			keyRevisions[key] = &keyRev{createRev: rev, modRev: rev, version: 1}
		}
	}

	// Helper to get revision info for a key
	getKeyRev := func(suffix string) *keyRev {
		return keyRevisions[path.Join(testKey, suffix)]
	}

	// Use captured revisions for expected values
	revA := getKeyRev("a")
	revB := getKeyRev("b")
	revC := getKeyRev("c")
	revFoo := getKeyRev("foo")
	revFooAbc := getKeyRev("foo/abc")
	revFop := getKeyRev("fop")

	tests := []struct {
		begin   string
		end     string
		rev     int64
		opts    []clientv3.OpOption
		wantSet []*mvccpb.KeyValue
	}{
		// range first two
		{
			begin: path.Join(testKey, "a"),
			end:   path.Join(testKey, "c"),
			rev:   0,
			opts:  nil,
			wantSet: []*mvccpb.KeyValue{
				{
					Key:            []byte(path.Join(testKey, "a")),
					Value:          nil,
					CreateRevision: revA.createRev,
					ModRevision:    revA.modRev,
					Version:        revA.version,
				},
				{
					Key:            []byte(path.Join(testKey, "b")),
					Value:          nil,
					CreateRevision: revB.createRev,
					ModRevision:    revB.modRev,
					Version:        revB.version,
				},
			},
		},

		// range first two with serializable
		{
			begin: path.Join(testKey, "a"),
			end:   path.Join(testKey, "c"),
			rev:   0,
			opts:  []clientv3.OpOption{clientv3.WithSerializable()},
			wantSet: []*mvccpb.KeyValue{
				{
					Key:            []byte(path.Join(testKey, "a")),
					Value:          nil,
					CreateRevision: revA.createRev,
					ModRevision:    revA.modRev,
					Version:        revA.version,
				},
				{
					Key:            []byte(path.Join(testKey, "b")),
					Value:          nil,
					CreateRevision: revB.createRev,
					ModRevision:    revB.modRev,
					Version:        revB.version,
				},
			},
		},

		// range all with rev
		{
			begin: path.Join(testKey, "a"),
			end:   path.Join(testKey, "x"),
			rev:   revA.createRev,
			opts:  nil,
			wantSet: []*mvccpb.KeyValue{
				{
					Key:            []byte(path.Join(testKey, "a")),
					Value:          nil,
					CreateRevision: revA.createRev,
					ModRevision:    revA.modRev,
					Version:        1,
				},
			},
		},

		// range all with countOnly
		{
			begin:   path.Join(testKey, "a"),
			end:     path.Join(testKey, "x"),
			rev:     revA.createRev,
			opts:    []clientv3.OpOption{clientv3.WithCountOnly()},
			wantSet: nil,
		},

		// range all with SortByKey, SortAscend
		{
			begin: path.Join(testKey, "a"),
			end:   path.Join(testKey, "x"),
			rev:   0,
			opts:  []clientv3.OpOption{clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend)},
			wantSet: []*mvccpb.KeyValue{
				{
					Key:            []byte(path.Join(testKey, "a")),
					Value:          nil,
					CreateRevision: revA.createRev,
					ModRevision:    revA.modRev,
					Version:        revA.version,
				},
				{
					Key:            []byte(path.Join(testKey, "b")),
					Value:          nil,
					CreateRevision: revB.createRev,
					ModRevision:    revB.modRev,
					Version:        revB.version,
				},
				{
					Key:            []byte(path.Join(testKey, "c")),
					Value:          nil,
					CreateRevision: revC.createRev,
					ModRevision:    revC.modRev,
					Version:        revC.version,
				},
				{
					Key:            []byte(path.Join(testKey, "foo")),
					Value:          nil,
					CreateRevision: revFoo.createRev,
					ModRevision:    revFoo.modRev,
					Version:        revFoo.version,
				},
				{
					Key:            []byte(path.Join(testKey, "foo/abc")),
					Value:          nil,
					CreateRevision: revFooAbc.createRev,
					ModRevision:    revFooAbc.modRev,
					Version:        revFooAbc.version,
				},
				{
					Key:            []byte(path.Join(testKey, "fop")),
					Value:          nil,
					CreateRevision: revFop.createRev,
					ModRevision:    revFop.modRev,
					Version:        revFop.version,
				},
			},
		},

		// range all with SortByKey, missing sorting order (ASCEND by default)
		{
			begin: path.Join(testKey, "a"),
			end:   path.Join(testKey, "x"),
			rev:   0,
			opts:  []clientv3.OpOption{clientv3.WithSort(clientv3.SortByKey, clientv3.SortNone)},
			wantSet: []*mvccpb.KeyValue{
				{
					Key:            []byte(path.Join(testKey, "a")),
					Value:          nil,
					CreateRevision: revA.createRev,
					ModRevision:    revA.modRev,
					Version:        revA.version,
				},
				{
					Key:            []byte(path.Join(testKey, "b")),
					Value:          nil,
					CreateRevision: revB.createRev,
					ModRevision:    revB.modRev,
					Version:        revB.version,
				},
				{
					Key:            []byte(path.Join(testKey, "c")),
					Value:          nil,
					CreateRevision: revC.createRev,
					ModRevision:    revC.modRev,
					Version:        revC.version,
				},
				{
					Key:            []byte(path.Join(testKey, "foo")),
					Value:          nil,
					CreateRevision: revFoo.createRev,
					ModRevision:    revFoo.modRev,
					Version:        revFoo.version,
				},
				{
					Key:            []byte(path.Join(testKey, "foo/abc")),
					Value:          nil,
					CreateRevision: revFooAbc.createRev,
					ModRevision:    revFooAbc.modRev,
					Version:        revFooAbc.version,
				},
				{
					Key:            []byte(path.Join(testKey, "fop")),
					Value:          nil,
					CreateRevision: revFop.createRev,
					ModRevision:    revFop.modRev,
					Version:        revFop.version,
				},
			},
		},

		// range all with SortByCreateRevision, SortDescend
		{
			begin: path.Join(testKey, "a"),
			end:   path.Join(testKey, "x"),
			rev:   0,
			opts:  []clientv3.OpOption{clientv3.WithSort(clientv3.SortByCreateRevision, clientv3.SortDescend)},
			wantSet: []*mvccpb.KeyValue{
				{
					Key:            []byte(path.Join(testKey, "fop")),
					Value:          nil,
					CreateRevision: revFop.createRev,
					ModRevision:    revFop.modRev,
					Version:        revFop.version,
				},
				{
					Key:            []byte(path.Join(testKey, "foo/abc")),
					Value:          nil,
					CreateRevision: revFooAbc.createRev,
					ModRevision:    revFooAbc.modRev,
					Version:        revFooAbc.version,
				},
				{
					Key:            []byte(path.Join(testKey, "foo")),
					Value:          nil,
					CreateRevision: revFoo.createRev,
					ModRevision:    revFoo.modRev,
					Version:        revFoo.version,
				},
				{
					Key:            []byte(path.Join(testKey, "c")),
					Value:          nil,
					CreateRevision: revC.createRev,
					ModRevision:    revC.modRev,
					Version:        revC.version,
				},
				{
					Key:            []byte(path.Join(testKey, "b")),
					Value:          nil,
					CreateRevision: revB.createRev,
					ModRevision:    revB.modRev,
					Version:        revB.version,
				},
				{
					Key:            []byte(path.Join(testKey, "a")),
					Value:          nil,
					CreateRevision: revA.createRev,
					ModRevision:    revA.modRev,
					Version:        revA.version,
				},
			},
		},

		// range all with SortByCreateRevision, missing sorting order (ASCEND by default)
		{
			begin: path.Join(testKey, "a"),
			end:   path.Join(testKey, "x"),
			rev:   0,
			opts:  []clientv3.OpOption{clientv3.WithSort(clientv3.SortByCreateRevision, clientv3.SortNone)},
			wantSet: []*mvccpb.KeyValue{
				{
					Key:            []byte(path.Join(testKey, "a")),
					Value:          nil,
					CreateRevision: revA.createRev,
					ModRevision:    revA.modRev,
					Version:        revA.version,
				},
				{
					Key:            []byte(path.Join(testKey, "b")),
					Value:          nil,
					CreateRevision: revB.createRev,
					ModRevision:    revB.modRev,
					Version:        revB.version,
				},
				{
					Key:            []byte(path.Join(testKey, "c")),
					Value:          nil,
					CreateRevision: revC.createRev,
					ModRevision:    revC.modRev,
					Version:        revC.version,
				},
				{
					Key:            []byte(path.Join(testKey, "foo")),
					Value:          nil,
					CreateRevision: revFoo.createRev,
					ModRevision:    revFoo.modRev,
					Version:        revFoo.version,
				},
				{
					Key:            []byte(path.Join(testKey, "foo/abc")),
					Value:          nil,
					CreateRevision: revFooAbc.createRev,
					ModRevision:    revFooAbc.modRev,
					Version:        revFooAbc.version,
				},
				{
					Key:            []byte(path.Join(testKey, "fop")),
					Value:          nil,
					CreateRevision: revFop.createRev,
					ModRevision:    revFop.modRev,
					Version:        revFop.version,
				},
			},
		},

		// range all with SortByModRevision, SortDescend
		{
			begin: path.Join(testKey, "a"),
			end:   path.Join(testKey, "x"),
			rev:   0,
			opts:  []clientv3.OpOption{clientv3.WithSort(clientv3.SortByModRevision, clientv3.SortDescend)},
			wantSet: []*mvccpb.KeyValue{
				{
					Key:            []byte(path.Join(testKey, "fop")),
					Value:          nil,
					CreateRevision: revFop.createRev,
					ModRevision:    revFop.modRev,
					Version:        revFop.version,
				},
				{
					Key:            []byte(path.Join(testKey, "foo/abc")),
					Value:          nil,
					CreateRevision: revFooAbc.createRev,
					ModRevision:    revFooAbc.modRev,
					Version:        revFooAbc.version,
				},
				{
					Key:            []byte(path.Join(testKey, "foo")),
					Value:          nil,
					CreateRevision: revFoo.createRev,
					ModRevision:    revFoo.modRev,
					Version:        revFoo.version,
				},
				{
					Key:            []byte(path.Join(testKey, "c")),
					Value:          nil,
					CreateRevision: revC.createRev,
					ModRevision:    revC.modRev,
					Version:        revC.version,
				},
				{
					Key:            []byte(path.Join(testKey, "b")),
					Value:          nil,
					CreateRevision: revB.createRev,
					ModRevision:    revB.modRev,
					Version:        revB.version,
				},
				{
					Key:            []byte(path.Join(testKey, "a")),
					Value:          nil,
					CreateRevision: revA.createRev,
					ModRevision:    revA.modRev,
					Version:        revA.version,
				},
			},
		},

		// WithPrefix
		{
			begin: path.Join(testKey, "foo"),
			end:   "",
			rev:   0,
			opts:  []clientv3.OpOption{clientv3.WithPrefix()},
			wantSet: []*mvccpb.KeyValue{
				{
					Key:            []byte(path.Join(testKey, "foo")),
					Value:          nil,
					CreateRevision: revFoo.createRev,
					ModRevision:    revFoo.modRev,
					Version:        revFoo.version,
				},
				{
					Key:            []byte(path.Join(testKey, "foo/abc")),
					Value:          nil,
					CreateRevision: revFooAbc.createRev,
					ModRevision:    revFooAbc.modRev,
					Version:        revFooAbc.version,
				},
			},
		},

		// WithFromKey
		{
			begin: path.Join(testKey, "fo"),
			end:   "",
			rev:   0,
			opts:  []clientv3.OpOption{clientv3.WithFromKey()},
			wantSet: []*mvccpb.KeyValue{
				{
					Key:            []byte(path.Join(testKey, "foo")),
					Value:          nil,
					CreateRevision: revFoo.createRev,
					ModRevision:    revFoo.modRev,
					Version:        revFoo.version,
				},
				{
					Key:            []byte(path.Join(testKey, "foo/abc")),
					Value:          nil,
					CreateRevision: revFooAbc.createRev,
					ModRevision:    revFooAbc.modRev,
					Version:        revFooAbc.version,
				},
				{
					Key:            []byte(path.Join(testKey, "fop")),
					Value:          nil,
					CreateRevision: revFop.createRev,
					ModRevision:    revFop.modRev,
					Version:        revFop.version,
				},
			},
		},

		// fetch entire keyspace using WithFromKey
		{
			begin: "\x00",
			end:   "",
			rev:   0,
			opts: []clientv3.OpOption{
				clientv3.WithFromKey(),
				clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
			},
			wantSet: []*mvccpb.KeyValue{
				{
					Key:            []byte(path.Join(testKey, "a")),
					Value:          nil,
					CreateRevision: revA.createRev,
					ModRevision:    revA.modRev,
					Version:        revA.version,
				},
				{
					Key:            []byte(path.Join(testKey, "b")),
					Value:          nil,
					CreateRevision: revB.createRev,
					ModRevision:    revB.modRev,
					Version:        revB.version,
				},
				{
					Key:            []byte(path.Join(testKey, "c")),
					Value:          nil,
					CreateRevision: revC.createRev,
					ModRevision:    revC.modRev,
					Version:        revC.version,
				},
				{
					Key:            []byte(path.Join(testKey, "foo")),
					Value:          nil,
					CreateRevision: revFoo.createRev,
					ModRevision:    revFoo.modRev,
					Version:        revFoo.version,
				},
				{
					Key:            []byte(path.Join(testKey, "foo/abc")),
					Value:          nil,
					CreateRevision: revFooAbc.createRev,
					ModRevision:    revFooAbc.modRev,
					Version:        revFooAbc.version,
				},
				{
					Key:            []byte(path.Join(testKey, "fop")),
					Value:          nil,
					CreateRevision: revFop.createRev,
					ModRevision:    revFop.modRev,
					Version:        revFop.version,
				},
			},
		},

		// fetch entire keyspace using WithPrefix
		{
			begin: "",
			end:   "",
			rev:   0,
			opts: []clientv3.OpOption{
				clientv3.WithPrefix(),
				clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
			},
			wantSet: []*mvccpb.KeyValue{
				{
					Key:            []byte(path.Join(testKey, "a")),
					Value:          nil,
					CreateRevision: revA.createRev,
					ModRevision:    revA.modRev,
					Version:        revA.version,
				},
				{
					Key:            []byte(path.Join(testKey, "b")),
					Value:          nil,
					CreateRevision: revB.createRev,
					ModRevision:    revB.modRev,
					Version:        revB.version,
				},
				{
					Key:            []byte(path.Join(testKey, "c")),
					Value:          nil,
					CreateRevision: revC.createRev,
					ModRevision:    revC.modRev,
					Version:        revC.version,
				},
				{
					Key:            []byte(path.Join(testKey, "foo")),
					Value:          nil,
					CreateRevision: revFoo.createRev,
					ModRevision:    revFoo.modRev,
					Version:        revFoo.version,
				},
				{
					Key:            []byte(path.Join(testKey, "foo/abc")),
					Value:          nil,
					CreateRevision: revFooAbc.createRev,
					ModRevision:    revFooAbc.modRev,
					Version:        revFooAbc.version,
				},
				{
					Key:            []byte(path.Join(testKey, "fop")),
					Value:          nil,
					CreateRevision: revFop.createRev,
					ModRevision:    revFop.modRev,
					Version:        revFop.version,
				},
			},
		},

		// fetch keyspace with empty key using WithFromKey
		{
			begin: "",
			end:   "",
			rev:   0,
			opts: []clientv3.OpOption{
				clientv3.WithFromKey(),
				clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
			},
			wantSet: []*mvccpb.KeyValue{
				{
					Key:            []byte(path.Join(testKey, "a")),
					Value:          nil,
					CreateRevision: revA.createRev,
					ModRevision:    revA.modRev,
					Version:        revA.version,
				},
				{
					Key:            []byte(path.Join(testKey, "b")),
					Value:          nil,
					CreateRevision: revB.createRev,
					ModRevision:    revB.modRev,
					Version:        revB.version,
				},
				{
					Key:            []byte(path.Join(testKey, "c")),
					Value:          nil,
					CreateRevision: revC.createRev,
					ModRevision:    revC.modRev,
					Version:        revC.version,
				},
				{
					Key:            []byte(path.Join(testKey, "foo")),
					Value:          nil,
					CreateRevision: revFoo.createRev,
					ModRevision:    revFoo.modRev,
					Version:        revFoo.version,
				},
				{
					Key:            []byte(path.Join(testKey, "foo/abc")),
					Value:          nil,
					CreateRevision: revFooAbc.createRev,
					ModRevision:    revFooAbc.modRev,
					Version:        revFooAbc.version,
				},
				{
					Key:            []byte(path.Join(testKey, "fop")),
					Value:          nil,
					CreateRevision: revFop.createRev,
					ModRevision:    revFop.modRev,
					Version:        revFop.version,
				},
			},
		},
	}

	for i, tt := range tests {
		opts := make([]clientv3.OpOption, 0, 2+len(tt.opts))
		opts = append(opts,
			clientv3.WithRange(tt.end),
			clientv3.WithRev(tt.rev),
		)
		opts = append(opts, tt.opts...)

		// Use runner's default timeout for range queries in high-latency cloud/VPN environments
		// (e.g., cross-DC WireGuard networks). Some queries can return large datasets.
		ctx, cancel = runner.NewCtx()
		resp, err := cli.Get(ctx, tt.begin, opts...)
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("#%d: couldn't range (%v)", i, err)

			return
		}
		// NOTE: We don't check `getHeaderAfterPuts.Revision == resp.Header.Revision`
		// because in a live Kubernetes cluster, concurrent etcd operations can
		// advance the revision between calls. The test verifies range query
		// semantics using historical revisions (WithRev), not current revision stability.

		// Filter response to only include keys with the test prefix.
		// NOTE: Since etcd tests now run BEFORE kube-apiserver starts, the etcd
		// keyspace should be clean. However, we keep this filtering as defensive
		// coding in case the tests are run against an existing etcd for debugging.
		var filteredKvs []*mvccpb.KeyValue
		for _, kv := range resp.Kvs {
			if len(kv.Key) >= len(testKey) && string(kv.Key[:len(testKey)]) == testKey {
				filteredKvs = append(filteredKvs, kv)
			}
		}
		if !reflect.DeepEqual(tt.wantSet, filteredKvs) {
			result.Success = false
			result.Output = fmt.Sprintf("#%d: resp.Kvs expected %+v, got %+v", i, tt.wantSet, filteredKvs)

			return
		}
	}
}
