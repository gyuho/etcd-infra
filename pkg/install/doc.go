// Package install provides shared install/download helpers for artifacts.
//
// WHY NAME: "install" describes the action: downloading and placing binary artifacts on disk.
// WHY PATH: Under pkg/ because both CLI tooling and internal provisioning code share download logic.
// OWNS: HTTP download, checksum verification, and binary installation helpers.
// DOES NOT OWN: Artifact manifest definitions (pkg/artifacts) or provider-specific provisioning (internal/).
package install
