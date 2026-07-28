// Package scenarios defines the etcd stress scenario catalog (21 workloads) and workload generators.
//
// WHY NAME: "scenarios" holds workload type definitions, ID enums, and generator
// functions, not the orchestration engine that dispatches them.
//
// WHY PATH: Under etcd/stress/ to scope to stress testing; conformance scenarios
// live separately in etcd/conformance/scenarios.
//
// OWNS: stress scenario ID enum and generated registry, StressRunner contracts,
// workload parameter types, load generator and metrics collection, individual
// workload implementations (concurrent puts, burst writes, sustained load,
// watch churn, leader election contention, large values, pagination, and others),
// workload execution harness, code generation (GenerateStress) for README,
// ADD-TESTS, and ID map files.
//
// DOES NOT OWN: stress orchestration and workflow (internal/etcd/stress),
// remote VM test dispatch (internal/etcd/testrunner), conformance scenarios
// (internal/etcd/conformance/scenarios).
package scenarios
