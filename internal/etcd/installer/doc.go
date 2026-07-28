// Package installer downloads, verifies, and stages etcd binaries (etcd, etcdctl, etcdutl).
//
// WHY NAME: "installer" for download-and-stage semantics, consistent with
// installer convention used by the source repository.
//
// WHY PATH: Under etcd/ because it fetches etcd-specific binaries with
// etcd-specific version handling and checksum verification.
//
// OWNS: versioned etcd/etcdctl/etcdutl binary downloads with checksum manifest
// verification, offline artifact directory support, pre-extracted tarball extraction,
// download retry logic, platform-specific binary URL construction.
//
// DOES NOT OWN: starting/stopping the etcd service (internal/etcd/member), cluster
// configuration (internal/etcd/clusterconfig), control-plane binary installation
// in the source repository.
package installer
