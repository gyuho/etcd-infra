package aws

// IAM building blocks for "etcd-infra aws iam create-user": idempotent
// create/inspect helpers for customer-managed policies, roles, instance
// profiles, and users. Nothing here deletes a policy, role, profile, or
// user.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
)

// maxPolicyVersions is IAM's limit on stored versions per managed policy.
const maxPolicyVersions = 5

// Policy document states reported by EnsureManagedPolicy.
const (
	PolicyCreated   = "created"
	PolicyUpdated   = "updated"
	PolicyUnchanged = "unchanged"
)

type iamAPI interface {
	GetPolicy(ctx context.Context, input *iam.GetPolicyInput, optFns ...func(*iam.Options)) (*iam.GetPolicyOutput, error)
	CreatePolicy(ctx context.Context, input *iam.CreatePolicyInput, optFns ...func(*iam.Options)) (*iam.CreatePolicyOutput, error)
	GetPolicyVersion(ctx context.Context, input *iam.GetPolicyVersionInput, optFns ...func(*iam.Options)) (*iam.GetPolicyVersionOutput, error)
	ListPolicyVersions(ctx context.Context, input *iam.ListPolicyVersionsInput, optFns ...func(*iam.Options)) (*iam.ListPolicyVersionsOutput, error)
	CreatePolicyVersion(ctx context.Context, input *iam.CreatePolicyVersionInput, optFns ...func(*iam.Options)) (*iam.CreatePolicyVersionOutput, error)
	DeletePolicyVersion(ctx context.Context, input *iam.DeletePolicyVersionInput, optFns ...func(*iam.Options)) (*iam.DeletePolicyVersionOutput, error)

	GetRole(ctx context.Context, input *iam.GetRoleInput, optFns ...func(*iam.Options)) (*iam.GetRoleOutput, error)
	CreateRole(ctx context.Context, input *iam.CreateRoleInput, optFns ...func(*iam.Options)) (*iam.CreateRoleOutput, error)
	AttachRolePolicy(ctx context.Context, input *iam.AttachRolePolicyInput, optFns ...func(*iam.Options)) (*iam.AttachRolePolicyOutput, error)
	ListAttachedRolePolicies(ctx context.Context, input *iam.ListAttachedRolePoliciesInput, optFns ...func(*iam.Options)) (*iam.ListAttachedRolePoliciesOutput, error)

	GetInstanceProfile(ctx context.Context, input *iam.GetInstanceProfileInput, optFns ...func(*iam.Options)) (*iam.GetInstanceProfileOutput, error)
	CreateInstanceProfile(ctx context.Context, input *iam.CreateInstanceProfileInput, optFns ...func(*iam.Options)) (*iam.CreateInstanceProfileOutput, error)
	AddRoleToInstanceProfile(ctx context.Context, input *iam.AddRoleToInstanceProfileInput, optFns ...func(*iam.Options)) (*iam.AddRoleToInstanceProfileOutput, error)

	GetUser(ctx context.Context, input *iam.GetUserInput, optFns ...func(*iam.Options)) (*iam.GetUserOutput, error)
	CreateUser(ctx context.Context, input *iam.CreateUserInput, optFns ...func(*iam.Options)) (*iam.CreateUserOutput, error)
	AttachUserPolicy(ctx context.Context, input *iam.AttachUserPolicyInput, optFns ...func(*iam.Options)) (*iam.AttachUserPolicyOutput, error)
	ListAttachedUserPolicies(ctx context.Context, input *iam.ListAttachedUserPoliciesInput, optFns ...func(*iam.Options)) (*iam.ListAttachedUserPoliciesOutput, error)
	PutUserPermissionsBoundary(ctx context.Context, input *iam.PutUserPermissionsBoundaryInput, optFns ...func(*iam.Options)) (*iam.PutUserPermissionsBoundaryOutput, error)
	ListAccessKeys(ctx context.Context, input *iam.ListAccessKeysInput, optFns ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error)
	CreateAccessKey(ctx context.Context, input *iam.CreateAccessKeyInput, optFns ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error)
}

// iamPolicy identifies a customer-managed policy's default version.
type iamPolicy struct {
	ARN            string
	DefaultVersion string
}

// IAMRole describes a role.
type IAMRole struct {
	ARN  string
	Tags map[string]string
}

// IAMInstanceProfile describes an instance profile and its role names.
type IAMInstanceProfile struct {
	ARN   string
	Roles []string
	Tags  map[string]string
}

// IAMUser describes a user.
type IAMUser struct {
	ARN string
	// PermissionsBoundary is the boundary policy ARN, empty when unset.
	PermissionsBoundary string
	Tags                map[string]string
}

// AccessKey is a newly created access key; the secret is only ever
// returned by CreateAccessKey.
type AccessKey struct {
	ID     string
	Secret string
}

