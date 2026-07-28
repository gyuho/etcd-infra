// Package randutil provides random helpers used across etcd-infra.
//
// WHY NAME: "rand": cryptographically secure random value generation.
// WHY PATH: Under pkg/ so both internal/ and cmd/ can generate tokens, IDs, and random strings.
// OWNS: Random string, token, and byte-slice generation using crypto/rand.
// DOES NOT OWN: Secret management (pkg/secrets) or bootstrap token lifecycle (internal/).
package randutil
