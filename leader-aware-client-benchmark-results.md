# Leader-Aware Client Balancer: Benchmark Results

Implements the opt-in leader-aware client balancer proposed in
[etcd-io/etcd#22268](https://github.com/etcd-io/etcd/issues/22268), on top of
[etcd-io/etcd#22133](https://github.com/etcd-io/etcd/pull/22133). The v2
client shipped leader-prioritized endpoint selection for the same reason;
[etcd-io/etcd#9157](https://github.com/etcd-io/etcd/issues/9157) asked for
the v3 equivalent.

## What changes

Raft commits every mutation through the leader, but the v3 client picks
endpoints round-robin. A mutation sent to a follower is forwarded to the
leader first, so two thirds of writes pay an extra follower-to-leader hop and
payload copy. The balancer tracks the leader from response headers and routes
mutations directly to it. Reads, watches, Raft replication, forwarding, and
retry semantics are unchanged. The server never learns that a client used
leader-aware routing; a stale leader hint costs at most one forwarded or
failed attempt before round-robin resumes. The balancer is disabled by
default; rollback is configuration-only.

## Summary of results

Direction of improvement is marked on every metric (↑ higher is better,
↓ lower is better):

| Metric | Round-robin | Leader-aware | Change |
|---|---:|---:|---:|
| Throughput, [K8S_JOB_STORM](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_k8s_job_storm.go) (ops/s, ↑) | 2,779.0 | 3,120.9 | **+12.3%** |
| p99 latency, [K8S_JOB_STORM](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_k8s_job_storm.go) (ms, ↓) | 31.17 | 24.17 | **−22.5%** |
| Peer traffic, AWS mixed suite (bytes/run, ↓) | 11,483,178,909 | 10,516,319,159 | **−8.4%** |
| Peer traffic, controlled PUT (bytes/PUT, ↓) | 175,458 | 131,584 | **−25.0%** |

## Test setup

Two environments, same comparison: the published etcd v3.7.1 client
(round-robin) against the leader-aware client.

| | Local | AWS us-east-2 |
|---|---|---|
| etcd members | 3 containers (Podman; Docker fallback) on loopback | 3 × t3a.medium EC2 in one VPC |
| Load generators | the test process on the host | 3 × t3a.medium stress clients, one per AZ (us-east-2a, us-east-2b, us-east-2c) |
| Network path | published localhost ports | direct private VPC endpoints; no tunnels, no public ingress |
| Execution | `go test` | binaries shipped via S3, run via SSM Run Command, results collected from S3 |

AWS benchmark shape: each stress client runs the full [26-scenario suite](https://github.com/gyuho/etcd-infra/tree/main/internal/etcd/stress/scenarios); 10
workers per client, capped at 100 requests/s per worker, 90 seconds per
scenario. Each client side ran twice in A B B A order (round-robin,
leader-aware, leader-aware, round-robin) on the same cluster, so slow time
drift cancels. 312 scenario records total (26 scenarios × 3 stress clients ×
4 runs); all passed.

## Throughput (higher is better)

Combined request rate of the three stress clients, averaged across the two
runs per client side:

| Scenario | Round-robin ops/s | Leader-aware ops/s | Change |
|---|---:|---:|---:|
| [K8S_JOB_STORM](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_k8s_job_storm.go) | 2,779.0 | 3,120.9 | **+12.3%** |
| [K8S_POD_LIFECYCLE_CHURN](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_k8s_pod_lifecycle_churn.go) | 1,703.4 | 1,889.8 | **+10.9%** |
| [K8S_MIXED_APISERVER](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_k8s_mixed_apiserver.go) | 529.7 | 532.1 | +0.5% |
| [K8S_CRD_HEAVY_CHURN](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_k8s_crd_heavy_churn.go) | 134.4 | 135.9 | +1.1% |
| [CONCURRENT_PUTS](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_concurrent_puts.go) | 300.3 | 300.3 | +0.0% (rate-capped) |
| [SUSTAINED_LOAD](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_sustained_load.go) | 300.3 | 300.3 | +0.0% (rate-capped) |

```
ops/s, 3 stress clients combined (higher is better)   round-robin =░  leader-aware =█
K8S_JOB_STORM            2,779.0 ░░░░░░░░░░░░░░░░░░░░░░░     3,120.9 ██████████████████████████  +12.3%
K8S_POD_LIFECYCLE_CHURN  1,703.4 ░░░░░░░░░░░░░░              1,889.8 ████████████████            +10.9%
K8S_MIXED_APISERVER        529.7 ░░░░                          532.1 ████                        +0.5%
K8S_CRD_HEAVY_CHURN        134.4 ░                             135.9 █                           +1.1%
CONCURRENT_PUTS            300.3 ░░░                           300.3 ███                         rate-capped
SUSTAINED_LOAD             300.3 ░░░                           300.3 ███                         rate-capped
```

The gain appears only in latency-bound scenarios. The job-storm and pod-churn
workers run synchronous create/update/delete sequences: lower per-mutation
latency means more completed sequences per window. CONCURRENT_PUTS and
SUSTAINED_LOAD are rate-capped at 100 requests/s per client, so they sit at
their cap on both clients by construction.

## Latency (lower is better)

Each stress client reports one average and one p99 per scenario; the table
shows the arithmetic mean of those values across the three clients and two
runs. This is not a percentile recomputed from merged raw samples.

| Scenario | Average ms (rr → la) | p99 ms (rr → la) |
|---|---:|---:|
| [K8S_JOB_STORM](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_k8s_job_storm.go) | 10.29 → 9.12 (**−11.4%**) | 31.17 → 24.17 (**−22.5%**) |
| [K8S_POD_LIFECYCLE_CHURN](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_k8s_pod_lifecycle_churn.go) | 8.49 → 7.10 (**−16.4%**) | 25.83 → 20.67 (**−20.0%**) |
| [K8S_MIXED_APISERVER](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_k8s_mixed_apiserver.go) | 2.71 → 2.52 (−7.1%) | 7.83 → 6.83 (−12.8%) |
| [K8S_CRD_HEAVY_CHURN](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_k8s_crd_heavy_churn.go) | 4.96 → 3.96 (**−20.3%**) | 20.0 → 14.83 (**−25.8%**) |
| [SUSTAINED_LOAD](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_sustained_load.go) | 3.63 → 2.81 (**−22.4%**) | 8.50 → 6.50 (**−23.5%**) |
| [SEQUENTIAL_WRITES](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_sequential_writes.go) | 4.05 → 3.78 (−6.8%) | 10.00 → 11.17 (+11.7%) |
| [CONCURRENT_PUTS](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_concurrent_puts.go) | 3.44 → 2.96 (−14.1%) | 7.67 → 6.50 (−15.2%) |
| [K8S_NODE_HEARTBEAT_LEASES](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_k8s_node_heartbeat_leases.go) | 0.85 → 0.83 (−2.3%) | 2.33 → 3.50 (+50.0%) |

```
p99 ms (lower is better)   round-robin =░  leader-aware =█
K8S_JOB_STORM              31.17 ░░░░░░░░░░░░░░░░░░░░░░░░░░  24.17 ████████████████████        −22.5%
K8S_POD_LIFECYCLE_CHURN    25.83 ░░░░░░░░░░░░░░░░░░░░░░      20.67 █████████████████           −20.0%
K8S_CRD_HEAVY_CHURN        20.00 ░░░░░░░░░░░░░░░░░           14.83 ████████████                −25.8%
SUSTAINED_LOAD              8.50 ░░░░░░░                      6.50 █████                       −23.5%
SEQUENTIAL_WRITES          10.00 ░░░░░░░░                    11.17 █████████                   +11.7%
CONCURRENT_PUTS             7.67 ░░░░░░                       6.50 █████                       −15.2%
K8S_MIXED_APISERVER         7.83 ░░░░░░░                      6.83 ██████                      −12.8%
K8S_NODE_HEARTBEAT_LEASES   2.33 ░░                           3.50 ███                         +50.0%
```

The mechanism: a mutation that lands on a follower takes the path client →
follower → leader; the follower receives, queues, and forwards it before the
leader can process it. Leader-aware routing removes that hop. The p99 gains
are larger than the average gains, consistent with removing a queueing point
from the tail.

Two scenarios show a higher p99, and the raw records say why. In
K8S_NODE_HEARTBEAT_LEASES, five of six per-client p99s sit at 2–4 ms on both
clients; one leader-aware record read 6 ms (max 7 ms), and on a 2.3 ms
baseline that single reading out of 720 renewals moves the aggregated p99 to
3.50 ms. In SEQUENTIAL_WRITES, the shift comes from one leader-aware run
whose three clients all tailed at 12–13 ms while its requests were otherwise
as fast. Both scenarios improved on average latency (0.85 → 0.83 ms and
4.05 → 3.78 ms), and their worst single request was no worse than
round-robin's (max 7 ms vs 12 ms; 40 ms vs 47 ms). These are tail readings on
the two lowest-rate scenarios — a mean of six per-client p99s, with no merged
sample set to absorb one event — not a slower code path: both workloads take
the same direct-leader route that lowers latency everywhere else in the
table.

Local controlled PUT baseline (loopback, write-only, 720 PUTs per client
side): round-robin mean 4.53 ms, p99 11.34 ms; leader-aware mean 4.29 ms,
p99 9.47 ms. This isolates the write path without cross-AZ networking or
mixed background traffic.

## Peer-to-peer traffic (lower is better)

Change in `etcd_network_peer_sent_bytes_total`, summed across the three
members and averaged across the two runs per client side. Load-generator
traffic is not part of this metric.

| Measurement | Round-robin | Leader-aware | Reduction |
|---|---:|---:|---:|
| AWS 3-AZ mixed suite (bytes/run) | 11,483,178,909 | 10,516,319,159 | **966,859,750 B (−8.4%)** |
| Local controlled PUT (bytes/PUT) | 175,458 | 131,584 | **43,874 B (−25.0%)** |

```
bytes per full suite run (lower is better)   round-robin =░  leader-aware =█
round-robin   11,483,178,909 ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░
leader-aware  10,516,319,159 ██████████████████████████████████████████  −8.4%
```

The 25.0% controlled PUT figure is a measured write-only baseline, not the
expected fleet reduction. It matches the payload-copy model for a three-voter
cluster: Raft requires two replication copies, and uniform round-robin adds
one follower-to-leader proposal copy on two thirds of PUTs, so removing it
saves one quarter of the payload bytes (8/3 copies → 2 copies). The mixed AWS
suite also carries reads, watches, heartbeats, and elections that this change
does not touch, so its reduction is smaller: 8.4%.

## Kubernetes scenario notes

| Scenario | What it tests | Payload |
|---|---|---:|
| [K8S_JOB_STORM](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_k8s_job_storm.go) | Unpaced pod create, status-update, and delete bursts with four prefix watches. A synthetic burst shape; not a replay of production traces. | 3,072-byte generated values; the status update appends 7 bytes |
| [K8S_POD_LIFECYCLE_CHURN](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_k8s_pod_lifecycle_churn.go) | Steady pod create, two status updates, delete, with four prefix watches and a 20 ms pause per cycle. | 3,072-byte generated values; updates append 7 bytes |
| [K8S_MIXED_APISERVER](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_k8s_mixed_apiserver.go) | Concurrent prefix watches over pods, EndpointSlices, and ConfigMaps; cache-miss GETs; pod PUTs; node-lease renewals. | 3,072-byte pod values; short node-name lease values |
| [K8S_CRD_HEAVY_CHURN](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_k8s_crd_heavy_churn.go) | Create, update, delete of generated CRD-sized values with three prefix watches and a 250 ms pause per cycle. | 65,536 bytes; 262,144 bytes for 1 in 10; update appends 3 bytes |
| [K8S_NODE_HEARTBEAT_LEASES](https://github.com/gyuho/etcd-infra/blob/main/internal/etcd/stress/scenarios/scenario_k8s_node_heartbeat_leases.go) | 64 short-TTL leases renewed once per second. | short generated node names |

## Test status and limitations

| Check | Result |
|---|---|
| Stress benchmark | 312/312 scenario records passed (26 scenarios × 3 stress clients × 4 runs) |
| Conformance | Suite completed; the detailed count was not retained in the benchmark log |
| Member replacement | Passed |
| snap.db durability | 7/7 passed, including real-power-loss tests on a non-journaled EBS volume |
| AWS cleanup | Post-run checks found 0 running test instances, 0 tagged test volumes, and no local state file; the test code makes no EKS API calls |

Limitations: three-member clusters, fixed instance sizes, 90-second windows,
generated keys and values. The latency aggregation averages per-client
percentiles rather than recomputing percentiles from merged samples. The AWS
comparison pools two runs per client side; the two leader-aware runs agreed
within 2.5% on peer traffic. These results show association in this
benchmark, not a guaranteed production-wide gain.