func (m *Manager) iamClient() (iamAPI, error) {
	if m.iam == nil {
		return nil, errors.New("aws: iam client is nil")
	}
	return m.iam, nil
}

// managedPolicy returns the policy, or nil when it does not exist.
func (m *Manager) managedPolicy(ctx context.Context, arn string) (*iamPolicy, error) {
	out, err := m.iam.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(arn)})
	if isIAMNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("aws: get policy %s: %w", arn, err)
	}
	return &iamPolicy{ARN: aws.ToString(out.Policy.Arn), DefaultVersion: aws.ToString(out.Policy.DefaultVersionId)}, nil
}

// policyDocument returns the decoded document of a policy version.
func (m *Manager) policyDocument(ctx context.Context, arn, versionID string) (string, error) {
	out, err := m.iam.GetPolicyVersion(ctx, &iam.GetPolicyVersionInput{PolicyArn: aws.String(arn), VersionId: aws.String(versionID)})
	if err != nil {
		return "", fmt.Errorf("aws: get policy %s version %s: %w", arn, versionID, err)
	}
	// IAM returns documents URL-encoded (RFC 3986).
	doc, err := url.PathUnescape(aws.ToString(out.PolicyVersion.Document))
	if err != nil {
		return "", fmt.Errorf("aws: decode policy %s version %s: %w", arn, versionID, err)
	}
	return doc, nil
}

// equalPolicyDocuments compares two JSON policy documents semantically.
func equalPolicyDocuments(a, b string) (bool, error) {
	var va, vb any
	if err := json.Unmarshal([]byte(a), &va); err != nil {
		return false, fmt.Errorf("aws: parse policy document: %w", err)
	}
	if err := json.Unmarshal([]byte(b), &vb); err != nil {
		return false, fmt.Errorf("aws: parse policy document: %w", err)
	}
	return reflect.DeepEqual(va, vb), nil
}

// EnsureManagedPolicy creates the customer-managed policy with the given
// ARN (name taken from it) or, when it exists with a different document,
// makes document the new default version, pruning the oldest non-default
// version when IAM's five-version limit is reached. Tags only apply on
// creation. It returns PolicyCreated, PolicyUpdated, or PolicyUnchanged.
func (m *Manager) EnsureManagedPolicy(ctx context.Context, arn, document, description string, tags map[string]string) (string, error) {
	client, err := m.iamClient()
	if err != nil {
		return "", err
	}
	if !json.Valid([]byte(document)) {
		return "", errors.New("aws: policy document is not valid JSON")
	}
	existing, err := m.managedPolicy(ctx, arn)
	if err != nil {
		return "", err
	}
	if existing == nil {
		name := arn[strings.LastIndex(arn, "/")+1:]
		_, err := client.CreatePolicy(ctx, &iam.CreatePolicyInput{
			PolicyName:     aws.String(name),
			PolicyDocument: aws.String(document),
			Description:    aws.String(description),
			Tags:           iamTags(tags),
		})
		if err != nil {
			return "", fmt.Errorf("aws: create policy %s: %w", name, err)
		}
		return PolicyCreated, nil
	}
	current, err := m.policyDocument(ctx, arn, existing.DefaultVersion)
	if err != nil {
		return "", err
	}
	same, err := equalPolicyDocuments(current, document)
	if err != nil {
		return "", err
	}
	if same {
		return PolicyUnchanged, nil
	}
	if err := m.pruneOldestPolicyVersion(ctx, arn); err != nil {
		return "", err
	}
	_, err = client.CreatePolicyVersion(ctx, &iam.CreatePolicyVersionInput{
		PolicyArn:      aws.String(arn),
		PolicyDocument: aws.String(document),
		SetAsDefault:   true,
	})
	if err != nil {
		return "", fmt.Errorf("aws: update policy %s: %w", arn, err)
	}
	return PolicyUpdated, nil
}

// pruneOldestPolicyVersion frees a version slot when all five are used.
func (m *Manager) pruneOldestPolicyVersion(ctx context.Context, arn string) error {
	versions, err := m.policyVersions(ctx, arn)
	if err != nil {
		return err
	}
	if len(versions) < maxPolicyVersions {
		return nil
	}
	var nonDefault []iamtypes.PolicyVersion
	for _, v := range versions {
		if !v.IsDefaultVersion {
			nonDefault = append(nonDefault, v)
		}
	}
	if len(nonDefault) == 0 {
		return fmt.Errorf("aws: policy %s has %d versions and none is deletable", arn, len(versions))
	}
	sort.Slice(nonDefault, func(i, j int) bool {
		return aws.ToTime(nonDefault[i].CreateDate).Before(aws.ToTime(nonDefault[j].CreateDate))
	})
	return m.deletePolicyVersion(ctx, arn, aws.ToString(nonDefault[0].VersionId))
}

