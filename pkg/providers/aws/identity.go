package aws

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type stsAPI interface {
	GetCallerIdentity(ctx context.Context, input *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// AccountID returns the AWS account of the current credentials.
// GetCallerIdentity needs no IAM permission. Teardown compares it with the
// account recorded at creation before trusting an InvalidInstanceID.NotFound.
func (m *Manager) AccountID(ctx context.Context) (string, error) {
	if m.sts == nil {
		return "", errors.New("aws: sts client is nil")
	}
	out, err := m.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("aws: get caller identity: %w", err)
	}
	account := aws.ToString(out.Account)
	if account == "" {
		return "", errors.New("aws: caller identity has no account")
	}
	return account, nil
}
