package aws

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
)

// LoadDefaultConfig loads AWS SDK config using the default credential chain.
// If region is empty, it lets the SDK resolve region from the environment/profile.
func LoadDefaultConfig(ctx context.Context, region string) (awssdk.Config, error) {
	if region == "" {
		return config.LoadDefaultConfig(ctx) //nolint:wrapcheck // External package error
	}
	return config.LoadDefaultConfig(ctx, config.WithRegion(region)) //nolint:wrapcheck // External package error
}
