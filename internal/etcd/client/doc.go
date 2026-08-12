// Package client constructs etcd clients and provides quorum-aware health
// polling for etcd client connections.
//
// WHY NAME: "client" because it wraps the etcd client SDK (clientv3), not a
// general-purpose etcd client library.
//
// WHY PATH: Under etcd/ because the helpers are specific to etcd's client
// protocol, endpoint status API, and quorum semantics.
//
// OWNS: the New/Mode construction choke point that selects the upstream
// clientv3 build or the copied build with the etcd_leader_aware balancer,
// switched by the etcd_infra_custom_client build tag; the WaitForClusterHealthy
// polling loop that waits for a quorum of etcd endpoints to respond to Status
// RPCs within a deadline.
//
// DOES NOT OWN: HTTP endpoint health probes (internal/etcd/checker), cluster
// membership operations (internal/etcd/member), cluster config loading
// (internal/etcd/clusterconfig).
package client
