package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/hack"
	awsprovider "git.tbd/etcd-infra/pkg/providers/aws"
)

// fakeIAMAccount is an in-memory IAM account with IAM's attachment rules.
type fakeIAMAccount struct {
	policies map[string]*fakeIAMPolicy // by ARN
	roles    map[string]*fakeIAMRole
	profiles map[string]*awsprovider.IAMInstanceProfile
	users    map[string]*fakeIAMUser
	creates  []string
}

type fakeIAMPolicy struct {
	doc  string
	tags map[string]string
}

type fakeIAMRole struct {
	tags     map[string]string
	attached []string
}

type fakeIAMUser struct {
	arn      string // default arn:aws:iam::111111111111:user/<name>
	tags     map[string]string
	boundary string
	attached []string
	keys     []string
}

func newFakeIAMAccount() *fakeIAMAccount {
	return &fakeIAMAccount{
		policies: map[string]*fakeIAMPolicy{},
		roles:    map[string]*fakeIAMRole{},
		profiles: map[string]*awsprovider.IAMInstanceProfile{},
		users:    map[string]*fakeIAMUser{},
	}
}

func (f *fakeIAMAccount) EnsureManagedPolicy(_ context.Context, arn, document, _ string, tags map[string]string) (string, error) {
	p, ok := f.policies[arn]
	if !ok {
		f.creates = append(f.creates, "policy "+arn)
		f.policies[arn] = &fakeIAMPolicy{doc: document, tags: maps.Clone(tags)}
		return awsprovider.PolicyCreated, nil
	}
	if p.doc == document {
		return awsprovider.PolicyUnchanged, nil
	}
	p.doc = document
	return awsprovider.PolicyUpdated, nil
}

func (f *fakeIAMAccount) Role(_ context.Context, name string) (*awsprovider.IAMRole, error) {
	r, ok := f.roles[name]
	if !ok {
		return nil, nil
	}
	return &awsprovider.IAMRole{ARN: "arn:aws:iam::1:role/" + name, Tags: r.tags}, nil
}

func (f *fakeIAMAccount) CreateRole(_ context.Context, name, trust, _ string, tags map[string]string) error {
	if !json.Valid([]byte(trust)) {
		return fmt.Errorf("invalid trust policy")
	}
	f.creates = append(f.creates, "role "+name)
	f.roles[name] = &fakeIAMRole{tags: maps.Clone(tags)}
	return nil
}

func (f *fakeIAMAccount) RolePolicyARNs(_ context.Context, role string) ([]string, error) {
	return slices.Clone(f.roles[role].attached), nil
}

func (f *fakeIAMAccount) AttachRolePolicy(_ context.Context, role, arn string) error {
	if !slices.Contains(f.roles[role].attached, arn) {
		f.roles[role].attached = append(f.roles[role].attached, arn)
	}
	return nil
}

func (f *fakeIAMAccount) InstanceProfile(_ context.Context, name string) (*awsprovider.IAMInstanceProfile, error) {
	p, ok := f.profiles[name]
	if !ok {
		return nil, nil
	}
	c := *p
	c.Roles = slices.Clone(p.Roles)
	return &c, nil
}

func (f *fakeIAMAccount) CreateInstanceProfile(_ context.Context, name string, tags map[string]string) error {
	f.creates = append(f.creates, "instance-profile "+name)
	f.profiles[name] = &awsprovider.IAMInstanceProfile{Tags: maps.Clone(tags)}
	return nil
}

func (f *fakeIAMAccount) AddRoleToInstanceProfile(_ context.Context, profile, role string) error {
	f.profiles[profile].Roles = append(f.profiles[profile].Roles, role)
	return nil
}

func (f *fakeIAMAccount) User(_ context.Context, name string) (*awsprovider.IAMUser, error) {
	u, ok := f.users[name]
	if !ok {
		return nil, nil
	}
	arn := u.arn
	if arn == "" {
		arn = "arn:aws:iam::111111111111:user/" + name
	}
	return &awsprovider.IAMUser{ARN: arn, PermissionsBoundary: u.boundary, Tags: u.tags}, nil
}

func (f *fakeIAMAccount) CreateUser(_ context.Context, name, boundary string, tags map[string]string) error {
	f.creates = append(f.creates, "user "+name)
	f.users[name] = &fakeIAMUser{tags: maps.Clone(tags), boundary: boundary}
	return nil
}

func (f *fakeIAMAccount) SetUserPermissionsBoundary(_ context.Context, name, boundary string) error {
	f.users[name].boundary = boundary
	return nil
}

func (f *fakeIAMAccount) UserPolicyARNs(_ context.Context, name string) ([]string, error) {
	return slices.Clone(f.users[name].attached), nil
}

func (f *fakeIAMAccount) AttachUserPolicy(_ context.Context, name, arn string) error {
	f.users[name].attached = append(f.users[name].attached, arn)
	return nil
}

