// Package log provides logging setup and helpers for etcd-infra components.
//
// WHY NAME: "log": structured logging configuration and convenience wrappers.
// WHY PATH: Under pkg/ because etcd-infra commands and packages share logging setup.
// OWNS: Logger initialization, log level configuration, and structured logging helpers.
// DOES NOT OWN: Colored process output (pkg/process/proc) or progress spinners (pkg/progress).
package log
