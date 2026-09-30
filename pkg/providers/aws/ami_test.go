package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUbuntuAMIParameter(t *testing.T) {
	t.Parallel()

	name, err := UbuntuAMIParameter("24.04", "amd64")
	require.NoError(t, err)
	assert.Equal(t, "/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id", name)

	// Canonical publishes gp2 images for releases before 23.10.
	name, err = UbuntuAMIParameter("22.04", "arm64")
	require.NoError(t, err)
	assert.Equal(t, "/aws/service/canonical/ubuntu/server/22.04/stable/current/arm64/hvm/ebs-gp2/ami-id", name)

	_, err = UbuntuAMIParameter("23.04", "amd64")
	require.ErrorContains(t, err, "unsupported Ubuntu release")
	_, err = UbuntuAMIParameter("24.04", "386")
	require.ErrorContains(t, err, "unsupported Ubuntu architecture")
}

func TestResolveSSMParameter(t *testing.T) {
	t.Parallel()

	const name = "/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id"

	fake := &fakeSSM{paramOutput: &ssm.GetParametersOutput{
		Parameters: []ssmtypes.Parameter{{Name: aws.String(name), Value: aws.String("ami-0123\n")}},
	}}
	value, err := newWithClients(nil, fake).ResolveSSMParameter(context.Background(), name)
	require.NoError(t, err)
	assert.Equal(t, "ami-0123", value)
	assert.Equal(t, []string{name}, fake.paramInput.Names)

	// Unknown names come back in InvalidParameters, not as an API error.
	fake = &fakeSSM{paramOutput: &ssm.GetParametersOutput{InvalidParameters: []string{name}}}
	_, err = newWithClients(nil, fake).ResolveSSMParameter(context.Background(), name)
	require.ErrorContains(t, err, "not found in this region")

	fake = &fakeSSM{paramOutput: &ssm.GetParametersOutput{
		Parameters: []ssmtypes.Parameter{{Name: aws.String(name), Value: aws.String(" ")}},
	}}
	_, err = newWithClients(nil, fake).ResolveSSMParameter(context.Background(), name)
	require.ErrorContains(t, err, "is empty")

	fake = &fakeSSM{paramErr: errors.New("AccessDeniedException")}
	_, err = newWithClients(nil, fake).ResolveSSMParameter(context.Background(), name)
	require.ErrorContains(t, err, "AccessDeniedException")
}