func (m *Manager) policyVersions(ctx context.Context, arn string) ([]iamtypes.PolicyVersion, error) {
	var versions []iamtypes.PolicyVersion
	pager := iam.NewListPolicyVersionsPaginator(m.iam, &iam.ListPolicyVersionsInput{PolicyArn: aws.String(arn)})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aws: list versions of policy %s: %w", arn, err)
		}
		versions = append(versions, page.Versions...)
	}
	return versions, nil
}

func (m *Manager) deletePolicyVersion(ctx context.Context, arn, versionID string) error {
	_, err := m.iam.DeletePolicyVersion(ctx, &iam.DeletePolicyVersionInput{PolicyArn: aws.String(arn), VersionId: aws.String(versionID)})
	if err != nil && !isIAMNotFound(err) {
		return fmt.Errorf("aws: delete policy %s version %s: %w", arn, versionID, err)
	}
	return nil
}

// Role returns the role, or nil when it does not exist.
func (m *Manager) Role(ctx context.Context, name string) (*IAMRole, error) {
	client, err := m.iamClient()
	if err != nil {
		return nil, err
	}
	out, err := client.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
	if isIAMNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("aws: get role %s: %w", name, err)
	}
	return &IAMRole{ARN: aws.ToString(out.Role.Arn), Tags: iamTagMap(out.Role.Tags)}, nil
}

// CreateRole creates a role with the given trust policy.
func (m *Manager) CreateRole(ctx context.Context, name, trustPolicy, description string, tags map[string]string) error {
	client, err := m.iamClient()
	if err != nil {
		return err
	}
	_, err = client.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(name),
		AssumeRolePolicyDocument: aws.String(trustPolicy),
		Description:              aws.String(description),
		Tags:                     iamTags(tags),
	})
	if err != nil {
		return fmt.Errorf("aws: create role %s: %w", name, err)
	}
	return nil
}

// RolePolicyARNs lists the managed policies attached to a role.
func (m *Manager) RolePolicyARNs(ctx context.Context, role string) ([]string, error) {
	client, err := m.iamClient()
	if err != nil {
		return nil, err
	}
	var arns []string
	pager := iam.NewListAttachedRolePoliciesPaginator(client, &iam.ListAttachedRolePoliciesInput{RoleName: aws.String(role)})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aws: list policies of role %s: %w", role, err)
		}
		for _, p := range page.AttachedPolicies {
			arns = append(arns, aws.ToString(p.PolicyArn))
		}
	}
	return arns, nil
}

// AttachRolePolicy attaches a managed policy to a role (idempotent).
func (m *Manager) AttachRolePolicy(ctx context.Context, role, policyARN string) error {
	client, err := m.iamClient()
	if err != nil {
		return err
	}
	if _, err := client.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{RoleName: aws.String(role), PolicyArn: aws.String(policyARN)}); err != nil {
		return fmt.Errorf("aws: attach %s to role %s: %w", policyARN, role, err)
	}
	return nil
}

// InstanceProfile returns the instance profile, or nil when it does not exist.
func (m *Manager) InstanceProfile(ctx context.Context, name string) (*IAMInstanceProfile, error) {
	client, err := m.iamClient()
	if err != nil {
		return nil, err
	}
	out, err := client.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{InstanceProfileName: aws.String(name)})
	if isIAMNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("aws: get instance profile %s: %w", name, err)
	}
	p := out.InstanceProfile
	profile := &IAMInstanceProfile{ARN: aws.ToString(p.Arn), Tags: iamTagMap(p.Tags)}
	for _, r := range p.Roles {
		profile.Roles = append(profile.Roles, aws.ToString(r.RoleName))
	}
	return profile, nil
}

// CreateInstanceProfile creates an empty instance profile.
func (m *Manager) CreateInstanceProfile(ctx context.Context, name string, tags map[string]string) error {
	client, err := m.iamClient()
	if err != nil {
		return err
	}
	if _, err := client.CreateInstanceProfile(ctx, &iam.CreateInstanceProfileInput{InstanceProfileName: aws.String(name), Tags: iamTags(tags)}); err != nil {
		return fmt.Errorf("aws: create instance profile %s: %w", name, err)
	}
	return nil
}

// AddRoleToInstanceProfile puts role into an instance profile. A profile
// holds at most one role.
func (m *Manager) AddRoleToInstanceProfile(ctx context.Context, profile, role string) error {
	client, err := m.iamClient()
	if err != nil {
		return err
	}
	if _, err := client.AddRoleToInstanceProfile(ctx, &iam.AddRoleToInstanceProfileInput{InstanceProfileName: aws.String(profile), RoleName: aws.String(role)}); err != nil {
		return fmt.Errorf("aws: add role %s to instance profile %s: %w", role, profile, err)
	}
	return nil
}

