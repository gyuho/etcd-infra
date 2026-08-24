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
| *K8S_JOB_STORM throughput (3 drivers combined) | 2,779.0 ops/s | 3,120.9 ops/s | **+12.3%** |
| *K8S_JOB_STORM average latency | 10.29 ms | 9.12 ms | **−11.4%** |
| *K8S_JOB_STORM p99 latency | 31.17 ms | 24.17 ms | **−22.5%** |
| AWS peer traffic, full mixed suite | 11,483,178,909 B | 10,516,319,159 B | **−8.4%** |
| Local controlled PUT peer traffic | 175,458 B/PUT | 131,584 B/PUT | **−25.0%** |

All 312 scenario records passed (26 scenarios × 3 drivers × 4 runs).

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
the current leader. The benchmark measures lower latency, higher throughput
for the storm and pod-churn scenarios, and less peer traffic. Avoiding the
follower hop is the expected mechanism, but this test does not isolate the
share of each result caused by network transit, queueing, serialization, or
run-to-run variation.

Rate-capped workloads stay at the configured cap. Raft replication,
heartbeats, reads, and watches still operate; the change removes only the
avoidable follower-to-leader mutation path.

## Throughput

The Kubernetes scenarios below contain repeated mutation sequences. The
reported throughput is the combined request rate from all three drivers,
averaged across the two runs for each client.

| Scenario | Round-robin | Leader-aware | Change |
|---|---:|---:|---:|
| *K8S_JOB_STORM | 2,779.0 ops/s | **3,120.9 ops/s** | **+12.3%** |
| *K8S_POD_LIFECYCLE_CHURN | 1,703.4 ops/s | **1,889.8 ops/s** | **+10.9%** |
| *K8S_MIXED_APISERVER | 529.7 ops/s | 532.1 ops/s | +0.5% |
| *K8S_CRD_HEAVY_CHURN | 134.4 ops/s | 135.9 ops/s | +1.1% |
| CONCURRENT_PUTS | 300.3 ops/s | 300.3 ops/s | No change; rate-capped |
| SUSTAINED_LOAD | 300.3 ops/s | 300.3 ops/s | No change; rate-capped |

The throughput increase is not a higher configured request rate. The storm
and pod-churn implementations issue synchronous mutation sequences: a worker
starts its next lifecycle only after the current mutations finish. Their
higher completed-operation rate is consistent with the measured lower
latency. CONCURRENT_PUTS and SUSTAINED_LOAD stay at about 300.3 ops/s
combined because each driver is rate-capped at about 100.1 ops/s.

## Latency

Each driver reports one average and one p99 for each scenario. The table shows
the arithmetic mean of those reported values across three drivers and two
runs per client. It is not a percentile recomputed from a merged raw-latency
sample set.

| Scenario | Average: round-robin → leader-aware | p99: round-robin → leader-aware |
|---|---:|---:|
| *K8S_JOB_STORM | 10.29 → **9.12 ms** (−11.4%) | 31.17 → **24.17 ms** (−22.5%) |
| *K8S_POD_LIFECYCLE_CHURN | 8.49 → **7.10 ms** (−16.4%) | 25.83 → **20.67 ms** (−20.0%) |
| *K8S_MIXED_APISERVER | 2.71 → **2.52 ms** (−7.1%) | 7.83 → **6.83 ms** (−12.8%) |
| *K8S_CRD_HEAVY_CHURN | 4.96 → **3.96 ms** (−20.3%) | 20.0 → **14.83 ms** (−25.8%) |
| SUSTAINED_LOAD | 3.63 → **2.81 ms** (−22.4%) | 8.5 → **6.5 ms** (−23.5%) |
| SEQUENTIAL_WRITES | 4.05 → **3.78 ms** (−6.8%) | 10.0 → 11.17 ms (+11.7%) |
| CONCURRENT_PUTS | 3.44 → **2.96 ms** (−14.1%) | 7.67 → **6.5 ms** (−15.2%) |
| *K8S_NODE_HEARTBEAT_LEASES | 0.85 → 0.83 ms (−2.3%) | 2.33 → 3.5 ms (+50.0%) |

