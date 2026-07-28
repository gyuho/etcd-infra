// Package aws implements compute management for AWS EC2.
//
// WHY NAME: "aws" - matches the AWS ecosystem name. Standard Go convention for cloud
// SDK wrappers.
//
// WHY PATH: Under pkg/providers/ alongside the compute interfaces it implements. In pkg/
// because session/compute_reader_registry.go and other internal packages construct AWS
// managers via this public API.
//
// OWNS: Manager (compute.Provider + ReadinessWaiter + GroupScaler), instanceInfo
// (Instance + SSHInstance + InstanceMetadata), EC2/SSM transport, ASG scaling, instance
// readiness polling.
//
// DOES NOT OWN: AWS infrastructure provisioning (internal/clusterlifecycle/providers/aws/),
// S3 operations (pkg/providers/aws/s3/).
//
// TRANSPORT: Commands execute via AWS Systems Manager (SSM), not SSH.
// SSH is available for diagnostics only (SSHInstance is implemented on
// instanceInfo, but SSM is the primary execution path). This means no
// SSH key management is required for command execution.
//
// IMPLEMENTED INTERFACES:
//
//	Manager:   compute.Provider, compute.ReadinessWaiter, compute.GroupScaler
//	Instance:  compute.Instance, compute.SSHInstance (diagnostics), compute.InstanceMetadata
//
// CAPABILITIES REPORTED:
//
//	LifecycleCreateDelete, PowerControl, InventoryRead, CommandExecution,
//	SSHAccess, ReadinessWait, InstanceMetadata, GroupScaling (conditional on ASG client)
//
// KEY DESIGN DECISIONS:
//   - SSM over SSH: eliminates SSH key rotation, works through NAT, audit-logged.
//   - GroupScaler via EC2 Auto Scaling Groups; conditional on ASG client being set.
//   - ReadinessWaiter polls EC2 instance state + SSM connectivity probes.
//   - Instance IDs are EC2 instance IDs (e.g., "i-0123456789abcdef0").
//   - InstanceMetadata exposes EC2 tags and availability zone.
//
// NOT IMPLEMENTED: StreamingInstance, FileTransferInstance, PollableCommandInstance.
// SSM SendCommand is synchronous-polling (fire-and-wait), not streaming.
//
// This is the reference for API-based (non-SSH) cloud providers.
// See docs/add-provider.md for the full provider integration guide.
package aws
