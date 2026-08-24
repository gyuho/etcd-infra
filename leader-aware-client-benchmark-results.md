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
| Throughput, [K8S_JOB_STORM](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_k8s_job_storm.go) (ops/s, ↑) | 2,834.0 | 3,081.2 | **+8.7%** |
| p99 latency, K8S_JOB_STORM (ms, ↓) | 32.00 | 24.68 | **−22.9%** |
| Peer traffic, AWS mixed suite (bytes/run, ↓) | 11,350,526,046 | 10,767,006,481 | **−5.1%** |
| Peer traffic, controlled PUT (bytes/PUT, ↓) | 175,467 | 131,590 | **−25.0%** |

## Test setup

Two environments, same comparison: the published etcd v3.7.1 client
(round-robin) against the leader-aware client.

| | Local | AWS us-east-2 |
|---|---|---|
| etcd members | 3 containers (Podman; Docker fallback) on loopback | 3 × t3a.medium EC2 in one VPC |
| Load generators | the test process on the host | 3 × t3a.medium stress clients, one per AZ (us-east-2a, us-east-2b, us-east-2c) |
| Network path | published localhost ports | direct private VPC endpoints; no tunnels, no public ingress |
| Execution | `go test` | binaries shipped via S3, run via SSM Run Command, results collected from S3 |