// User returns the user, or nil when it does not exist.
func (m *Manager) User(ctx context.Context, name string) (*IAMUser, error) {
	client, err := m.iamClient()
	if err != nil {
		return nil, err
	}
	out, err := client.GetUser(ctx, &iam.GetUserInput{UserName: aws.String(name)})
	if isIAMNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("aws: get user %s: %w", name, err)
	}
	u := out.User
	user := &IAMUser{ARN: aws.ToString(u.Arn), Tags: iamTagMap(u.Tags)}
	if u.PermissionsBoundary != nil {
		user.PermissionsBoundary = aws.ToString(u.PermissionsBoundary.PermissionsBoundaryArn)
	}
	return user, nil
}

// CreateUser creates a user whose permissions boundary is boundaryARN, so
// the user can never exceed that policy whatever else gets attached.
func (m *Manager) CreateUser(ctx context.Context, name, boundaryARN string, tags map[string]string) error {
	client, err := m.iamClient()
	if err != nil {
		return err
	}
	_, err = client.CreateUser(ctx, &iam.CreateUserInput{
		UserName:            aws.String(name),
		PermissionsBoundary: aws.String(boundaryARN),
		Tags:                iamTags(tags),
	})
	if err != nil {
		return fmt.Errorf("aws: create user %s: %w", name, err)
	}
	return nil
}

// SetUserPermissionsBoundary sets (or replaces) a user's boundary policy.
func (m *Manager) SetUserPermissionsBoundary(ctx context.Context, name, boundaryARN string) error {
	client, err := m.iamClient()
	if err != nil {
		return err
	}
	if _, err := client.PutUserPermissionsBoundary(ctx, &iam.PutUserPermissionsBoundaryInput{UserName: aws.String(name), PermissionsBoundary: aws.String(boundaryARN)}); err != nil {
		return fmt.Errorf("aws: set permissions boundary of user %s: %w", name, err)
	}
	return nil
}

// UserPolicyARNs lists the managed policies attached to a user.
func (m *Manager) UserPolicyARNs(ctx context.Context, name string) ([]string, error) {
	client, err := m.iamClient()
	if err != nil {
		return nil, err
	}
	var arns []string
	pager := iam.NewListAttachedUserPoliciesPaginator(client, &iam.ListAttachedUserPoliciesInput{UserName: aws.String(name)})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aws: list policies of user %s: %w", name, err)
		}
		for _, p := range page.AttachedPolicies {
			arns = append(arns, aws.ToString(p.PolicyArn))
		}
	}
	return arns, nil
}

// AttachUserPolicy attaches a managed policy to a user (idempotent).
func (m *Manager) AttachUserPolicy(ctx context.Context, name, policyARN string) error {
	client, err := m.iamClient()
	if err != nil {
		return err
	}
	if _, err := client.AttachUserPolicy(ctx, &iam.AttachUserPolicyInput{UserName: aws.String(name), PolicyArn: aws.String(policyARN)}); err != nil {
		return fmt.Errorf("aws: attach %s to user %s: %w", policyARN, name, err)
	}
	return nil
}

// AccessKeyIDs lists a user's access key IDs.
func (m *Manager) AccessKeyIDs(ctx context.Context, name string) ([]string, error) {
	client, err := m.iamClient()
	if err != nil {
		return nil, err
	}
	var ids []string
	pager := iam.NewListAccessKeysPaginator(client, &iam.ListAccessKeysInput{UserName: aws.String(name)})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aws: list access keys of user %s: %w", name, err)
		}
		for _, k := range page.AccessKeyMetadata {
			ids = append(ids, aws.ToString(k.AccessKeyId))
		}
	}
	return ids, nil
}

// CreateAccessKey creates an access key; its secret is never retrievable
// again.
func (m *Manager) CreateAccessKey(ctx context.Context, name string) (AccessKey, error) {
	client, err := m.iamClient()
	if err != nil {
		return AccessKey{}, err
	}
	out, err := client.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(name)})
	if err != nil {
		return AccessKey{}, fmt.Errorf("aws: create access key for user %s: %w", name, err)
	}
	return AccessKey{ID: aws.ToString(out.AccessKey.AccessKeyId), Secret: aws.ToString(out.AccessKey.SecretAccessKey)}, nil
}

func isIAMNotFound(err error) bool {
	return err != nil && apiErrorCode(err) == "NoSuchEntity"
}

func iamTags(tags map[string]string) []iamtypes.Tag {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]iamtypes.Tag, 0, len(keys))
	for _, k := range keys {
		out = append(out, iamtypes.Tag{Key: aws.String(k), Value: aws.String(tags[k])})
	}
	return out
}

func iamTagMap(tags []iamtypes.Tag) map[string]string {
	out := make(map[string]string, len(tags))
	for _, t := range tags {
		out[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return out
}
