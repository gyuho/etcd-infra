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

// fakeIAM models one policy's versions plus the user and role cleanup
// listings. Unused iamAPI methods panic via the nil embedded interface.
type fakeIAM struct {
	iamAPI

	policy   *iamtypes.Policy // nil: absent
	versions []iamtypes.PolicyVersion
	docs     map[string]string // version ID -> raw document
	// attachmentsByPoll are returned by successive GetPolicy calls.
	attachmentsByPoll []int32

	created *iam.CreatePolicyInput
	calls   []string

	userExists bool
	keys       []string
	groups     []string
	attached   []string
	inline     []string

	mfa          []string // serial numbers
	sshKeys      []string
	certs        []string
	serviceCreds []string
}

func (f *fakeIAM) GetPolicy(context.Context, *iam.GetPolicyInput, ...func(*iam.Options)) (*iam.GetPolicyOutput, error) {
	if f.policy == nil {
		return nil, iamNotFound
	}
	p := *f.policy
	if len(f.attachmentsByPoll) > 0 {
		p.AttachmentCount = aws.Int32(f.attachmentsByPoll[0])
		if len(f.attachmentsByPoll) > 1 {
			f.attachmentsByPoll = f.attachmentsByPoll[1:]
		}
	}
	return &iam.GetPolicyOutput{Policy: &p}, nil
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

func (f *fakeIAM) DeletePolicy(context.Context, *iam.DeletePolicyInput, ...func(*iam.Options)) (*iam.DeletePolicyOutput, error) {
	f.calls = append(f.calls, "DeletePolicy")
	f.policy = nil
	return &iam.DeletePolicyOutput{}, nil
}

func (f *fakeIAM) GetUser(_ context.Context, in *iam.GetUserInput, _ ...func(*iam.Options)) (*iam.GetUserOutput, error) {
	if !f.userExists {
		return nil, iamNotFound
	}
	return &iam.GetUserOutput{User: &iamtypes.User{Arn: aws.String("arn:aws:iam::1:user/" + aws.ToString(in.UserName))}}, nil
}

func (f *fakeIAM) ListAccessKeys(context.Context, *iam.ListAccessKeysInput, ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error) {
	out := &iam.ListAccessKeysOutput{}
	for _, k := range f.keys {
		out.AccessKeyMetadata = append(out.AccessKeyMetadata, iamtypes.AccessKeyMetadata{AccessKeyId: aws.String(k)})
	}
	return out, nil
}

func (f *fakeIAM) DeleteAccessKey(_ context.Context, in *iam.DeleteAccessKeyInput, _ ...func(*iam.Options)) (*iam.DeleteAccessKeyOutput, error) {
	f.calls = append(f.calls, "DeleteAccessKey "+aws.ToString(in.AccessKeyId))
	return &iam.DeleteAccessKeyOutput{}, nil
}

func (f *fakeIAM) DeleteLoginProfile(context.Context, *iam.DeleteLoginProfileInput, ...func(*iam.Options)) (*iam.DeleteLoginProfileOutput, error) {
	f.calls = append(f.calls, "DeleteLoginProfile")
	return nil, iamNotFound // no console password: must not fail the delete
}

func (f *fakeIAM) ListMFADevices(context.Context, *iam.ListMFADevicesInput, ...func(*iam.Options)) (*iam.ListMFADevicesOutput, error) {
	out := &iam.ListMFADevicesOutput{}
	for _, s := range f.mfa {
		out.MFADevices = append(out.MFADevices, iamtypes.MFADevice{SerialNumber: aws.String(s)})
	}
	return out, nil
}

func (f *fakeIAM) DeactivateMFADevice(_ context.Context, in *iam.DeactivateMFADeviceInput, _ ...func(*iam.Options)) (*iam.DeactivateMFADeviceOutput, error) {
	f.calls = append(f.calls, "DeactivateMFADevice "+aws.ToString(in.SerialNumber))
	return &iam.DeactivateMFADeviceOutput{}, nil
}

func (f *fakeIAM) DeleteVirtualMFADevice(_ context.Context, in *iam.DeleteVirtualMFADeviceInput, _ ...func(*iam.Options)) (*iam.DeleteVirtualMFADeviceOutput, error) {
	f.calls = append(f.calls, "DeleteVirtualMFADevice "+aws.ToString(in.SerialNumber))
	return &iam.DeleteVirtualMFADeviceOutput{}, nil
}

func (f *fakeIAM) ListSSHPublicKeys(context.Context, *iam.ListSSHPublicKeysInput, ...func(*iam.Options)) (*iam.ListSSHPublicKeysOutput, error) {
	out := &iam.ListSSHPublicKeysOutput{}
	for _, k := range f.sshKeys {
		out.SSHPublicKeys = append(out.SSHPublicKeys, iamtypes.SSHPublicKeyMetadata{SSHPublicKeyId: aws.String(k)})
	}
	return out, nil
}

func (f *fakeIAM) DeleteSSHPublicKey(_ context.Context, in *iam.DeleteSSHPublicKeyInput, _ ...func(*iam.Options)) (*iam.DeleteSSHPublicKeyOutput, error) {
	f.calls = append(f.calls, "DeleteSSHPublicKey "+aws.ToString(in.SSHPublicKeyId))
	return &iam.DeleteSSHPublicKeyOutput{}, nil
}

func (f *fakeIAM) ListSigningCertificates(context.Context, *iam.ListSigningCertificatesInput, ...func(*iam.Options)) (*iam.ListSigningCertificatesOutput, error) {
	out := &iam.ListSigningCertificatesOutput{}
	for _, c := range f.certs {
		out.Certificates = append(out.Certificates, iamtypes.SigningCertificate{CertificateId: aws.String(c)})
	}
	return out, nil
}

func (f *fakeIAM) DeleteSigningCertificate(_ context.Context, in *iam.DeleteSigningCertificateInput, _ ...func(*iam.Options)) (*iam.DeleteSigningCertificateOutput, error) {
	f.calls = append(f.calls, "DeleteSigningCertificate "+aws.ToString(in.CertificateId))
	return &iam.DeleteSigningCertificateOutput{}, nil
}

func (f *fakeIAM) ListServiceSpecificCredentials(context.Context, *iam.ListServiceSpecificCredentialsInput, ...func(*iam.Options)) (*iam.ListServiceSpecificCredentialsOutput, error) {
	out := &iam.ListServiceSpecificCredentialsOutput{}
	for _, c := range f.serviceCreds {
		out.ServiceSpecificCredentials = append(out.ServiceSpecificCredentials, iamtypes.ServiceSpecificCredentialMetadata{ServiceSpecificCredentialId: aws.String(c)})
	}
	return out, nil
}

func (f *fakeIAM) DeleteServiceSpecificCredential(_ context.Context, in *iam.DeleteServiceSpecificCredentialInput, _ ...func(*iam.Options)) (*iam.DeleteServiceSpecificCredentialOutput, error) {
	f.calls = append(f.calls, "DeleteServiceSpecificCredential "+aws.ToString(in.ServiceSpecificCredentialId))
	return &iam.DeleteServiceSpecificCredentialOutput{}, nil
}

func (f *fakeIAM) ListGroupsForUser(context.Context, *iam.ListGroupsForUserInput, ...func(*iam.Options)) (*iam.ListGroupsForUserOutput, error) {
	out := &iam.ListGroupsForUserOutput{}
	for _, g := range f.groups {
		out.Groups = append(out.Groups, iamtypes.Group{GroupName: aws.String(g)})
	}
	return out, nil
}

func (f *fakeIAM) RemoveUserFromGroup(_ context.Context, in *iam.RemoveUserFromGroupInput, _ ...func(*iam.Options)) (*iam.RemoveUserFromGroupOutput, error) {
	f.calls = append(f.calls, "RemoveUserFromGroup "+aws.ToString(in.GroupName))
	return &iam.RemoveUserFromGroupOutput{}, nil
}

func (f *fakeIAM) ListAttachedUserPolicies(context.Context, *iam.ListAttachedUserPoliciesInput, ...func(*iam.Options)) (*iam.ListAttachedUserPoliciesOutput, error) {
	out := &iam.ListAttachedUserPoliciesOutput{}
	for _, a := range f.attached {
		out.AttachedPolicies = append(out.AttachedPolicies, iamtypes.AttachedPolicy{PolicyArn: aws.String(a)})
	}
	return out, nil
}

func (f *fakeIAM) DetachUserPolicy(_ context.Context, in *iam.DetachUserPolicyInput, _ ...func(*iam.Options)) (*iam.DetachUserPolicyOutput, error) {
	f.calls = append(f.calls, "DetachUserPolicy "+aws.ToString(in.PolicyArn))
	return &iam.DetachUserPolicyOutput{}, nil
}

func (f *fakeIAM) ListUserPolicies(context.Context, *iam.ListUserPoliciesInput, ...func(*iam.Options)) (*iam.ListUserPoliciesOutput, error) {
	return &iam.ListUserPoliciesOutput{PolicyNames: f.inline}, nil
}

func (f *fakeIAM) DeleteUserPolicy(_ context.Context, in *iam.DeleteUserPolicyInput, _ ...func(*iam.Options)) (*iam.DeleteUserPolicyOutput, error) {
	f.calls = append(f.calls, "DeleteUserPolicy "+aws.ToString(in.PolicyName))
	return &iam.DeleteUserPolicyOutput{}, nil
}

func (f *fakeIAM) DeleteUser(context.Context, *iam.DeleteUserInput, ...func(*iam.Options)) (*iam.DeleteUserOutput, error) {
	f.calls = append(f.calls, "DeleteUser")
	f.userExists = false
	return &iam.DeleteUserOutput{}, nil
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

func TestDeleteManagedPolicy(t *testing.T) {
	ctx := context.Background()
	defer func(timeout, poll time.Duration) { iamDetachSettleTimeout, iamPollInterval = timeout, poll }(iamDetachSettleTimeout, iamPollInterval)
	iamDetachSettleTimeout, iamPollInterval = 50*time.Millisecond, time.Millisecond

	// Still attached elsewhere: nothing is touched, not even old versions.
	f := existingPolicyFake("{}", policyVersion("v1", true, 0), policyVersion("v2", false, time.Hour))
	f.attachmentsByPoll = []int32{1}
	err := (&Manager{iam: f}).DeleteManagedPolicy(ctx, testPolicyARN)
	require.ErrorIs(t, err, ErrPolicyInUse)
	assert.Empty(t, f.calls)

	// Attachment counts lag a detach: wait for zero, then delete the
	// non-default versions before the policy (IAM requires it).
	f = existingPolicyFake("{}", policyVersion("v1", true, 0), policyVersion("v2", false, time.Hour))
	f.attachmentsByPoll = []int32{1, 1, 0}
	require.NoError(t, (&Manager{iam: f}).DeleteManagedPolicy(ctx, testPolicyARN))
	assert.Equal(t, []string{"DeletePolicyVersion v2", "DeletePolicy"}, f.calls)

	require.NoError(t, (&Manager{iam: &fakeIAM{}}).DeleteManagedPolicy(ctx, testPolicyARN), "absent policy")
}

// IAM refuses DeleteUser while any credential, group, or policy remains;
// people commonly add a console password and MFA to a user.
func TestDeleteUserRemovesDependenciesFirst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := &fakeIAM{
		userExists: true, keys: []string{"AKIA1", "AKIA2"}, groups: []string{"g"}, attached: []string{testPolicyARN}, inline: []string{"p"},
		mfa: []string{"arn:aws:iam::1:mfa/u", "GAHT12345678"}, sshKeys: []string{"APKA1"}, certs: []string{"cert1"}, serviceCreds: []string{"ACCA1"},
	}
	require.NoError(t, (&Manager{iam: f}).DeleteUser(ctx, "u"))
	assert.Equal(t, []string{
		"DeleteAccessKey AKIA1", "DeleteAccessKey AKIA2", "DeleteLoginProfile",
		// Only the virtual device (ARN serial) is an IAM resource to delete.
		"DeactivateMFADevice arn:aws:iam::1:mfa/u", "DeleteVirtualMFADevice arn:aws:iam::1:mfa/u", "DeactivateMFADevice GAHT12345678",
		"DeleteSSHPublicKey APKA1", "DeleteSigningCertificate cert1", "DeleteServiceSpecificCredential ACCA1",
		"RemoveUserFromGroup g", "DetachUserPolicy " + testPolicyARN, "DeleteUserPolicy p", "DeleteUser",
	}, f.calls)

	f = &fakeIAM{}
	require.NoError(t, (&Manager{iam: f}).DeleteUser(ctx, "u"), "absent user")
	assert.Empty(t, f.calls)
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