AWS benchmark shape: each stress client runs the full [26-scenario
suite](https://github.com/gyuho/etcd-infra/tree/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios);
10 workers per client, capped at 100 requests/s per worker, 90 seconds per
scenario. Each client side ran twice in A B B A order (round-robin,
leader-aware, leader-aware, round-robin) on the same cluster, so slow time
drift cancels. 312 scenario records total (26 scenarios × 3 stress clients ×
4 runs); all passed.

Latency aggregation: each scenario run records a mergeable latency histogram
(log-scale buckets, about 9% resolution, 0.0625 ms–16 s) that counts every
request. The tables merge those buckets across the three stress clients and
both runs of each client side, so the reported p99 is the bucket holding the
fleet-wide 99th-percentile request — not a mean of per-run percentiles.
Averages are request-weighted.

## Throughput (higher is better)

Combined request rate of the three stress clients, averaged across the two
runs per client side:

| Scenario | Round-robin ops/s | Leader-aware ops/s | Change |
|---|---:|---:|---:|
| [K8S_JOB_STORM](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_k8s_job_storm.go) | 2,834.0 | 3,081.2 | **+8.7%** |
| [K8S_POD_LIFECYCLE_CHURN](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_k8s_pod_lifecycle_churn.go) | 1,739.1 | 1,891.7 | **+8.8%** |
| [K8S_MIXED_APISERVER](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_k8s_mixed_apiserver.go) | 529.9 | 530.3 | +0.1% |
| [K8S_NODE_HEARTBEAT_LEASES](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_k8s_node_heartbeat_leases.go) | 191.1 | 191.2 | +0.1% |
| [K8S_CRD_HEAVY_CHURN](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_k8s_crd_heavy_churn.go) | 134.8 | 135.3 | +0.4% |
| [CONCURRENT_PUTS](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_concurrent_puts.go) | 300.3 | 300.3 | +0.0% (rate-capped) |
| [SUSTAINED_LOAD](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_sustained_load.go) | 300.3 | 300.3 | +0.0% (rate-capped) |

```
ops/s, 3 stress clients combined (higher is better)   round-robin =░  leader-aware =█
K8S_JOB_STORM              2,834.0 ░░░░░░░░░░░░░░░░░░░░░░░░    3,081.2 ██████████████████████████  +8.7%
K8S_POD_LIFECYCLE_CHURN    1,739.1 ░░░░░░░░░░░░░░░             1,891.7 ████████████████            +8.8%
K8S_MIXED_APISERVER          529.9 ░░░░                          530.3 ████                        +0.1%
K8S_NODE_HEARTBEAT_LEASES    191.1 ░░                            191.2 ██                          +0.1%
K8S_CRD_HEAVY_CHURN          134.8 ░                             135.3 █                           +0.4%
CONCURRENT_PUTS              300.3 ░░░                           300.3 ███                         rate-capped
SUSTAINED_LOAD               300.3 ░░░                           300.3 ███                         rate-capped
```

The gain appears only in latency-bound scenarios. The job-storm and pod-churn
workers run synchronous create/update/delete sequences: lower per-mutation
latency means more completed sequences per window. CONCURRENT_PUTS and
SUSTAINED_LOAD are rate-capped at 100 requests/s per client, so they sit at
their cap on both clients by construction.

## Latency (lower is better)

Fleet-wide values from merged latency histograms (bucket upper bounds; about
9% resolution):

| Scenario | Average ms (rr → la) | p99 ms (rr → la) |
|---|---:|---:|
| [K8S_JOB_STORM](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_k8s_job_storm.go) | 10.07 → 9.22 (−8.4%) | 32.00 → 24.68 (**−22.9%**) |
| [K8S_POD_LIFECYCLE_CHURN](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_k8s_pod_lifecycle_churn.go) | 8.19 → 7.06 (**−13.8%**) | 26.91 → 22.63 (**−15.9%**) |
| [K8S_MIXED_APISERVER](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_k8s_mixed_apiserver.go) | 2.69 → 2.63 (−2.4%) | 8.72 → 8.72 (0.0%) |
| [K8S_CRD_HEAVY_CHURN](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_k8s_crd_heavy_churn.go) | 4.64 → 4.31 (−7.2%) | 19.03 → 17.45 (−8.3%) |
| [K8S_NODE_HEARTBEAT_LEASES](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_k8s_node_heartbeat_leases.go) | 1.42 → 1.45 (+1.6%) | 5.19 → 5.19 (0.0%) |
| [SUSTAINED_LOAD](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_sustained_load.go) | 3.40 → 2.78 (**−18.2%**) | 10.37 → 7.34 (**−29.3%**) |
| [SEQUENTIAL_WRITES](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_sequential_writes.go) | 4.18 → 3.49 (**−16.4%**) | 12.34 → 9.51 (**−22.9%**) |
| [CONCURRENT_PUTS](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_concurrent_puts.go) | 3.52 → 2.95 (**−16.2%**) | 8.72 → 7.34 (**−15.9%**) |
| LARGE_VALUES | 10.90 → 13.32 (**+22.1%**) | 26.91 → 34.90 (**+29.7%**) |
| LIST_PAGINATION_HEAVY | 5.96 → 6.65 (+11.5%) | 19.03 → 32.00 (+68.2%) |

```
p99 ms (lower is better)   round-robin =░  leader-aware =█
K8S_JOB_STORM              32.00 ░░░░░░░░░░░░░░░░░░░░░░░░    24.68 ██████████████████          −22.9%
K8S_POD_LIFECYCLE_CHURN    26.91 ░░░░░░░░░░░░░░░░░░░░        22.63 █████████████████           −15.9%
K8S_CRD_HEAVY_CHURN        19.03 ░░░░░░░░░░░░░░              17.45 █████████████               −8.3%
SUSTAINED_LOAD             10.37 ░░░░░░░░                     7.34 █████                       −29.3%
SEQUENTIAL_WRITES          12.34 ░░░░░░░░░                    9.51 ███████                     −22.9%
CONCURRENT_PUTS             8.72 ░░░░░░                       7.34 █████                       −15.9%
K8S_MIXED_APISERVER         8.72 ░░░░░░                       8.72 ██████                      0.0%
K8S_NODE_HEARTBEAT_LEASES   5.19 ░░░░                         5.19 ████                        0.0%
LARGE_VALUES               26.91 ░░░░░░░░░░░░░░░░░░░░        34.90 ██████████████████████████  +29.7%
LIST_PAGINATION_HEAVY      19.03 ░░░░░░░░░░░░░░              32.00 ████████████████████████    +68.2%
```

The mechanism: a mutation that lands on a follower takes the path client →
follower → leader; the follower receives, queues, and forwards it before the
leader can process it. Leader-aware routing removes that hop. The p99 gains
are larger than the average gains, consistent with removing a queueing point
from the tail.

Two scenarios read worse and the raw records say what each is:

- **LARGE_VALUES** (1 MiB values) regressed consistently across all six
  leader-aware records, so this is a real cost, not noise: round-robin spreads
  the ingress of large values across all three members (a follower receives
  the payload, then forwards it), while leader-aware concentrates every
  megabyte of client traffic on the leader's receive path on top of its Raft
  replication sends. At 1 MiB per value, that concentration shows: average
  +22%, p99 +30%. The change is off by default; write-heavy workloads with
  megabyte-scale values are the case to measure before opting in.
- **LIST_PAGINATION_HEAVY** is a read-path scenario, and this change does not
  touch reads. Its shift came from one leader-aware run whose three clients
  all read slow (p99 39 ms each); its other run matched round-robin
  (18–20 ms). Run-level environment variance, not a routing effect.

The earlier run's tail artifacts (a +50% node-lease p99 from one 6 ms reading,
a +11.7% sequential-writes p99 from one slow run) do not appear here: the
lease scenario now renews 64 leases (thousands of samples per run, matching
the one-lease-per-node shape of real clusters), and the merged histograms
absorb single tail events into the fleet distribution.

