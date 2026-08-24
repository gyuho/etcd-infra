# etcd stress

Package `scenarios` defines stress workloads that mirror Kubernetes etcd usage.

> [!NOTE]
> **What "mirrors Kubernetes etcd usage" means concretely.** Kubernetes does not use etcd as a generic key-value store. It submits high-frequency concurrent writes from multiple controllers, bursts of API server requests during scale events, large values for ConfigMaps and Secrets, high-contention transactions during leader election, and sustained watch streams from informer caches. These workloads replicate those patterns under realistic production-like load instead of relying on synthetic benchmarks that miss the same write and contention profile.

26 workloads:

| Workload | What it exercises |
|---|---|
| `CONCURRENT_PUTS` | Many goroutines writing distinct keys simultaneously; simulates multi-controller write fan-out |
| `BURST_WRITES` | Sudden spike of writes in a short window; simulates API server burst during cluster scale-up |
| `SUSTAINED_LOAD` | Steady write rate over a longer duration; validates no throughput degradation under continuous pressure |
| `RAMP_UP_LOAD` | Gradually increasing write rate; validates etcd handles load growth without abrupt failures |
| `MIXED_WORKLOAD` | Interleaved reads, writes, and deletes; simulates typical controller reconcile-loop traffic |
| `LARGE_VALUES` | Writes with large payloads (ConfigMaps, Secrets); tests etcd serialization and storage under large-value conditions |
| `MANY_KEYS` | Writes across a large keyspace; validates pagination, compaction, and range-query performance at scale |
| `HIGH_CONTENTION` | Multiple writers competing on the same keys via transactions; simulates leader-election contention |
| `SEQUENTIAL_WRITES` | Single-goroutine sequential writes; baseline throughput measurement with no concurrency overhead |
| `RANDOM_READS` | Random key reads across the keyspace; validates read latency under a realistic reader distribution |
| `WATCH_PROGRESS_NOTIFY` | Sustained watch stream with progress notifications; validates watch delivery under load |
| `LEASE_INTENSIVE_WORKLOAD` | Exercises lease_manager.go TTL handling when many lease-backed objects churn (e.g., Node heartbeats) |
| `LIST_PAGINATION_HEAVY` | Simulates clients consuming large list responses where store.go#List produces continue tokens under pressure |
| `OPTIMISTIC_CONCURRENCY_TXN` | Targets OptimisticPut/Delete transactions in vendor/go.etcd.io/etcd/client/v3/kubernetes/client.go |
| `WATCH_MANY_PREFIXES` | Covers watcher.go fan-out of prefix watches for dozens of resource collections at once |
| `COMPACT_DURING_LOAD` | Validates compact.go running background compaction while store.go continues to serve traffic |
| `WATCH_WITH_CHURN` | Exercises watcher.go restarting streams rapidly as pods or controllers connect and disconnect |
| `NAMESPACE_ISOLATION_HEAVY` | Targets per-namespace key prefixing enforced by store.go across multi-tenant clusters |
| `TXN_MULTI_KEY_UPDATE` | Models multi-key Txn batches store.go issues during coordinated updates (e.g., etcd compactor elections) |
| `LEADER_ELECTION_CONTENTION` | Validates optional helper behavior in clientv3/concurrency leader election |
| `WATCH_BOOKMARK_HEAVY` | Stresses watcher.go bookmark emission path that powers Kubernetes watch bookmarks for large collections |
| `K8S_POD_LIFECYCLE_CHURN` | Steady pod create, two status updates, and delete cycles with informer watches; the baseline mutation workload of a running cluster (3 KiB pod-shaped values) |
| `K8S_NODE_HEARTBEAT_LEASES` | Eight short-TTL leases renewed once per second; the kubelet node-heartbeat pattern |
| `K8S_MIXED_APISERVER` | Concurrent informer list/watches, cache-miss GETs, pod PUTs, and node-lease renewals; the full kube-apiserver traffic mix in one workload |
| `K8S_CRD_HEAVY_CHURN` | Create, update, and delete of CRD-sized values with informer watches (64 KiB typical, 256 KiB for 1 in 10); the largest common Kubernetes payload shape |
| `K8S_JOB_STORM` | Unpaced pod create, status-update, and delete bursts with informer watches; exercises the burst shape of gang-scheduled jobs and autoscaling churn |