func (f *fakeIAMAccount) AccessKeyIDs(_ context.Context, name string) ([]string, error) {
	return slices.Clone(f.users[name].keys), nil
}

func (f *fakeIAMAccount) CreateAccessKey(_ context.Context, name string) (awsprovider.AccessKey, error) {
	id := fmt.Sprintf("AKIA%d", len(f.users[name].keys)+1)
	f.users[name].keys = append(f.users[name].keys, id)
	return awsprovider.AccessKey{ID: id, Secret: "secret-" + id}, nil
}

func testAWSIAMTarget(opts awsIAMOptions) awsIAMTarget {
	if opts.User == "" {
		opts.User = defaultAWSIAMUser
	}
	return newAWSIAMTarget(awsprovider.CallerIdentity{
		Account: "111111111111", Partition: "aws", ARN: "arn:aws:sts::111111111111:assumed-role/Admin/me",
	}, opts)
}

func runIAM(t *testing.T, fn func(out, log *bytes.Buffer) error) (string, error) {
	t.Helper()
	var out, log bytes.Buffer
	err := fn(&out, &log)
	return out.String(), err
}

func TestAWSIAMCreateUserFreshAccountThenIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	acct := newFakeIAMAccount()
	target := testAWSIAMTarget(awsIAMOptions{Role: true})

	out, err := runIAM(t, func(o, l *bytes.Buffer) error { return awsIAMCreateUser(ctx, acct, target, o, l) })
	require.NoError(t, err)
	assert.Contains(t, out, "user_state=created\n")
	assert.Contains(t, out, "role_state=created\n")

	for _, name := range []string{target.UserPolicyARN, target.RoleExecPolicyARN} {
		assert.True(t, awsIAMManaged(acct.policies[name].tags), name)
	}
	user := acct.users[defaultAWSIAMUser]
	assert.True(t, awsIAMManaged(user.tags))
	assert.Equal(t, target.UserPolicyARN, user.boundary, "the user must be bounded by its own policy")
	assert.Equal(t, []string{target.UserPolicyARN}, user.attached)
	assert.JSONEq(t, hack.AWSE2EUserPolicy, acct.policies[target.UserPolicyARN].doc)
	assert.ElementsMatch(t, append(slices.Clone(target.RoleAWSPolicyARNs), target.RoleExecPolicyARN), acct.roles[awsIAMRoleName].attached)
	assert.Equal(t, []string{awsIAMRoleName}, acct.profiles[awsIAMRoleName].Roles)
	assert.Empty(t, user.keys, "no access key without --access-key")
	creates := len(acct.creates)
	out, err = runIAM(t, func(o, l *bytes.Buffer) error { return awsIAMCreateUser(ctx, acct, target, o, l) })
	require.NoError(t, err)
	assert.Len(t, acct.creates, creates, "a rerun creates nothing")
	assert.Contains(t, out, "user_policy=unchanged\n")
	assert.Contains(t, out, "user_state=existing\n")
}

// Editing hack/aws-e2e.iam-policy.json and rerunning create-user is the rollout path.
func TestAWSIAMCreateUserUpdatesStalePolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	acct := newFakeIAMAccount()
	target := testAWSIAMTarget(awsIAMOptions{Role: true})
	_, err := runIAM(t, func(o, l *bytes.Buffer) error { return awsIAMCreateUser(ctx, acct, target, o, l) })
	require.NoError(t, err)

	acct.policies[target.UserPolicyARN].doc = `{"Version":"2012-10-17","Statement":[]}`
	out, err := runIAM(t, func(o, l *bytes.Buffer) error { return awsIAMCreateUser(ctx, acct, target, o, l) })
	require.NoError(t, err)
	assert.Contains(t, out, "user_policy=updated\n")
	assert.JSONEq(t, hack.AWSE2EUserPolicy, acct.policies[target.UserPolicyARN].doc)
}

// Resources made by hand (README) are adopted: boundary set, policy attached.
func TestAWSIAMCreateUserAdoptsUnmanaged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	acct := newFakeIAMAccount()
	acct.users[defaultAWSIAMUser] = &fakeIAMUser{}
	acct.roles[awsIAMRoleName] = &fakeIAMRole{}
	acct.profiles[awsIAMRoleName] = &awsprovider.IAMInstanceProfile{Roles: []string{awsIAMRoleName}}
	target := testAWSIAMTarget(awsIAMOptions{Role: true})

	out, err := runIAM(t, func(o, l *bytes.Buffer) error { return awsIAMCreateUser(ctx, acct, target, o, l) })
	require.NoError(t, err)
	assert.Contains(t, out, "user_state=existing\n")
	assert.Contains(t, out, "role_state=existing\n")
	user := acct.users[defaultAWSIAMUser]
	assert.Equal(t, target.UserPolicyARN, user.boundary, "adopted user gets the boundary")
	assert.Equal(t, []string{target.UserPolicyARN}, user.attached)
	assert.ElementsMatch(t, append(slices.Clone(target.RoleAWSPolicyARNs), target.RoleExecPolicyARN), acct.roles[awsIAMRoleName].attached)
}

func TestAWSIAMCreateUserAccessKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	acct := newFakeIAMAccount()
	target := testAWSIAMTarget(awsIAMOptions{AccessKey: true})
	out, err := runIAM(t, func(o, l *bytes.Buffer) error { return awsIAMCreateUser(ctx, acct, target, o, l) })
	require.NoError(t, err)
	assert.Contains(t, out, "access_key_id=AKIA1\nsecret_access_key=secret-AKIA1\n")

	_, err = runIAM(t, func(o, l *bytes.Buffer) error { return awsIAMCreateUser(ctx, acct, target, o, l) })
	require.NoError(t, err)
	_, err = runIAM(t, func(o, l *bytes.Buffer) error { return awsIAMCreateUser(ctx, acct, target, o, l) })
	require.ErrorContains(t, err, "already has 2 access keys")
}

func TestAWSIAMCreateUserRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Bounding the caller's own identity would lock it out of IAM; user
	// ARNs may carry a path, so a name-suffix check is not enough.
	acct := newFakeIAMAccount()
	acct.users["admin"] = &fakeIAMUser{arn: "arn:aws:iam::111111111111:user/ops/admin"}
	target := testAWSIAMTarget(awsIAMOptions{User: "admin", Role: true})
	target.CallerARN = "arn:aws:iam::111111111111:user/ops/admin"
	_, err := runIAM(t, func(o, l *bytes.Buffer) error { return awsIAMCreateUser(ctx, acct, target, o, l) })
	require.ErrorContains(t, err, "administrator credentials")
	assert.Empty(t, acct.users["admin"].boundary)
	assert.Empty(t, acct.creates, "refused before any change")

	// An adopted user's existing boundary may be stricter: never replaced.
	acct = newFakeIAMAccount()
	acct.users[defaultAWSIAMUser] = &fakeIAMUser{boundary: "arn:aws:iam::111111111111:policy/strict"}
	_, err = runIAM(t, func(o, l *bytes.Buffer) error {
		return awsIAMCreateUser(ctx, acct, testAWSIAMTarget(awsIAMOptions{}), o, l)
	})
	require.ErrorContains(t, err, "refusing to replace it")
	assert.Equal(t, "arn:aws:iam::111111111111:policy/strict", acct.users[defaultAWSIAMUser].boundary)
	assert.Empty(t, acct.users[defaultAWSIAMUser].attached)

	// An instance profile holding another role is not silently rewired.
	acct = newFakeIAMAccount()
	acct.profiles[awsIAMRoleName] = &awsprovider.IAMInstanceProfile{Roles: []string{"other"}}
	_, err = runIAM(t, func(o, l *bytes.Buffer) error {
		return awsIAMCreateUser(ctx, acct, testAWSIAMTarget(awsIAMOptions{Role: true}), o, l)
	})
	require.ErrorContains(t, err, "holds role other")

	require.ErrorContains(t, validateAWSIAMPartition("aws-cn"), "unsupported")
	require.NoError(t, validateAWSIAMPartition("aws"))
}

// The embedded policies are what "create-user" uploads: they must be valid,
// fit IAM's managed-policy size limit, and name the role it creates.
func TestAWSIAMEmbeddedPolicies(t *testing.T) {
	t.Parallel()
	const maxManagedPolicyChars = 6144 // whitespace excluded
	for name, doc := range map[string]string{"user": hack.AWSE2EUserPolicy, "role exec": hack.AWSSSMRoleExecPolicy} {
		require.True(t, json.Valid([]byte(doc)), name)
		size := len(strings.Join(strings.Fields(doc), ""))
		assert.LessOrEqual(t, size, maxManagedPolicyChars, name)
	}
	assert.Contains(t, hack.AWSE2EUserPolicy, `"arn:aws:iam::*:role/`+awsIAMRoleName+`"`, "user may only pass the role 'etcd-infra aws iam create-user' creates")
}

func TestAWSIAMDryRunAndValidation(t *testing.T) {
	awsTestNoCredentials(t)
	ctx := context.Background()
	require.NoError(t, runAWSIAM(ctx, []string{"create-user", "--access-key"}), "dry run must not reach AWS")
	require.ErrorContains(t, runAWSIAM(ctx, []string{"create-user", "--user", "bad user"}), "invalid --user")
	require.ErrorContains(t, runAWSIAM(ctx, []string{"create-user", "extra"}), "unexpected arguments")
	require.ErrorContains(t, runAWSIAM(ctx, []string{"bogus"}), "unknown aws iam command")
	require.ErrorContains(t, runAWSIAM(ctx, []string{"create-user", "--dry-run=false"}), "identify AWS account")
}