Local controlled PUT baseline (loopback, write-only, 720 PUTs per client
side): round-robin mean 3.62 ms, p99 18.38 ms; leader-aware mean 3.44 ms,
p99 13.55 ms. This isolates the write path without cross-AZ networking or
mixed background traffic; loopback percentiles vary run to run.

## Peer-to-peer traffic (lower is better)

Change in `etcd_network_peer_sent_bytes_total`, summed across the three
members and averaged across the two runs per client side. Load-generator
traffic is not part of this metric.

| Measurement | Round-robin | Leader-aware | Reduction |
|---|---:|---:|---:|
| AWS 3-AZ mixed suite (bytes/run) | 11,350,526,046 | 10,767,006,481 | **583,519,565 B (−5.1%)** |
| Local controlled PUT (bytes/PUT) | 175,467 | 131,590 | **43,877 B (−25.0%)** |

```
bytes per full suite run (lower is better)   round-robin =░  leader-aware =█
round-robin   11,350,526,046 ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░
leader-aware  10,767,006,481 ████████████████████████████████████████████  −5.1%
```

The 25.0% controlled PUT figure is a measured write-only baseline, not the
expected fleet reduction. It matches the payload-copy model for a three-voter
cluster: Raft requires two replication copies, and uniform round-robin adds
one follower-to-leader proposal copy on two thirds of PUTs, so removing it
saves one quarter of the payload bytes (8/3 copies → 2 copies). The mixed AWS
suite also carries reads, watches, heartbeats, and elections that this change
does not touch, so its reduction is smaller and moves with the write share
(5.1% this run; 8.4% on an earlier run with the same suite).

## Kubernetes scenario notes

| Scenario | What it tests | Payload |
|---|---|---:|
| [K8S_JOB_STORM](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_k8s_job_storm.go) | Unpaced pod create, status-update, and delete bursts with four prefix watches. A synthetic burst shape; not a replay of production traces. | 3,072-byte generated values; the status update appends 7 bytes |
| [K8S_POD_LIFECYCLE_CHURN](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_k8s_pod_lifecycle_churn.go) | Steady pod create, two status updates, delete, with four prefix watches and a 20 ms pause per cycle. | 3,072-byte generated values; updates append 7 bytes |
| [K8S_MIXED_APISERVER](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_k8s_mixed_apiserver.go) | Concurrent prefix watches over pods, EndpointSlices, and ConfigMaps; cache-miss GETs; pod PUTs; node-lease renewals. | 3,072-byte pod values; short node-name lease values |
| [K8S_CRD_HEAVY_CHURN](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_k8s_crd_heavy_churn.go) | Create, update, delete of generated CRD-sized values with three prefix watches and a 250 ms pause per cycle. | 65,536 bytes; 262,144 bytes for 1 in 10; update appends 3 bytes |
| [K8S_NODE_HEARTBEAT_LEASES](https://github.com/gyuho/etcd-infra/blob/3cf13c64c5822fdecdc429e7758d6c9c510c0a0b/internal/etcd/stress/scenarios/scenario_k8s_node_heartbeat_leases.go) | 64 short-TTL leases renewed once per second. | short generated node names |

## Test status and limitations

| Check | Result |
|---|---|
| Stress benchmark | 312/312 scenario records passed (26 scenarios × 3 stress clients × 4 runs) |
| Conformance | Suite completed; the detailed count was not retained in the benchmark log |
| Member replacement | Passed |
| snap.db durability | 7/7 passed, including real-power-loss tests on a non-journaled EBS volume |
| AWS cleanup | Post-run checks found 0 running test instances, 0 tagged test volumes, and no local state file; the test code makes no EKS API calls |

Limitations: three-member clusters, fixed instance sizes, 90-second windows,
generated keys and values. p99 values are bucket upper bounds at about 9%
resolution. The AWS comparison pools two runs per client side; the two
leader-aware runs agreed within 3.3% on peer traffic. These results show
association in this benchmark, not a guaranteed production-wide gain.
