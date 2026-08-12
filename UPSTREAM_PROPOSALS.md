# Leader identity in responses, then leader-aware mutations

## Summary

Original discussion: https://github.com/SaranBalaji90/operation-type-aware-routing/tree/etcd-boltzmann

- **Problem.** Raft commits every write through the leader, but a v3 client cannot tell which member that is: ordinary responses do not say. A write sent to a follower is forwarded to the leader, so uniform round-robin pays an extra follower-to-leader payload copy on two out of three writes.
- **Proposal.** Bring back the v2 client's `EndpointSelectionPrioritizeLeader` in a gRPC-native way, as two independent PRs: return the leader's member ID in every v3 `ResponseHeader`, then add an opt-in leader-aware Go client balancer that routes mutations to that leader.
- **Payoff.** Removing the forwarding copy is an ideal 25% reduction in peer payload for a three-voter cluster; the local three-member E2E measures 24.9% fewer peer bytes per 64-KiB PUT (43,628 bytes). When members occupy different Availability Zones, the avoided bytes are cross-AZ transfer that EC2 charges for: an estimated savings of $226/month at 100 PUT/s and $2,262/month at 1,000 PUT/s of 64-KiB values, subject to the limits in the estimate below.
- **Safety.** The server never learns a request used leader-aware routing: forwarding, Raft, watches, and reads are untouched, and a stale hint costs at most one forwarded or failed attempt before round-robin resumes. The balancer is disabled by default, and rollback is configuration-only.
- **Precedent.** The v2 client shipped `EndpointSelectionPrioritizeLeader` for exactly this reason: it sent requests directly to the leader to avoid the follower forwarding roundtrip. [etcd #9157](https://github.com/etcd-io/etcd/issues/9157) asked for the v3 equivalent in 2018, and the v3 client has since been rebuilt on gRPC's resolver and balancer APIs, which is what makes a native version possible now.

## What

Propose two independent upstream PRs:

1. Add the current leader's member ID to the v3 `ResponseHeader`.
2. Add an opt-in leader-aware Go client balancer that routes mutating KV RPCs to that leader.

The response field is a protocol capability available to every client language. The balancer is a disabled-by-default Go policy. A third, independent PR fixes `SetEndpoints` endpoint ordering; it does not depend on either phase.

## How

Servers return an advisory `leader_id` in ordinary response headers. New clients treat `0`, old-server responses, ambiguous proxy routes, and stale or failed hints as unusable and use native round-robin. A client qualifies a route only when the response came directly from the reported leader, then uses that route for later mutations until the hint is invalidated. This eliminates steady `Status` discovery without changing retry semantics, Raft replication, or the default client behavior.

Keep the protocol and routing changes separate: their compatibility, implementation, and rollback risks are independent, so either PR can land or revert on its own.

## Current behavior and cost

etcd accepts a mutation through any voting member, but Raft commits it through the leader. When a client sends a write to a follower, the follower forwards the proposal to the leader, and normal quorum replication follows. Direct leader routing removes the follower-to-leader copy. It does not remove either replication copy required by a three-voter cluster.

For a payload of size `S` in a healthy three-voter cluster:

- leader-aware peer payload: `2S`
- uniform round-robin peer payload: `2S + (2/3)S = (8/3)S`
- ideal reduction: `((8/3)S - 2S) / ((8/3)S) = 25%`

The local E2E tests the mechanism directly instead of treating successful requests as proof. It records the first-hop peer for every PUT, holds the leader and Raft term stable, waits for every member to apply each block, then compares four 64-KiB blocks in `round-robin -> leader-aware -> leader-aware -> round-robin` order. See [`TestLeaderAwarePerformanceE2E`](cmd/etcd-infra/local_balancer_e2e_test.go).

### Result from the latest local run

| Measurement | Round-robin | Leader-aware | Difference |
|---|---:|---:|---:|
| Peer bytes per 64-KiB PUT | 175,203 | 131,575 | 24.9% lower |
| Mean loopback latency | 3.876 ms | 3.592 ms | 7.3% lower |
| p95 loopback latency | 5.283 ms | 5.044 ms | 4.5% lower |

The peer-byte result matches the three-voter derivation. The latency result is observational: same-host latency is dominated by storage, scheduling, and loopback overhead, so it is not evidence of a production latency gain.

The metric is `etcd_network_peer_sent_bytes_total`, which etcd defines as bytes sent to peers. The E2E sums only the sent counter across members; adding received bytes would count the same peer transfer twice. It does not measure TCP, TLS, ENI, or load-balancer framing. See the [etcd metrics reference](https://etcd.io/docs/v3.4/metrics/etcd-metrics-v3.4.8/).

### Reliability result from the same run

| Injected fault | Round-robin | Leader-aware |
|---|---:|---:|
| Paused follower | 67/101 writes succeeded | 101/101 writes succeeded |
| Paused leader, full observation window | 165/275 writes succeeded | 200/275 writes succeeded |
| Paused old leader, after leader-awareness recovery | 66/100 writes succeeded | 100/100 writes succeeded |

The reliability test confirms the fault before measuring it. It uses an independent client as the leader oracle, records each transport attempt, bounds the stale-hint cohort, and verifies every acknowledged or indeterminate write after recovery. A separate replacement E2E keeps one client open while replacing leaders and followers. It verifies first-hop routing after each election, crosses multiple 30-second refresh windows, and checks that the local provider reused the member's data volume. See [`TestLeaderAwareReliabilityE2E`](cmd/etcd-infra/local_balancer_e2e_test.go) and [`TestLeaderAwareReplacementE2E`](cmd/etcd-infra/local_replace_e2e_test.go).

These single-host Podman tests establish routing, failure behavior, and the source of the peer-byte reduction; they are not an SLA. The tested prototype uses jittered periodic `Status` calls, so it does not validate the response-driven Phase 2 design. Before either upstream API commitment, repeat the first-hop and fault tests with a server returning `leader_id` and a client making zero `Status` calls.

### Estimated bandwidth savings and transfer cost

The table below estimates the savings from the avoided peer bandwidth, from an application counter. It assumes every avoided counter byte would otherwise cross an Availability Zone and applies the published EC2 charge of $0.01/GB on each side of a cross-AZ transfer. Same-AZ transfer is free. The etcd counter excludes TCP, TLS, ENI, and other wire overhead, so the estimate covers application payload only. Check the target Region before using this rate. See [EC2 On-Demand data-transfer pricing](https://aws.amazon.com/ec2/pricing/on-demand/) and AWS's [cross-AZ charging example](https://aws.amazon.com/blogs/networking-and-content-delivery/optimizing-data-transfer-costs-when-using-aws-network-load-balancer/).

The measured delta was 43,628 peer bytes per 64-KiB PUT:

| Illustrative workload | Avoided counter bytes | Estimated transfer savings |
|---|---:|---:|
| 1 million PUTs | 43.6 GB | $0.87 |
| 100 PUTs/s | 11.3 TB | $226/month |
| 1,000 PUTs/s | 113.1 TB | $2,262/month |
| 10,000 PUTs/s | 1.13 PB | $22,617/month |

Under the ideal payload model, the payload-dependent saving for a 4-KiB value is about one-sixteenth as large: roughly $14/month at 100 PUTs/s or $141/month at 1,000 PUTs/s. That estimate does not scale the measured 64-KiB counter delta; fixed Raft and framing overhead does not shrink with the value.

The net savings are:

```text
(avoided cross-AZ peer bytes
 - added cross-AZ client bytes)
* regional transfer price
```

The estimate is relevant when three etcd members occupy different Availability Zones and clients already spread requests across all three endpoints without locality. Savings are zero when the relevant members share an Availability Zone. A locality-aware client may move some bytes from the peer path to the client path instead of removing them. An AWS experiment must measure both paths and `DataTransfer-Regional-Bytes` before treating the estimate as measured savings.

The experiment has not yet measured whether reduced peer serialization, queueing, and NIC pressure matter more than the transfer charge.

## Why normal responses need leader identity

The current v3 [`ResponseHeader`](https://github.com/etcd-io/etcd/blob/5b75ac62cf042a185e902530c25fd3d59c095232/api/etcdserverpb/rpc.proto#L417-L432) returns `cluster_id`, the serving `member_id`, the revision, and the Raft term. It does not return the leader. [`StatusResponse`](https://github.com/etcd-io/etcd/blob/5b75ac62cf042a185e902530c25fd3d59c095232/api/etcdserverpb/rpc.proto#L1181-L1203) does return the leader's member ID.

Without leader identity in normal responses, a client has three choices:

- poll `Status` on every endpoint;
- wait for a leader-related error and temporarily fall back to round-robin;
- remain on round-robin.

Errors alone are incomplete. A request sent to an old leader may be forwarded successfully after an election, so the client receives no error from which to learn the new leader. Periodic polling finds that change, but its steady-state request rate is:

```text
Status QPS = clients * endpoints / refresh interval
```

With three endpoints and the prototype's 30-second interval, that is `0.1 * clients` Status requests per second. Jitter spreads those requests over time; it does not reduce their number. Idle clients continue polling even though they have no mutation to route.

Returning the leader ID in successful responses removes steady discovery traffic. A mutation that reaches the leader directly can identify both the selected route and the leader without a separate call. An idle client creates no discovery traffic. Its first mutation after an election may cost one forwarded or failed attempt before the client relearns the route.

## Phase 1: add `leader_id` to `ResponseHeader`

### API

Add a field using the next free tag and the next-release version annotation:

```proto
message ResponseHeader {
  uint64 cluster_id = 1;
  uint64 member_id = 2;
  int64 revision = 3;
  uint64 raft_term = 4;
  uint64 leader_id = 5;
}
```

Define `leader_id` as the leader member ID known to the serving member when it fills the response header. `0` means unknown or no elected leader. It is an advisory observation: not necessarily the member that committed the request, and not a promise that the member remains leader when the client receives the response.

This information is already available at the fill site. The v3 RPC header helper currently writes `member_id` and `raft_term`, and its `RaftStatusGetter` already exposes `Leader()`. See [`header.fillWithoutRevision`](https://github.com/etcd-io/etcd/blob/5b75ac62cf042a185e902530c25fd3d59c095232/server/etcdserver/api/v3rpc/header.go#L47-L56) and [`RaftStatusGetter`](https://github.com/etcd-io/etcd/blob/5b75ac62cf042a185e902530c25fd3d59c095232/server/etcdserver/apply/interface.go#L107-L114).

The current getters expose leader and term separately. Keep the existing `raft_term` semantics and define `leader_id` as an independent advisory observation. The pair is not an atomic Raft snapshot and must not be used as a fencing token. Phase 2 below does not require term ordering.

### No request flag

`PrevKv` is opt-in because it requires an additional storage result and may return an arbitrarily large value. A leader ID is an in-memory integer already returned by `Status`. Adding `with_leader_id` to each request type would expand the API and complicate nested transactions to avoid at most 11 response bytes. Put the field in `ResponseHeader`: old clients ignore it, and new clients treat `0` from old servers as unavailable.

### Why an ID instead of `is_leader`

A `bool is_leader` field is sufficient for the minimal Phase 2 algorithm and costs two protobuf bytes when populated. `leader_id` can cost 11 bytes, but it preserves the value already returned by `Status`: clients with a trustworthy member-to-route map can learn the new leader from a follower response instead of waiting for round-robin to hit it, and operators can correlate an ordinary response with cluster membership.

Those additional uses must justify the wider field. If upstream does not want them, `is_leader` is the smaller protocol primitive and Phase 2 still works. This choice belongs in the Phase 1 API review, before the protobuf field becomes permanent.

The maximum added binary payload is 11 bytes times the number of responses carrying a populated header: one field tag plus a ten-byte `uint64`. JSON output is larger. Because the routing saving applies to mutations while the header appears on reads and watch responses too, Phase 1 must benchmark representative read and watch traffic. If the overhead is material, prefer the smaller boolean or a request-gated design.

### Required semantics

- `member_id` continues to identify the member that served the RPC.
- `leader_id` identifies that member's current Raft leader view when the header is filled.
- `raft_term` keeps its existing meaning and is independent of `leader_id`.
- `leader_id == 0` is valid during startup, leader loss, or an election.
- A stale nonzero value is permitted during convergence. Clients must fall back safely; they must not treat the field as proof of leadership.
- The field does not change authorization, linearizability, retry, or forwarding behavior.

Member IDs are already exposed through `member_id`, membership APIs, and `Status`, so this field does not introduce a new identifier class. The security review should still confirm that adding it to every response does not violate an existing proxy or tenancy boundary.

### Compatibility and rollout

- Adding a protobuf field is wire-compatible.
- New clients must work with old servers by treating `0` as no hint.
- Old clients must continue to work with new servers.
- Mixed-version clusters may return `0` from old members and a nonzero value from new members.
- JSON and gRPC-gateway output will gain `leaderId` when nonzero; generated API fixtures and version annotations must be updated.
- Proxies must preserve or regenerate the new field correctly.

### Acceptance tests

1. Query every member in a healthy three-voter cluster. Each response reports its own `member_id` and one common nonzero `leader_id`; `raft_term` keeps its existing behavior.
2. Move leadership while the old leader stays healthy. Normal KV responses eventually report the new `leader_id` without a `Status` call.
3. Stop the leader. During the election, `leader_id == 0` or a stale value is allowed; after election, successful responses converge on the new leader.
4. Exercise PUT, DELETE, mutating and read-only TXN branches, COMPACT, Range, Watch progress, lease, auth, cluster, and maintenance responses that carry `ResponseHeader`.
5. Verify new-client/old-server, old-client/new-server, and mixed-server compatibility.
6. Verify proxies and the JSON gateway.
7. Make `StatusResponse.Leader` agree with `StatusResponse.Header.LeaderId` so one response cannot expose two disagreeing leader observations.
8. Benchmark binary and JSON response size, allocations, and throughput for representative Range and Watch traffic with the field populated.

### Non-goals

- redirecting an RPC at the server;
- guaranteeing that the reported member will remain leader;
- using leader identity as a fencing token;
- changing Raft or election behavior;
- adding client routing policy in the same PR.

## Phase 2: add an opt-in leader-aware Go client balancer

Phase 2 carries the payoff measured above. That peer-byte reduction is inter-member traffic; when members occupy different Availability Zones, the avoided bytes are the ones that cross-AZ transfer pricing charges for, subject to the limits in the savings estimate. The reduction also grows with write-API coverage: leader-aware routing already covers the frequent consensus writes (KV mutations, lease grant/revoke, auth administration, Alarm), and extending it to the remaining ones — membership changes and cluster-wide `Downgrade` actions, excluded here as rare — applies the same per-RPC reduction to them, bounded by their low volume.

### Preconditions

Phase 2 depends on Phase 1's field semantics and a supported way for the client interceptor to retain the logical resolver route selected for one transport attempt. It must not infer that route from `peer.Addr`, advertised `ClientURLs`, or string comparison: DNS aliases, proxies, custom dialers, and address rewriting break those identities.

### Client state

Keep only:

- the current picked route, fenced by endpoint generation and a unique hint identity;
- the nonzero `cluster_id` accepted for that endpoint generation;
- a monotonic observation sequence allocated at transport pick time, so a late response from an older attempt cannot replace newer routing state;
- the member IDs observed behind each route during the current endpoint generation.

No member-to-endpoint map, tracker goroutine, timer, or bootstrap RPC is needed. A successful mutation qualifies its route only when `member_id == leader_id != 0`; otherwise native round-robin remains in control. That equality is the member's advisory view, not proof that it remains leader.

A logical RPC may retry through several routes, so pair the response with its exact successful transport attempt. Store a request-scoped attempt record where both the picker and retry interceptor can access it. Leader-aware routing is limited to direct-member routes: if one route produces multiple member IDs, mark it ambiguous for that endpoint generation. A single successful response cannot prove backend affinity.

### State transitions

| Event | Action |
|---|---|
| Client starts | Use native round-robin. |
| First nonzero `cluster_id` in an endpoint generation | Bind observations and hints to that cluster. |
| A later response has zero or different `cluster_id` | Ignore the observation, clear the hint, and disable leader-aware routing for that endpoint generation. |
| Successful mutation has `member_id == leader_id != 0` on an unambiguous route | Publish that exact successful attempt's route as an advisory hint. |
| Successful hinted mutation has `member_id != leader_id` or either value is zero | Invalidate the exact hint used by that attempt; the write may have succeeded through a former leader, so relearn through round-robin. |
| Hinted endpoint is not ready | Use round-robin for that RPC and invalidate only the hint used by that pick. A dead member emits no RPC error — mutations keep succeeding through follower forwarding — so picker readiness is the only prompt signal; when the hint was healthy, the rediscovery round republishes the same leader. |
| Hinted mutation returns `Unavailable`, `DeadlineExceeded`, or `ErrNotLeader` | Invalidate that exact hint; later attempts for the same RPC bypass it and use the existing retry policy. `DeadlineExceeded` stays because a gray-failed leader whose transport remains open produces no other signal. |
| Endpoints change | Clear route observations and the hint; reject callbacks from the old endpoint generation. |
| A route produces multiple `member_id` values | Mark it ambiguous and use round-robin for that route generation. |
| Read, watch, unknown RPC, old-server response, or no usable hint | Use native round-robin. |

After a voluntary leader move, the old leader may forward a mutation successfully. Its response then has the old member's `member_id` and the new `leader_id`, which clears the old hint. Round-robin eventually reaches the new leader; an equality observation qualifies that route, and leader-aware routing resumes. An idle client performs no work and pays that relearning cost on its next mutation.

This removes the 30-second loop, its jitter, all `Status` traffic, and the associated public timing knobs.

### RPC scope

The prototype uses leader-aware routing only when verification against etcdserver shows it is either an optimization (the server submits the request to Raft on whichever voting member receives it) or a requirement (only the leader serves it):

- `Put`;
- `DeleteRange`;
- `Compact`;
- `Txn` when any operation in either possible branch is not a Range — Put, DeleteRange, or a nested transaction of any kind — matching the server's `IsTxnReadonly` rule;
- `LeaseGrant` and `LeaseRevoke`;
- auth administration, including `Authenticate`, which proposes to register the issued token;
- `Alarm`, because the server submits every action, including `GET`;
- `MoveLeader`, the requirement case: a follower rejects it with `ErrNotLeader` instead of forwarding, and `ErrNotLeader` is `FailedPrecondition`, which the client retry policy never retries for either repeatable or non-repeatable RPCs. Leader-aware routing fixes first-attempt routing rather than changing retry behavior; a stale hint fails once, invalidates itself, and rediscovery finds the new leader.

The server chooses a transaction branch, so both possible branches decide the classification. The client mirrors the server's `etcdserver/txn.IsTxnReadonly` rule exactly: any operation that is not a Range — Put, DeleteRange, or a nested transaction of any kind — makes the transaction a Raft proposal. Like the server, the check is shallow: a nested read-only transaction still takes the consensus path, so the client routes it with leader awareness without recursing into nested branches. A conservative classification can use leader-aware routing for a read-only execution; an incomplete classification only misses the routing benefit. Reads, read-only transactions, watches, lease keep-alives (a stream the unary interceptor never sees), member-local maintenance (`Defragment`, `Status`, `HashKV`, which the client dials per endpoint), membership changes, and `Downgrade` remain on round-robin. The first upstream version can still stage KV-only leader-aware routing; the prototype's wider scope supplies the measurements for that decision.

`IsTxnReadonly` cannot be imported directly: it lives in `server/v3`, which already depends on `client/v3`, and the import would drag bbolt and raft into every client binary. The client therefore mirrors the rule in a few lines with a comment citing the server function. Moving `IsTxnReadonly` (and `IsTxnSerializable`) into the shared `api/v3` module would let client and server share one implementation and remove any risk of the two classifications drifting; that move is small and can land independently of either phase.

### Retry and correctness rules

- The balancer must not add retries or change which etcd operations the existing retry interceptor considers repeatable.
- One routing state covers every attempt of a logical RPC. After a failed hinted attempt, later attempts for that RPC use round-robin.
- A timeout remains indeterminate: the write may have committed. Tests must accept absence or the submitted value, never a conflicting value.
- An unavailable or stale hint changes performance and availability, not consistency. Native round-robin is the safe fallback.
- Hint publication and invalidation must be generation- and identity-fenced so stale callbacks cannot overwrite new endpoint or leader state.
- A stale picker can fail against a hint that a newer publication already replaced. When the newer hint repeats the failed address, the failure is fresher evidence than the probes behind that publication, so rediscovery runs; when the newer hint names a different address, the stale failure is ignored. The accepted false positive — a healthy same-address republication cleared by a late, unrelated failure — costs one coalesced Status round and a round-robin window.
- Response observations are advisory. Phase 2 does not order them by `raft_term`; the existing header does not promise that term and leader are one atomic snapshot.

Leader-aware routing trades distributed fault exposure for concentration. A gray follower no longer affects leader-aware mutations, but every mutation picked concurrently against a gray leader can stall until the first completion invalidates the hint; round-robin would send only a share there. That blast radius must be explicit.

With a configured maximum of `C` in-flight mutations, require at most `C + 1` stale-hint first hops, allowing one pick racing invalidation. For serial one-attempt probes with election deadline `E`, per-attempt deadline `D`, and `N` endpoints, use `E + (N + 1)D` as the predeclared maximum no-success interval instead of choosing a percentage after the run.

### Public API

Keep round-robin as the default and expose one explicit opt-in, with final naming decided during API review:

```go
cfg.ExperimentalLeaderAware = true
```

Do not upstream a generic balancer selector or polling controls. The response-driven design has no refresh interval or `Status` timeout.

The local policy wraps `round_robin` and uses gRPC's `endpointsharding.ChildStatesFromPicker` to reuse ready child pickers. That package is explicitly [experimental](https://pkg.go.dev/google.golang.org/grpc/balancer/endpointsharding). The upstream change must either secure a stable grpc-go composition API or own the required endpoint `SubConn` state using supported interfaces. A grpc-go upgrade must not be able to disable leader-aware routing silently while fallback keeps broad tests green.

### Kubernetes use

Kubernetes constructs a `clientv3.Config` from the API server's etcd transport settings and passes the configured endpoint list to the etcd client. See [`newETCD3Client`](https://github.com/kubernetes/kubernetes/blob/b882c60b4023bdf09264c2d5d30a2cadebc240fb/staging/src/k8s.io/apiserver/pkg/storage/storagebackend/factory/etcd3.go#L286-L356).

Kube-apiserver does not receive the benefit automatically. After the etcd server API and Go client policy land, Kubernetes would need a separate opt-in integration and rollout. For those API servers, direct mutation routing would remove the follower-to-leader proposal copy from the etcd peer path. It would not change watch routing or eliminate Raft replication.

### Required tests

#### Unit tests

Unit tests must cover:

- mutation classification, including nested and conditional transactions;
- default round-robin selection and explicit policy selection;
- leader selection, missing/unready leader fallback, and read/watch fallback;
- hint qualification only when `member_id == leader_id != 0`;
- late response suppression by observation sequence;
- response-to-route attribution when one logical RPC retries across two routes;
- exact-hint invalidation and A-to-B-to-A races;
- endpoint-generation changes;
- zero and conflicting `cluster_id` observations;
- one route producing multiple member IDs;
- old-server `leader_id == 0` behavior;
- service-config and resolver precedence;
- the chosen grpc-go integration boundary.

#### Three-member integration

The integration test must prove transport first hops. Successful calls alone are insufficient because round-robin fallback can make a broken leader policy look healthy. It must cover:

1. Stable leader: round-robin PUTs reach all endpoints; repeated leader-aware PUTs reach only the independently confirmed leader.
2. Follower gray failure: the fault is confirmed; leader-aware writes never select it and continue without failures.
3. Leader gray failure: the first stale-hint attempt is observed, writes resume through fallback after election, and a qualifying mutation response reroutes the same long-lived client while the old leader remains unavailable.
4. Voluntary leader move: normal responses move the hint to the new leader with no periodic `Status` call.
5. Endpoint change: stale-generation results are rejected and no discovery RPC or refresh timer appears.
6. Proxy ambiguity: a route that reaches multiple member IDs stays on round-robin.
7. Data safety: use a unique key and expected value for each attempt; every acknowledged PUT is present, and every timed-out PUT is absent or has exactly the submitted value. DELETE and TXN checks use their operation-specific postconditions.
8. Long idle window: the client emits no `Status` calls at startup, while active, or while idle.

#### Performance and reliability gates

Performance validation must keep the leader and term stable, prove both routing mechanisms, and compare equal payloads with applied-index barriers. In the three-voter local test, require at least 15% fewer peer bytes per successful 64-KiB PUT: 25% is the model's prediction, and the lower gate allows heartbeat noise. Treat loopback latency as a non-regression guard, not a promised gain; the current test rejects a leader-aware p95 more than 50% worse than round-robin.

Reliability acceptance must be deterministic. Under a paused follower, leader-aware traffic has zero failed writes and zero first hops to that follower while round-robin proves the fault. Under a paused leader, observe the stale hint, independently confirm election, require application progress through fallback, then require consecutive one-attempt writes to the new leader. Bound the stale cohort from the test's request deadline and concurrency, not a percentage chosen after the run.

#### AWS validation

Run this separately after credentials are available:

- place one etcd voter in each of three Availability Zones;
- run equal-value, equal-count round-robin and leader-aware blocks;
- measure peer and client bytes, ENI bytes, CPU, proposal latency, and `DataTransfer-Regional-Bytes`;
- record average mutation size and write QPS;
- report savings only after subtracting any client-path increase.

### Measurement and rollback

Do not add a client metrics framework in the first patch. The integration test can record picks and hint changes directly, while existing server metrics cover the material cost claim. If production demand later justifies client counters, keep labels bounded and never label by endpoint or member ID.

During rollout, compare:

- mutation success rate and maximum no-success gap;
- p50, p95, and p99 mutation latency;
- `etcd_network_peer_sent_bytes_total` per successful mutation;
- peer send failures and round-trip time;
- etcd CPU and network saturation;
- `Status` QPS attributable to this policy, which must remain zero;
- AWS regional transfer bytes where applicable.

Rollback is configuration-only: disable the opt-in policy and return to native round-robin. Servers can continue returning `leader_id`; clients that do not use it are unaffected.

### Non-goals

- changing the default balancer;
- leader-aware routing for serializable reads or watches;
- changing retry counts or write semantics;
- promising lower latency on every topology;
- claiming a 25% AWS bill reduction;
- sharing leader state across unrelated clients in the first version;
- changing server-side forwarding or Raft replication.

## Closest upstream work

No exact issue for response-carried leader identity plus mutation leader-aware routing was found. That is a search result, not proof that no duplicate exists. Related discussions define useful review boundaries:

- [etcd #9157](https://github.com/etcd-io/etcd/issues/9157) asked for the v3 equivalent of the v2 client's [`EndpointSelectionPrioritizeLeader`](https://pkg.go.dev/go.etcd.io/etcd/client/v2#EndpointSelectionMode), which routed requests directly to the leader to skip follower forwarding. This proposal is that ask, restated against the current gRPC balancer API.
- [etcd #10941](https://github.com/etcd-io/etcd/issues/10941) left leader prioritization as a client balancer TODO.
- [etcd #14501](https://github.com/etcd-io/etcd/issues/14501) documents severe write degradation when traffic lands on a slow follower.
- [etcd #15918](https://github.com/etcd-io/etcd/issues/15918) discusses locality-aware balancing and cloud cost.
- [etcd #15145](https://github.com/etcd-io/etcd/issues/15145) and [grpc-go #6472](https://github.com/grpc/grpc-go/issues/6472) cover the experimental resolver and balancer boundary.
- [etcd #21660](https://github.com/etcd-io/etcd/issues/21660) is a reminder not to add resolver publication churn.
- [etcd #18815](https://github.com/etcd-io/etcd/issues/18815) is one reason to leave watches distributed.

## Upstream sequence

1. Open one design issue that fixes both phases' contracts, compatibility matrix, and evidence boundary.
2. Send Phase 1 as a small protobuf/server PR with cross-version and three-member tests.
3. Develop the response-driven Phase 2 prototype against the Phase 1 branch; a server release is not required to validate the design.
4. Resolve the supported grpc-go picked-route boundary before merging Phase 2.
5. Repeat the local first-hop, fault, idle, and peer-byte E2Es with zero `Status` calls.
6. Land Phase 2 as its own PR only after Phase 1's field contract lands; ship it disabled by default.
7. Propose Kubernetes integration only after server/client compatibility and operational overhead are measured.

The companion `SetEndpoints` fix can be sent as its own PR at any point; it has no ordering constraint against either phase.

## Companion fix: canonical endpoint order in `SetEndpoints`

This fix stands apart from leader-aware routing and can land upstream as its own PR at any time.

`Sync` rebuilds the client endpoint list from `MemberList`, and the member order in that response is not stable across calls. Upstream `SetEndpoints` replaces the list verbatim, so a pure reordering is indistinguishable from a membership change. That resets any client state fenced by endpoint identity: this prototype's endpoint generation, which guards the leader hint, bumps on every permutation. Each bump republishes resolver state — the churn [etcd #21660](https://github.com/etcd-io/etcd/issues/21660) asks clients to avoid.

The prototype gives `SetEndpoints` an options parameter with one opt-in:

```go
func (c *Client) SetEndpoints(eps []string, opts ...SetEndpointsOption)

c.SetEndpoints(members, clientv3.WithSortedEndpoints())
```

`Sync` passes `WithSortedEndpoints()`, so a permutation compares equal; `SetEndpoints` treats any unchanged list as a no-op, so no generation bump, hint clear, or resolver republication follows. Callers passing a user-ordered list keep upstream behavior: the first configured endpoint still selects the dial authority and scheme-based credentials, and sorting is never applied implicitly.

Leader-aware routing does not depend on this change for correctness. Without it, a permuted `Sync` forces one rediscovery round — a few `Status` probes and a round-robin window — and leader-aware routing resumes. The fix removes that repeated churn.
