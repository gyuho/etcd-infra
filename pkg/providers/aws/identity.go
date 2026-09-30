package aws

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type stsAPI interface {
	GetCallerIdentity(ctx context.Context, input *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// CallerIdentity is who the current credentials authenticate as.
type CallerIdentity struct {
	Account string
	// ARN is the caller's ARN; Partition is its partition ("aws",
	// "aws-cn", "aws-us-gov"), needed to build IAM ARNs.
	ARN       string
	Partition string
}

// AccountID returns the AWS account of the current credentials.
// GetCallerIdentity needs no IAM permission. Teardown compares it with the
// account recorded at creation before trusting any "not found" answer.
func (m *Manager) AccountID(ctx context.Context) (string, error) {
	id, err := m.CallerIdentity(ctx)
	if err != nil {
		return "", err
	}
	return id.Account, nil
}

// CallerIdentity returns the account, ARN, and partition of the current
// credentials.
func (m *Manager) CallerIdentity(ctx context.Context) (CallerIdentity, error) {
	if m.sts == nil {
		return CallerIdentity{}, errors.New("aws: sts client is nil")
	}
	out, err := m.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return CallerIdentity{}, fmt.Errorf("aws: get caller identity: %w", err)
	}
	id := CallerIdentity{Account: aws.ToString(out.Account), ARN: aws.ToString(out.Arn)}
	if id.Account == "" {
		return CallerIdentity{}, errors.New("aws: caller identity has no account")
	}
	// arn:<partition>:sts::<account>:assumed-role/...
	if parts := strings.SplitN(id.ARN, ":", 3); len(parts) == 3 && parts[0] == "arn" && parts[1] != "" {
		id.Partition = parts[1]
	} else {
		return CallerIdentity{}, fmt.Errorf("aws: caller identity has malformed ARN %q", id.ARN)
	}
	return id, nil
}
