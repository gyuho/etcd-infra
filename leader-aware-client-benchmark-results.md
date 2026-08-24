# Leader-Aware Client Balancer: 3-AZ Benchmark Results

This document measures the leader-aware client balancer proposed in
[etcd-io/etcd#22268](https://github.com/etcd-io/etcd/issues/22268). The work
builds on [etcd-io/etcd#22133](https://github.com/etcd-io/etcd/pull/22133).

The leader-aware client tracks the raft leader and sends mutations directly
to it. The published etcd v3.7.1 client uses round-robin endpoint selection,
so a mutation sent to a follower must first be forwarded to the leader.
Removing that forwarding step is the change under test.

## Summary

| Metric | Round-robin | Leader-aware | Measured change |
|---|---:|---:|---:|
| *K8S_JOB_STORM throughput | 906.1 ops/s | 1,043.5 ops/s | **+15.2%** |
| *K8S_JOB_STORM average latency | 10.53 ms | 9.09 ms | **−13.7%** |
| *K8S_JOB_STORM p99 latency | 31.7 ms | 24.0 ms | **−24.3%** |
| AWS peer traffic, full mixed suite | 11,595,457,494 B | 10,516,319,159 B | **−9.3%** |
| Local controlled PUT peer traffic | 175,458 B/PUT | 131,584 B/PUT | **−25.0%** |

Zero operations failed in the completed scenario runs.

## Test topology

We use two test environments. The local test isolates the write path. The AWS
test measures the full Kubernetes-shaped workload over a three-AZ network.

| Environment | etcd members | Load generators | Network path | Result collection |
|---|---|---|---|---|
| Local | 3 Podman containers on the local Podman network | Test process on the same host | Published localhost ports to the three containers | Local test output and metrics |
| AWS us-east-2 | 3 × t3a.medium EC2 members in the VPC | 3 × t3a.medium ephemeral bastion/stress-driver instances, one in us-east-2a, one in us-east-2b, and one in us-east-2c | Each driver sends load directly to all three private VPC member endpoints; no SSM port-forwarding tunnel | Each driver writes JSONL results and member metric snapshots, uploads them to S3, and the test aggregates all three sets |

All three AWS drivers start the same workload at the same time against the
same three-member cluster. Each driver runs 10 workers at up to 100 requests
per second per worker, for 90 seconds per scenario. The suite contains 26
scenarios. It compares the published round-robin client with the leader-aware
client on the same cluster.

The bastion instances are only load generators. They do not store cluster
data or other critical state. The test sends the Linux driver binary through
S3, runs it through SSM Run Command, uploads the results to S3, and terminates
the instances during cleanup.

## Why leader-aware routing performs better

Every etcd mutation must pass through the raft leader. With round-robin
endpoint selection, two of the three endpoints are followers. A mutation that
lands on either follower takes an extra path: client → follower → leader. The
follower must receive, queue, and forward the request before the leader can
process it.

Leader-aware routing removes that extra hop. The client sends the mutation to
the current leader. This has three measured effects:

1. **Lower latency:** the mutation avoids the follower's network hop, queue,
   and forwarding work. The p99 improves more than the average because the
   removed queue also removes a source of tail latency.
2. **Higher throughput for latency-bound workloads:** workers finish each
   create, update, and delete cycle sooner, so they complete more cycles in
   the same test window. Rate-capped workloads remain at the configured cap.
3. **Less peer traffic:** followers no longer forward client mutations to the
   leader. Raft replication, heartbeats, reads, and watches remain unchanged.

## Throughput

The Kubernetes scenarios below contain repeated mutation sequences. They show
how the shorter write path changes completed work per second.

| Scenario | Round-robin | Leader-aware | Change |
|---|---:|---:|---:|
| *K8S_JOB_STORM | 906.1 ops/s | **1,043.5 ops/s** | **+15.2%** |
| *K8S_POD_LIFECYCLE_CHURN | 572.2 ops/s | **623.2 ops/s** | **+8.9%** |
| *K8S_MIXED_APISERVER | 175.8 ops/s | 177.5 ops/s | +1.0% |
| *K8S_CRD_HEAVY_CHURN | 44.8 ops/s | 45.3 ops/s | +1.1% |
| CONCURRENT_PUTS | 100.1 ops/s | 100.1 ops/s | No change; rate-capped |
| SUSTAINED_LOAD | 100.1 ops/s | 100.1 ops/s | No change; rate-capped |

The throughput increase is not a higher configured request rate. The storm
and pod-churn scenarios are latency-bound: a worker starts its next lifecycle
only after the current mutations finish. The leader-aware client shortens
that lifecycle. CONCURRENT_PUTS and SUSTAINED_LOAD stay at 100.1 ops/s
because the harness caps their offered rate.

## Latency

The table reports exact client-side average and p99 latency, aggregated across
the three AWS drivers.

| Scenario | Average: round-robin → leader-aware | p99: round-robin → leader-aware |
|---|---:|---:|
| *K8S_JOB_STORM | 10.53 → **9.09 ms** (−13.7%) | 31.7 → **24.0 ms** (−24.3%) |
| *K8S_POD_LIFECYCLE_CHURN | 8.39 → **7.22 ms** (−14.0%) | 26.7 → **20.7 ms** (−22.5%) |
| *K8S_MIXED_APISERVER | 2.88 → **2.49 ms** (−13.5%) | 8.7 → **6.7 ms** (−23.0%) |
| *K8S_CRD_HEAVY_CHURN | 4.89 → **4.02 ms** (−17.8%) | 19.3 → **15.7 ms** (−18.7%) |
| SUSTAINED_LOAD | 3.59 → **3.01 ms** (−16.2%) | 8.7 → **6.7 ms** (−23.0%) |
| SEQUENTIAL_WRITES | 4.16 → **3.69 ms** (−11.3%) | 10.7 → **9.7 ms** (−9.3%) |
| CONCURRENT_PUTS | 3.38 → 3.31 ms (−2.1%) | 8.0 → 7.3 ms (−8.8%) |
| *K8S_NODE_HEARTBEAT_LEASES | 0.89 → 0.84 ms | 2.7 → 3.7 ms (noise) |

The local controlled PUT test provides a simpler write-path baseline:
round-robin averaged 4.53 ms with an 11.34 ms p99; leader-aware averaged
4.29 ms with a 9.47 ms p99. The AWS results above are the deployment result
that matters; the local result only confirms the mechanism without cross-AZ
network effects or mixed background traffic.

## Peer-to-peer traffic

The AWS metric is the sum of bytes sent between the three etcd members during
one full 26-scenario run. It excludes traffic from the three load generators
to the members.

| Measurement | Round-robin | Leader-aware | Reduction |
|---|---:|---:|---:|
| AWS three-AZ mixed suite | 11,595,457,494 B | 10,516,319,159 B | **1,079,138,335 B (9.3%)** |
| Local controlled PUT baseline | 175,458 B/PUT | 131,584 B/PUT | **43,874 B/PUT (25.0%)** |

The local PUT result is a **measured write-only upper bound** for this
optimization, not the expected fleet reduction. It isolates the request path:
no reads, watches, elections, or unrelated raft traffic dilute the bytes saved
by removing follower forwarding. The mixed AWS suite includes all of that
traffic, so its 9.3% reduction is below the 25.0% PUT baseline.

The leader-aware client does not remove raft replication. The leader must
still replicate every committed mutation to the followers. It only removes
the avoidable follower-to-leader copy that occurs before replication starts.

## Kubernetes scenario notes

| Scenario | What it tests | Payload |
|---|---|---:|
| *K8S_JOB_STORM | Unpaced pod create, status-update, and delete bursts with informer watches. This models gang-scheduled AI/ML job starts and inference autoscaling churn. | 3 KB pod-shaped values |
| *K8S_POD_LIFECYCLE_CHURN | Steady pod create, two status updates, and delete cycles with informer watches. | 3 KB pod-shaped values |
| *K8S_MIXED_APISERVER | Concurrent informer list/watches, reads, pod mutations, and node-lease renewals. | 3 KB writes |
| *K8S_CRD_HEAVY_CHURN | Create, update, and delete of CRD-shaped values with informer watches. | 64 KB typical; 256 KB for 1 in 10 values |
| *K8S_NODE_HEARTBEAT_LEASES | Fixed-interval renewal of short-TTL kubelet node leases. | Approximately 200–500 B |

## Test status and limitations

| Check | Result |
|---|---|
| Conformance | 132/132 passed |
| Stress scenarios | 26/26 passed on both clients |
| Member replacement | Passed |
| snap.db durability | 7/7 passed, including real-power-loss tests on a non-journaled EBS volume |
| AWS cleanup | No test instances, volumes, or state files remained; no EKS resource was touched |

The final round-robin comparison completed once. The leader-aware comparison
completed twice, and those two measurements agreed within 2.5%. A later
round-robin repetition did not complete because of a driver-side network
fault. The report therefore compares one completed round-robin run with the
mean of two completed leader-aware runs.
