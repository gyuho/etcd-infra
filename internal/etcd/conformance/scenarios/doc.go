// Package scenarios defines the etcd conformance scenario catalog (132 scenarios) and runner contracts.
//
// WHY NAME: "scenarios" holds scenario type definitions, ID enums, and runner
// functions, not the orchestration engine that dispatches them.
//
// WHY PATH: Under etcd/conformance/ to scope to conformance testing; stress
// scenarios live separately in etcd/stress/scenarios.
//
// OWNS: conformance scenario ID enum and generated registry, scenario runner
// contracts (ScenarioRunner interface), individual scenario implementations
// (KV, watch, txn, lease, concurrency, maintenance, cluster membership, TLS, leasing, mirror),
// code generation (Generate) for README, ID maps, and scenario stubs.
//
// DOES NOT OWN: conformance orchestration and workflow (internal/etcd/conformance),
// remote VM test dispatch (internal/etcd/testrunner), stress scenarios
// (internal/etcd/stress/scenarios).
package scenarios
