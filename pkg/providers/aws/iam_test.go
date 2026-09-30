package aws

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var iamNotFound = &smithy.GenericAPIError{Code: "NoSuchEntity"}

// fakeIAM models one customer-managed policy and its versions. Unused
// iamAPI methods panic via the nil embedded interface.
type fakeIAM struct {
	iamAPI

	policy   *iamtypes.Policy // nil: absent
	versions []iamtypes.PolicyVersion
	docs     map[string]string // version ID -> raw document

	created *iam.CreatePolicyInput
	calls   []string
}

func (f *fakeIAM) GetPolicy(context.Context, *iam.GetPolicyInput, ...func(*iam.Options)) (*iam.GetPolicyOutput, error) {
	if f.policy == nil {
		return nil, iamNotFound
	}
	return &iam.GetPolicyOutput{Policy: f.policy}, nil
}

func (f *fakeIAM) CreatePolicy(_ context.Context, in *iam.CreatePolicyInput, _ ...func(*iam.Options)) (*iam.CreatePolicyOutput, error) {
	f.created = in
	return &iam.CreatePolicyOutput{}, nil
}

func (f *fakeIAM) GetPolicyVersion(_ context.Context, in *iam.GetPolicyVersionInput, _ ...func(*iam.Options)) (*iam.GetPolicyVersionOutput, error) {
	// IAM returns documents URL-encoded.
	doc := url.PathEscape(f.docs[aws.ToString(in.VersionId)])
	return &iam.GetPolicyVersionOutput{PolicyVersion: &iamtypes.PolicyVersion{Document: aws.String(doc)}}, nil
}

func (f *fakeIAM) ListPolicyVersions(context.Context, *iam.ListPolicyVersionsInput, ...func(*iam.Options)) (*iam.ListPolicyVersionsOutput, error) {
	return &iam.ListPolicyVersionsOutput{Versions: f.versions}, nil
}

func (f *fakeIAM) CreatePolicyVersion(_ context.Context, in *iam.CreatePolicyVersionInput, _ ...func(*iam.Options)) (*iam.CreatePolicyVersionOutput, error) {
	if len(f.versions) >= maxPolicyVersions {
		return nil, &smithy.GenericAPIError{Code: "LimitExceeded"}
	}
	f.calls = append(f.calls, "CreatePolicyVersion default="+map[bool]string{true: "true", false: "false"}[in.SetAsDefault])
	return &iam.CreatePolicyVersionOutput{}, nil
}

func (f *fakeIAM) DeletePolicyVersion(_ context.Context, in *iam.DeletePolicyVersionInput, _ ...func(*iam.Options)) (*iam.DeletePolicyVersionOutput, error) {
	id := aws.ToString(in.VersionId)
	f.calls = append(f.calls, "DeletePolicyVersion "+id)
	for i, v := range f.versions {
		if aws.ToString(v.VersionId) == id {
			f.versions = append(f.versions[:i], f.versions[i+1:]...)
			break
		}
	}
	return &iam.DeletePolicyVersionOutput{}, nil
}

const testPolicyARN = "arn:aws:iam::111111111111:policy/etcd-infra-aws-e2e"

func policyVersion(id string, isDefault bool, age time.Duration) iamtypes.PolicyVersion {
	return iamtypes.PolicyVersion{
		VersionId: aws.String(id), IsDefaultVersion: isDefault,
		CreateDate: aws.Time(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(-age)),
	}
}

func existingPolicyFake(doc string, versions ...iamtypes.PolicyVersion) *fakeIAM {
	return &fakeIAM{
		policy:   &iamtypes.Policy{Arn: aws.String(testPolicyARN), DefaultVersionId: aws.String("v1")},
		versions: versions,
		docs:     map[string]string{"v1": doc},
	}
}

func TestEnsureManagedPolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const doc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`

	// Absent: created under the ARN's name, with the tags.
	f := &fakeIAM{}
	state, err := (&Manager{iam: f}).EnsureManagedPolicy(ctx, testPolicyARN, doc, "d", map[string]string{"k": "v"})
	require.NoError(t, err)
	assert.Equal(t, PolicyCreated, state)
	assert.Equal(t, "etcd-infra-aws-e2e", aws.ToString(f.created.PolicyName))
	assert.Equal(t, []iamtypes.Tag{{Key: aws.String("k"), Value: aws.String("v")}}, f.created.Tags)

	// Same document modulo whitespace and key order (as IAM may return it):
	// no new version, or reruns would churn through the 5-version limit.
	reformatted := "{\n  \"Statement\": [{\"Resource\": \"*\", \"Action\": \"s3:GetObject\", \"Effect\": \"Allow\"}],\n  \"Version\": \"2012-10-17\"\n}"
	f = existingPolicyFake(reformatted, policyVersion("v1", true, 0))
	state, err = (&Manager{iam: f}).EnsureManagedPolicy(ctx, testPolicyARN, doc, "d", nil)
	require.NoError(t, err)
	assert.Equal(t, PolicyUnchanged, state)
	assert.Empty(t, f.calls)

	// Different document: becomes the new default version.
	f = existingPolicyFake(`{"Version":"2012-10-17","Statement":[]}`, policyVersion("v1", true, 0))
	state, err = (&Manager{iam: f}).EnsureManagedPolicy(ctx, testPolicyARN, doc, "d", nil)
	require.NoError(t, err)
	assert.Equal(t, PolicyUpdated, state)
	assert.Equal(t, []string{"CreatePolicyVersion default=true"}, f.calls)

	// At the 5-version limit the oldest non-default version is pruned; the
	// default is kept even when it is the oldest.
	f = existingPolicyFake(`{"Version":"2012-10-17","Statement":[]}`,
		policyVersion("v1", true, 50*time.Hour),
		policyVersion("v2", false, 10*time.Hour),
		policyVersion("v3", false, 40*time.Hour),
		policyVersion("v4", false, 20*time.Hour),
		policyVersion("v5", false, 30*time.Hour),
	)
	_, err = (&Manager{iam: f}).EnsureManagedPolicy(ctx, testPolicyARN, doc, "d", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"DeletePolicyVersion v3", "CreatePolicyVersion default=true"}, f.calls)

	_, err = (&Manager{iam: &fakeIAM{}}).EnsureManagedPolicy(ctx, testPolicyARN, "{not json", "d", nil)
	require.ErrorContains(t, err, "not valid JSON")
}

type fakeSTS struct{ arn string }

func (f fakeSTS) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	return &sts.GetCallerIdentityOutput{Account: aws.String("111111111111"), Arn: aws.String(f.arn)}, nil
}

// The partition feeds every IAM ARN the setup builds.
func TestCallerIdentityPartition(t *testing.T) {
	t.Parallel()
	for arn, want := range map[string]string{
		"arn:aws:sts::111111111111:assumed-role/Admin/me": "aws",
		"arn:aws-cn:iam::111111111111:user/me":            "aws-cn",
		"arn:aws-us-gov:iam::111111111111:user/me":        "aws-us-gov",
	} {
		id, err := (&Manager{sts: fakeSTS{arn: arn}}).CallerIdentity(context.Background())
		require.NoError(t, err)
		assert.Equal(t, want, id.Partition, arn)
	}
	_, err := (&Manager{sts: fakeSTS{arn: "garbage"}}).CallerIdentity(context.Background())
	require.ErrorContains(t, err, "malformed ARN")
}
