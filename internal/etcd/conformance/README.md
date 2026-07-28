# etcd conformance

The conformance package hosts an etcd conformance runner that validates the etcd behavior required by Kubernetes.

Scenarios live under `./scenarios`.

> [!NOTE]
> **Why etcd conformance matters here.** Kubernetes uses etcd in specific ways that differ from generic etcd usage: it relies on atomic transactions for leader elections, prefix-range watches for controller reflectors, lease-backed bootstrap tokens, and compaction semantics for garbage collection. The conformance suite maps each scenario group directly to a Kubernetes feature area, ensuring every API surface Kubernetes consumes is continuously exercised.

## Coverage Snapshot (October 9, 2025)

- 99 distinct scenarios are implemented in `scenarios/` (see `scenarios/README.md` for the up-to-date catalogue).
- Scenario groups map directly to Kubernetes feature areas, ensuring the suite continuously exercises every API surface Kubernetes consumes.
- Long-running categories are capped with focused scenarios to keep the suite fast and deterministic.

| Kubernetes feature area | Representative scenarios |
| --- | --- |
| Key-value CRUD & pagination | `PUT_AND_GET_WITH_LATEST_REVISION`, `GET_WITH_PREFIX`, `DELETE_ALL_WITH_FROM_KEY` |
| Watches & progress notifications | `WATCH_WITH_PREFIX`, `WATCH_WITH_COMPACTED_REVISION`, `WATCH_WITH_PROGRESS_NOTIFY` |
| Lease lifecycle & keepalive semantics | `PUT_WITH_LEASE_KEEPALIVE`, `LEASING_PUT_AND_DELETE_WITH_PREFIX`, `LEASE_TOO_LARGE` |
| Transactions & optimistic concurrency | `TXN_COMPARE_RANGE`, `TXN_ERROR_DUPLICATE_KEY`, `TXN_PUT_MULTIPLE` |
| Concurrency primitives | `CONCURRENCY_MUTEX_LOCK`, `CONCURRENCY_ELECTION_CAMPAIGN`, `CONCURRENCY_STM_APPLY` |
| Maintenance & health | `MAINTENANCE_STATUS`, `COMPACT` |
| Cluster membership | `CLUSTER_MEMBER_LIST`, `CLUSTER_MEMBER_LIFECYCLE` |

Adding a new Kubernetes feature requires appending a matching scenario (or scenario family) under `scenarios/`.

Refer to `scenarios/add-tests.md` for authoring guidance when extending the catalogue.
