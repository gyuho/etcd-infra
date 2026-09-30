package aws

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// DefaultUbuntuRelease is the Ubuntu Server LTS release used when a caller
// asks for an Ubuntu AMI without naming a release.
const DefaultUbuntuRelease = "24.04"

// ubuntuVolumeTypes maps supported Ubuntu Server releases to the EBS volume
// type segment of Canonical's SSM parameter path: Canonical publishes gp3
// images for 23.10 and later and gp2 images for 23.04 and earlier.
var ubuntuVolumeTypes = map[string]string{
	"22.04": "ebs-gp2",
	"24.04": "ebs-gp3",
	"26.04": "ebs-gp3",
}

// UbuntuAMIParameter returns Canonical's public SSM parameter name that
// always points at the latest stable Ubuntu Server AMI for the release and
// architecture (amd64 or arm64). Format documented by Canonical:
//
//	/aws/service/canonical/ubuntu/server/<release>/stable/current/<arch>/hvm/<vol>/ami-id
func UbuntuAMIParameter(release, arch string) (string, error) {
	vol, ok := ubuntuVolumeTypes[release]
	if !ok {
		return "", fmt.Errorf("aws: unsupported Ubuntu release %q (supported: 22.04, 24.04, 26.04)", release)
	}
	if arch != "amd64" && arch != "arm64" {
		return "", fmt.Errorf("aws: unsupported Ubuntu architecture %q (amd64 or arm64)", arch)
	}
	return fmt.Sprintf("/aws/service/canonical/ubuntu/server/%s/stable/current/%s/hvm/%s/ami-id", release, arch, vol), nil
}

// ResolveSSMParameter returns the value of one SSM parameter, including
// AWS-published public parameters such as AMI IDs. The EC2 API does not
// expand "resolve:ssm:" image references for SDK callers, so AMI parameters
// are resolved here before launch.
func (m *Manager) ResolveSSMParameter(ctx context.Context, name string) (string, error) {
	if m.ssm == nil {
		return "", errors.New("aws: ssm client is nil")
	}
	out, err := m.ssm.GetParameters(ctx, &ssm.GetParametersInput{Names: []string{name}})
	if err != nil {
		return "", fmt.Errorf("aws: get ssm parameter %s: %w", name, err)
	}
	for _, param := range out.Parameters {
		if aws.ToString(param.Name) != name {
			continue
		}
		value := strings.TrimSpace(aws.ToString(param.Value))
		if value == "" {
			return "", fmt.Errorf("aws: ssm parameter %s is empty", name)
		}
		return value, nil
	}
	return "", fmt.Errorf("aws: ssm parameter %s not found in this region", name)
}