The local controlled PUT test provides a simpler write-path baseline:
round-robin averaged 4.53 ms with an 11.34 ms p99; leader-aware averaged
4.29 ms with a 9.47 ms p99. The AWS results above are the deployment result
that matters; the local result only confirms the mechanism without cross-AZ
network effects or mixed background traffic.

## Peer-to-peer traffic

The AWS metric is the change in
`etcd_network_peer_sent_bytes_total`, summed across the three etcd members
and averaged across the two runs for each client. It covers one full
26-scenario run at a time. It excludes traffic from the three load generators
to the members.

| Measurement | Round-robin | Leader-aware | Reduction |
|---|---:|---:|---:|
| AWS three-AZ mixed suite | 11,483,178,909 B | 10,516,319,159 B | **966,859,750 B (8.4%)** |
| Local controlled PUT baseline | 175,458 B/PUT | 131,584 B/PUT | **43,874 B/PUT (25.0%)** |

The local PUT result is a **measured controlled PUT baseline**, not the
expected fleet reduction. It isolates the request path and showed the 25.0%
reduction predicted by the three-voter payload-copy model used by the test:
two raft replication copies are required, while uniform round-robin adds one
follower-to-leader proposal copy on two thirds of PUTs. Fixed framing and
background traffic can move the observed ratio, so this is not a universal
maximum. The mixed AWS suite includes reads, watches, elections, and other
raft traffic; its measured reduction was 8.4%.

The leader-aware client does not remove raft replication. The leader must
still replicate every committed mutation to the followers. It only removes
the avoidable follower-to-leader copy that occurs before replication starts.

## Kubernetes scenario notes

| Scenario | What it tests | Payload |
|---|---|---:|
| *K8S_JOB_STORM | Unpaced pod create, status-update, and delete bursts with four prefix watches. This synthetic shape is intended to exercise bursts associated with gang-scheduled jobs and inference autoscaling; it does not replay a captured production trace. | 3,072-byte generated values; the status update appends 7 bytes |
| *K8S_POD_LIFECYCLE_CHURN | Steady pod create, two status updates, and delete cycles with four prefix watches and a 20 ms pause per cycle. | 3,072-byte generated values; each status update appends 7 bytes |
| *K8S_MIXED_APISERVER | Concurrent prefix watches over pods, EndpointSlices, and ConfigMaps; cache-miss GETs; pod PUTs; and node-lease renewals. | 3,072-byte generated pod values; lease values are short node names |
| *K8S_CRD_HEAVY_CHURN | Create, update, and delete of generated CRD-sized values with three prefix watches and a 250 ms pause per cycle. | 65,536 bytes normally; 262,144 bytes for 1 in 10 generated values; update appends 3 bytes |
| *K8S_NODE_HEARTBEAT_LEASES | Eight short-TTL leases renewed once per second. | Lease-attached value is the generated node name, not a 200–500 B serialized Kubernetes Lease object |

## Test status and limitations

| Check | Result |
|---|---|
| Conformance | Command completed successfully; the detailed conformance count was not retained in this benchmark log |
| Stress benchmark | 312/312 scenario records passed (26 scenarios × 3 drivers × 4 runs) |
| Member replacement | Passed |
| snap.db durability | 7/7 passed, including real-power-loss tests on a non-journaled EBS volume |
| AWS cleanup | Post-run checks found 0 running test instances, 0 tagged test volumes, and no local AWS state file; the test code did not call an EKS API |

The reported AWS values pool two completed round-robin runs and two completed
leader-aware runs from the same cluster, in round-robin, leader-aware,
leader-aware, round-robin order. Each run contains 78 scenario records: 26
scenarios from each of the three drivers. All 312 records passed. Throughput
is summed across drivers; latency is averaged across the per-driver records;
peer traffic is summed across members and drivers for each run, then averaged
across the two runs for each client.

These results show association within this controlled benchmark. They do not
establish production-wide gains for every Kubernetes workload. The scenarios
use generated keys and values, a fixed three-member cluster, fixed instance
sizes, and a 90-second window. In particular, *K8S_JOB_STORM is a synthetic
stress shape rather than a replay of a measured production AI/ML cluster.
