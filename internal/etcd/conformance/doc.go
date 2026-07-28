// Package conformance orchestrates etcd conformance test execution against a running cluster.
//
// WHY NAME: "conformance" denotes correctness-focused testing (linearizability,
// watch contracts, transactions), distinct from load-focused stress tests.
//
// WHY PATH: Under etcd/ because these tests validate etcd cluster behavior
// (not Kubernetes API conformance). Sibling to etcd/stress.
//
// OWNS: conformance workflow configuration and validation, TLS client setup,
// scenario resolution from conformance/scenarios registry, parallel and sequential
// scenario execution with result collection and JSON/YAML output.
//
// DOES NOT OWN: scenario definitions and runner contracts (internal/etcd/conformance/scenarios),
// remote VM test dispatch (internal/etcd/testrunner), stress testing (internal/etcd/stress).
package conformance
