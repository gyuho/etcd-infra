// Package stress orchestrates etcd stress test execution against a running cluster.
//
// WHY NAME: "stress" denotes load/throughput testing, distinct from correctness-focused
// conformance tests in etcd/conformance.
//
// WHY PATH: Under etcd/ because stress workflows target etcd cluster throughput
// and latency, not Kubernetes API load. Sibling to etcd/conformance.
//
// OWNS: stress workflow configuration and validation, TLS client setup, scenario
// resolution from stress/scenarios registry, workload execution with result
// collection and health-retry logic.
//
// DOES NOT OWN: workload generators and scenario definitions (internal/etcd/stress/scenarios),
// remote VM test dispatch (internal/etcd/testrunner), conformance testing
// (internal/etcd/conformance).
package stress
