// Package metadata wraps the EC2 Instance Metadata Service (IMDS).
//
// WHY NAME: "metadata" - matches the AWS EC2 Instance Metadata Service concept.
//
// WHY PATH: Under pkg/providers/aws/ec2/ because IMDS is an EC2-specific service
// accessed from within running instances.
//
// OWNS: IMDS client, instance identity queries (region, availability zone, instance ID).
//
// DOES NOT OWN: EC2 API operations (parent ec2/ package), instance lifecycle management.
package metadata
