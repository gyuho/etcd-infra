// Package hack embeds the reviewed IAM policy documents in this directory so
// "etcd-infra aws iam up" applies exactly these files: editing a JSON file
// here and rerunning the command is how a policy change is rolled out.
package hack

import _ "embed"

// AWSE2EUserPolicy is the least-privilege policy for the IAM user that runs
// every etcd-infra AWS command (aws-e2e.iam-policy.json).
//
//go:embed aws-e2e.iam-policy.json
var AWSE2EUserPolicy string

// AWSSSMRoleExecPolicy is the tag-scoped execution policy of the
// etcd-infra-ssm instance role (aws-ssm-role-exec.iam-policy.json).
//
//go:embed aws-ssm-role-exec.iam-policy.json
var AWSSSMRoleExecPolicy string
