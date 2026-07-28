package scenarios

//go:generate stringer -type=StressID -output=id_string.go
//go:generate gofmt -l -s -w .
//go:generate goimports -w .
//go:generate go build -v .

// StressID defines stress test scenario IDs
// Same pattern as conformance ID.
type StressID int

const (
	// ConcurrentPuts Stress scenario IDs - same naming pattern as conformance
	// Simulates the overlapping GuaranteedUpdate calls kube-apiserver issues via staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go when many controllers write simultaneously.
	ConcurrentPuts StressID = iota
	// BurstWrites Exercises the short spike of KV.Put traffic triggered by Kubernetes resync loops committing objects through store.go.
	BurstWrites
	// SustainedLoad Models continuous API writes from steady controllers so store.go's linearizable Put path stays responsive under load.
	SustainedLoad
	// RampUpLoad Covers the ramping QPS pattern kube-apiserver sees when new clusters scale up and store.go begins handling bulk creates.
	RampUpLoad
	// MixedWorkload Blends reads, writes, and deletes like real API traffic fanning through store.go and watcher.go.
	MixedWorkload
	// LargeValues Echoes how Kubernetes stores large ConfigMaps/Secrets; GuaranteedUpdate must stream big payloads without tripping etcd limits.
	LargeValues
	// ManyKeys Mirrors `/registry` expansion where store.go#List enumerates thousands of keys during namespace or CRD listing.
	ManyKeys
	// HighContention Stresses the ModRevision compare loop in store.go's GuaranteedUpdate when multiple writers compete for the same object.
	HighContention
	// SequentialWrites Reflects stateful workloads applying ordered updates that store.go must commit without gaps.
	SequentialWrites
	// RandomReads Matches kube-apiserver cache misses that call store.go#get on arbitrary resource keys.
	RandomReads
	// WatchProgressNotify Matches watcher.go's use of WithProgressNotify to emit bookmarks for long-lived API watches.
	WatchProgressNotify

	// LeaseIntensiveWorkload Kubernetes-specific stress patterns
	// staging/src/k8s.io/apiserver/pkg/storage/etcd3/lease_manager.go
	// Exercises lease_manager.go's TTL handling when many lease-backed objects churn (e.g., Node heartbeats).
	LeaseIntensiveWorkload
	// ListPaginationHeavy staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go "GetList" with continue
	// Simulates clients consuming large list responses where store.go#List produces continue tokens under pressure.
	ListPaginationHeavy
	// OptimisticConcurrencyTxn staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go "GuaranteedUpdate"
	// Targets the kubernetes.OptimisticPut/Delete transactions in vendor/go.etcd.io/etcd/client/v3/kubernetes/client.go.
	OptimisticConcurrencyTxn
	// WatchManyPrefixes staging/src/k8s.io/apiserver/pkg/storage/etcd3/watcher.go
	// Covers watcher.go's fan-out of prefix watches for dozens of resource collections at once.
	WatchManyPrefixes
	// CompactDuringLoad staging/src/k8s.io/apiserver/pkg/storage/etcd3/compact.go
	// Validates compact.go running background compaction while store.go continues to serve traffic.
	CompactDuringLoad
	// WatchWithChurn Pod lifecycle creates/destroys many watches
	// Exercises watcher.go restarting streams rapidly as pods or controllers connect and disconnect.
	WatchWithChurn
	// NamespaceIsolationHeavy staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go prefix-based isolation
	// Targets the per-namespace key prefixing enforced by store.go across multi-tenant clusters.
	NamespaceIsolationHeavy
	// TxnMultiKeyUpdate staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go atomic multi-key updates
	// Models multi-key Txn batches store.go issues during coordinated updates (e.g., etcd compactor elections).
	TxnMultiKeyUpdate
	// LeaderElectionContention staging/src/k8s.io/client-go/tools/leaderelection
	// Docs do not list a Kubernetes consumer of clientv3/concurrency leader election; retained to validate optional helper behavior.
	LeaderElectionContention
	// WatchBookmarkHeavy staging/src/k8s.io/apiserver/pkg/storage/etcd3/watcher.go bookmarks
	// Stresses watcher.go's bookmark emission path that powers Kubernetes watch bookmarks for large collections.
	WatchBookmarkHeavy
)
