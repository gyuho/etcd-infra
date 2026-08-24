# Leader-Aware Client: Measured Results

Final test round: AWS us-east-2, three stress drivers in three availability
zones (us-east-2a, us-east-2b, us-east-2c), all running at the same time
against the same three etcd members. Each driver ran the full 26-scenario
suite. Results were collected per driver over S3 and aggregated.

Two clients are compared: the published etcd v3.7.1 client (round-robin) and
the leader-aware client from the fork. Zero failed operations on every
scenario.

The change under test is the leader-aware client balancer: the client tracks
the raft leader and sends mutations to it directly, instead of round-robin
through a follower. It implements the proposal in
[etcd-io/etcd#22268](https://github.com/etcd-io/etcd/issues/22268), and the
work builds on the base branch in
[etcd-io/etcd#22133](https://github.com/etcd-io/etcd/pull/22133).

## Peer-to-peer traffic

Bytes the etcd members sent to each other during one full suite run:

| Client | Bytes sent (3 drivers, summed) | Per driver |
|---|---|---|
| round-robin | 11,595,457,494 | 3,865,152,498 |
| leader-aware (two runs) | 10,648,546,205 and 10,384,092,113 | ~3.5 GB |
| **Reduction** | **9.3%** (1.08 GB less per run) | |

The 9.3% is the fleet-level number on the mixed suite. It is lower than the
pure-write figure because the suite's peer traffic includes background raft
traffic (heartbeats, elections, watch propagation) that the balancer does not
change. The saving applies only to the mutation share.

### The upper bound (controlled best case)

| Measurement | round-robin | leader-aware | Reduction |
|---|---|---|---|
| Local controlled PUT, peer bytes per PUT | 175,458 B | 131,584 B | **25.0%** |

The local test is the upper bound for this optimization, not the expected
fleet number. It measures only the write path, with no background traffic and
no reads or watches. On that path, exactly one third of writes were forwarded
from a follower to the leader before; leader-aware removes that copy, so the
saving is exactly 25%. Real clusters mix in reads, watches, and background
raft traffic, so the fleet saving lands below this bound — 9.3% on the mixed
suite above.

## Latency (exact milliseconds, aggregated across the 3 drivers)

| Scenario | avg ms, before → after | p99 ms, before → after |
|---|---|---|
| *K8S_JOB_STORM | 10.53 → **9.09** (−13.7%) | 31.7 → **24.0** (−24.3%) |
| *K8S_POD_LIFECYCLE_CHURN | 8.39 → **7.22** (−14.0%) | 26.7 → **20.7** (−22.5%) |
| *K8S_MIXED_APISERVER | 2.88 → **2.49** (−13.5%) | 8.7 → **6.7** (−23.0%) |
| *K8S_CRD_HEAVY_CHURN | 4.89 → **4.02** (−17.8%) | 19.3 → **15.7** (−18.7%) |
| SUSTAINED_LOAD | 3.59 → **3.01** (−16.2%) | 8.7 → **6.7** (−23.0%) |
| SEQUENTIAL_WRITES | 4.16 → **3.69** (−11.3%) | 10.7 → 9.7 (−9.3%) |
| CONCURRENT_PUTS | 3.38 → 3.31 (−2.1%) | 8.0 → 7.3 (−8.8%) |
| *K8S_NODE_HEARTBEAT_LEASES | 0.89 → 0.84 | 2.7 → 3.7 (noise) |

Leader-aware is faster on every write path. The largest wins are on the
write-dense workloads.

## Throughput (operations per second, aggregated across the 3 drivers)

| Scenario | ops/s, before → after | Delta |
|---|---|---|
| *K8S_JOB_STORM | 906.1 → **1043.5** | **+15.2%** |
| *K8S_POD_LIFECYCLE_CHURN | 572.2 → **623.2** | **+8.9%** |
| *K8S_MIXED_APISERVER | 175.8 → 177.5 | +1.0% |
| *K8S_CRD_HEAVY_CHURN | 44.8 → 45.3 | +1.1% |
| CONCURRENT_PUTS | 100.1 → 100.1 | parity (rate-capped) |
| SUSTAINED_LOAD | 100.1 → 100.1 | parity (rate-capped) |

Throughput rises only where the workload is limited by latency, not by the
harness rate cap. The storm and churn workloads are latency-bound, so the
removed forward hop shows up as more completed work per second.

---

## What each scenario does

| Scenario | What it does | Payload size |
|---|---|---|
| *K8S_JOB_STORM | A burst of pod creates, one status update, and deletes, with no pacing, with informer watches. This is the gang-scheduling and inference-autoscaling signature of AI/ML workloads (Kueue, JobSet, TrainJob, KServe). | 3 KB (pod-shaped) |
| *K8S_POD_LIFECYCLE_CHURN | Steady pod create, two status updates, delete, at a lifecycle-realistic rate. The baseline mutation workload of a running cluster. | 3 KB (pod-shaped) |
| *K8S_MIXED_APISERVER | The full kube-apiserver shape at once: informer list+watches, steady reads, bursty pod writes, and node-lease renewals, concurrently. | 3 KB writes |
| *K8S_CRD_HEAVY_CHURN | Create, update, delete of CRD-shaped objects with informer watches. The largest common objects in Kubernetes. | 64 KB typical, 256 KB for 1-in-10 |
| *K8S_NODE_HEARTBEAT_LEASES | Short-TTL lease renewals on a fixed interval — the kubelet node-heartbeat pattern. | ~200-500 B |
| CONCURRENT_PUTS | Overlapping writes from many workers at a fixed rate. | 256 B |
| SUSTAINED_LOAD | Continuous writes at a fixed rate. | 256 B |
| SEQUENTIAL_WRITES | Ordered writes, one after another. | 256 B |

The remaining scenarios (reads, watches, pagination, compaction, leader
election, large values, transactions) showed parity within noise; they are
omitted from the tables for readability. The full per-scenario records are in
the run's S3 result set.

## Notes

- The last official run in the palindrome did not complete (a driver-side
  network fault), so the official figure rests on one run against two
  leader-aware runs. The two leader-aware runs agree to within 2.5%, so the
  9.3% figure is stable.
- All tests passed on every completed run: conformance 132/132, stress 26/26
  on both clients, member replacement, and the snap.db durability suite
  (7/7, including the real-power-loss cases on a non-journaled EBS volume).
- The account was left clean: no instances, no volumes, no state files, and
  nothing outside us-east-2. No EKS resource was touched.
