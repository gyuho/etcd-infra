// Package client provides quorum-aware health polling for etcd client connections.
//
// WHY NAME: "client" because it wraps the etcd client SDK (clientv3) with
// polling and retry helpers, not a general-purpose etcd client library.
//
// WHY PATH: Under etcd/ because the helpers are specific to etcd's client
// protocol, endpoint status API, and quorum semantics.
//
// OWNS: WaitForClusterHealthy polling loop that waits for a quorum of etcd
// endpoints to respond to Status RPCs within a deadline.
//
// DOES NOT OWN: HTTP endpoint health probes (internal/etcd/checker), cluster
// membership operations (internal/etcd/member), cluster config loading
// (internal/etcd/clusterconfig).
package client
