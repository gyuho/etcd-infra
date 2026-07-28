// Package archive provides helpers for working with tar and zip archives.
//
// WHY NAME: "archive": tar/zip archive creation and extraction utilities.
// WHY PATH: Under pkg/ because artifact bundling and node provisioning both need archive helpers.
// OWNS: Tar and zip creation, extraction, and stream-based archive processing.
// DOES NOT OWN: Artifact download/caching (pkg/artifacts) or zstd compression for etcd snapshots (internal/).
package archive
