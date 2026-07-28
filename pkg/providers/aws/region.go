package aws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"git.tbd/etcd-infra/pkg/providers/aws/ec2/metadata"
)

// ResolveRegion determines the AWS region using a three-step fallback chain:
//
//  1. explicit - value passed directly (typically from a CLI flag)
//  2. AWS_REGION environment variable
//  3. IMDS (EC2 Instance Metadata Service) - only works on EC2 instances
//
// Returns an error if all three sources are empty.
func ResolveRegion(ctx context.Context, explicit string) (string, error) {
	if r := strings.TrimSpace(explicit); r != "" {
		return r, nil
	}
	if r := strings.TrimSpace(os.Getenv("AWS_REGION")); r != "" {
		return r, nil
	}

	metaCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	region, err := metadata.FetchRegion(metaCtx)
	if err != nil {
		return "", fmt.Errorf("detect region from IMDS: %w", err)
	}
	region = strings.TrimSpace(region)
	if region == "" {
		return "", errors.New("aws region is empty (set --region, AWS_REGION, or run on EC2)")
	}
	return region, nil
}
