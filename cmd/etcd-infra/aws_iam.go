package main

// "etcd-infra aws iam" bootstraps the least-privilege IAM identity that
// every other etcd-infra AWS command should run as, so day-to-day work
// never needs administrator credentials:
//
//	etcd-infra aws iam up --dry-run=false --access-key   # run once as an admin
//	etcd-infra aws iam status
//	etcd-infra aws iam down [--role]
//
// "up" is idempotent and converges the account to the reviewed policy
// files embedded from hack/ (rerun it after editing them):
//
//   - customer-managed policy etcd-infra-aws-e2e (hack/aws-e2e.iam-policy.json);
//   - IAM user etcd-infra-aws-e2e (--user) with that policy attached AND set
//     as its permissions boundary, so the user can never exceed the policy
//     even if a broader policy is attached to it later;
//   - with --role (default): role and instance profile etcd-infra-ssm, which
//     test instances run as, with AmazonSSMManagedInstanceCore,
//     AmazonS3ReadOnlyAccess, and policy etcd-infra-ssm-exec
//     (hack/aws-ssm-role-exec.iam-policy.json). The name is fixed: the
//     user policy only allows iam:PassRole on role/etcd-infra-ssm;
//   - with --access-key: a new access key, printed once.
//
// Ownership: everything "up" creates is tagged
// etcd-infra.managed-by=etcd-infra-aws-iam, and "down" deletes only tagged
// resources. Resources that already existed without the tag (for example
// created by hand from the README) are adopted by "up" (policies attached,
// documents updated) but never deleted. A shared policy still attached to
// other principals is kept.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"

	"git.tbd/etcd-infra/hack"
	awsprovider "git.tbd/etcd-infra/pkg/providers/aws"
)

const (
	defaultAWSIAMUser        = "etcd-infra-aws-e2e"
	awsIAMUserPolicyName     = "etcd-infra-aws-e2e"
	awsIAMRoleName           = "etcd-infra-ssm"
	awsIAMRoleExecPolicyName = "etcd-infra-ssm-exec"
	awsIAMManagedByTag       = "etcd-infra.managed-by"
	awsIAMManagedByValue     = "etcd-infra-aws-iam"
	// IAM caps a user at two access keys.
	awsIAMMaxAccessKeys = 2
	// IAM's default region for the global endpoint when none is configured.
	awsIAMDefaultRegion = "us-east-1"
)

// awsIAMRoleTrustPolicy lets EC2 instances assume the role.
const awsIAMRoleTrustPolicy = `{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Service": "ec2.amazonaws.com"},
    "Action": "sts:AssumeRole"
  }]
}`

// awsIAMRoleAWSPolicies are the AWS-managed policies the instance role needs.
var awsIAMRoleAWSPolicies = []string{"AmazonSSMManagedInstanceCore", "AmazonS3ReadOnlyAccess"}

var awsIAMUserNamePattern = regexp.MustCompile(`^[A-Za-z0-9+=,.@_-]{1,64}$`)

// awsIAMClient is the subset of *awsprovider.Manager the iam commands use.
type awsIAMClient interface {
	ManagedPolicy(ctx context.Context, arn string) (*awsprovider.IAMPolicy, error)
	PolicyDocumentCurrent(ctx context.Context, policy *awsprovider.IAMPolicy, document string) (bool, error)
	EnsureManagedPolicy(ctx context.Context, arn, document, description string, tags map[string]string) (string, error)
	DeleteManagedPolicy(ctx context.Context, arn string) error

	Role(ctx context.Context, name string) (*awsprovider.IAMRole, error)
	CreateRole(ctx context.Context, name, trustPolicy, description string, tags map[string]string) error
	RolePolicyARNs(ctx context.Context, role string) ([]string, error)
	AttachRolePolicy(ctx context.Context, role, policyARN string) error
	RoleInstanceProfiles(ctx context.Context, role string) ([]string, error)
	DeleteRole(ctx context.Context, name string) error

	InstanceProfile(ctx context.Context, name string) (*awsprovider.IAMInstanceProfile, error)
	CreateInstanceProfile(ctx context.Context, name string, tags map[string]string) error
	AddRoleToInstanceProfile(ctx context.Context, profile, role string) error
	DeleteInstanceProfile(ctx context.Context, name string) error

	User(ctx context.Context, name string) (*awsprovider.IAMUser, error)
	CreateUser(ctx context.Context, name, boundaryARN string, tags map[string]string) error
	SetUserPermissionsBoundary(ctx context.Context, name, boundaryARN string) error
	UserPolicyARNs(ctx context.Context, name string) ([]string, error)
	AttachUserPolicy(ctx context.Context, name, policyARN string) error
	AccessKeyIDs(ctx context.Context, name string) ([]string, error)
	CreateAccessKey(ctx context.Context, name string) (awsprovider.AccessKey, error)
	DeleteUser(ctx context.Context, name string) error
}

var _ awsIAMClient = (*awsprovider.Manager)(nil)

type awsIAMOptions struct {
	Region    string
	User      string
	Role      bool
	AccessKey bool
	DryRun    bool
}

// awsIAMTarget is the resolved set of IAM names in one account.
type awsIAMTarget struct {
	Account            string
	Partition          string
	User               string
	UserPolicyARN      string
	RoleExecPolicyARN  string
	RoleAWSPolicyARNs  []string
	UserPolicyDocument string
	RoleExecPolicyDoc  string
	IncludeRole        bool
	CreateAccessKey    bool
	CallerARN          string
	// ManagedTags mark resources created by "aws iam up".
	ManagedTags map[string]string
}

func newAWSIAMTarget(id awsprovider.CallerIdentity, opts awsIAMOptions) awsIAMTarget {
	t := awsIAMTarget{
		Account:            id.Account,
		Partition:          id.Partition,
		CallerARN:          id.ARN,
		User:               opts.User,
		UserPolicyARN:      awsIAMPolicyARN(id.Partition, id.Account, awsIAMUserPolicyName),
		RoleExecPolicyARN:  awsIAMPolicyARN(id.Partition, id.Account, awsIAMRoleExecPolicyName),
		UserPolicyDocument: hack.AWSE2EUserPolicy,
		RoleExecPolicyDoc:  hack.AWSSSMRoleExecPolicy,
		IncludeRole:        opts.Role,
		CreateAccessKey:    opts.AccessKey,
		ManagedTags:        map[string]string{awsIAMManagedByTag: awsIAMManagedByValue},
	}
	for _, name := range awsIAMRoleAWSPolicies {
		t.RoleAWSPolicyARNs = append(t.RoleAWSPolicyARNs, fmt.Sprintf("arn:%s:iam::aws:policy/%s", id.Partition, name))
	}
	return t
}

func awsIAMPolicyARN(partition, account, name string) string {
	return fmt.Sprintf("arn:%s:iam::%s:policy/%s", partition, account, name)
}

func awsIAMManaged(tags map[string]string) bool {
	return tags[awsIAMManagedByTag] == awsIAMManagedByValue
}

func runAWSIAM(ctx context.Context, args []string) error {
	const usage = "usage: etcd-infra aws iam <up|status|down>"
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "up":
		return runAWSIAMUp(ctx, args[1:])
	case "status":
		return runAWSIAMStatus(ctx, args[1:])
	case "down":
		return runAWSIAMDown(ctx, args[1:])
	default:
		return fmt.Errorf("unknown aws iam command %q; %s", args[0], usage)
	}
}

func awsIAMFlags(name string, opts *awsIAMOptions) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.StringVar(&opts.Region, "region", os.Getenv("AWS_REGION"), "AWS region for the API endpoint (IAM is global; defaults to AWS configuration, else "+awsIAMDefaultRegion+")")
	flags.StringVar(&opts.User, "user", defaultAWSIAMUser, "least-privilege IAM user name")
	return flags
}

func validateAWSIAMOptions(opts awsIAMOptions) error {
	if !awsIAMUserNamePattern.MatchString(opts.User) {
		return fmt.Errorf("invalid --user %q: IAM user names are 1-64 characters of letters, digits, and +=,.@_-", opts.User)
	}
	return nil
}

// newAWSIAMSession loads credentials and resolves the caller.
func newAWSIAMSession(ctx context.Context, region string) (*awsprovider.Manager, awsprovider.CallerIdentity, error) {
	cfg, err := awsprovider.LoadDefaultConfig(ctx, region)
	if err != nil {
		return nil, awsprovider.CallerIdentity{}, fmt.Errorf("load AWS config: %w", err)
	}
	if cfg.Region == "" {
		cfg.Region = awsIAMDefaultRegion
	}
	manager := awsprovider.New(cfg)
	id, err := manager.CallerIdentity(ctx)
	if err != nil {
		return nil, awsprovider.CallerIdentity{}, fmt.Errorf("identify AWS account: %w", err)
	}
	if err := validateAWSIAMPartition(id.Partition); err != nil {
		return nil, awsprovider.CallerIdentity{}, err
	}
	return manager, id, nil
}

// validateAWSIAMPartition refuses partitions the embedded policies do not
// cover: their resource ARNs are written for "arn:aws:".
func validateAWSIAMPartition(partition string) error {
	if partition != "aws" {
		return fmt.Errorf("partition %q is unsupported: the policies in hack/ use arn:aws: resource ARNs", partition)
	}
	return nil
}

func runAWSIAMUp(ctx context.Context, args []string) error {
	opts := awsIAMOptions{}
	flags := awsIAMFlags("aws iam up", &opts)
	flags.BoolVar(&opts.Role, "role", true, "also set up role and instance profile "+awsIAMRoleName+" that test instances run as")
	flags.BoolVar(&opts.AccessKey, "access-key", false, "create an access key for the user and print it once (IAM allows two per user)")
	flags.BoolVar(&opts.DryRun, "dry-run", true, "show the plan without calling AWS")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if err := validateAWSIAMOptions(opts); err != nil {
		return err
	}
	if opts.DryRun {
		printAWSIAMPlan(os.Stdout, opts)
		return nil
	}
	manager, id, err := newAWSIAMSession(ctx, opts.Region)
	if err != nil {
		return err
	}
	return awsIAMUp(ctx, manager, newAWSIAMTarget(id, opts), os.Stdout, os.Stderr)
}

func runAWSIAMStatus(ctx context.Context, args []string) error {
	opts := awsIAMOptions{}
	flags := awsIAMFlags("aws iam status", &opts)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if err := validateAWSIAMOptions(opts); err != nil {
		return err
	}
	manager, id, err := newAWSIAMSession(ctx, opts.Region)
	if err != nil {
		return err
	}
	opts.Role = true
	return awsIAMStatus(ctx, manager, newAWSIAMTarget(id, opts), os.Stdout)
}

func runAWSIAMDown(ctx context.Context, args []string) error {
	opts := awsIAMOptions{}
	flags := awsIAMFlags("aws iam down", &opts)
	flags.BoolVar(&opts.Role, "role", false, "also delete role, instance profile, and policy of "+awsIAMRoleName+" (running etcd-infra instances lose SSM access)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if err := validateAWSIAMOptions(opts); err != nil {
		return err
	}
	manager, id, err := newAWSIAMSession(ctx, opts.Region)
	if err != nil {
		return err
	}
	return awsIAMDown(ctx, manager, newAWSIAMTarget(id, opts), os.Stdout, os.Stderr)
}

// awsIAMUp converges the account; every step is idempotent, so a rerun
// finishes an interrupted one.
func awsIAMUp(ctx context.Context, c awsIAMClient, t awsIAMTarget, out, log io.Writer) error {
	// Bounding the caller's own identity would lock it out of IAM. Compare
	// full ARNs: user ARNs may carry a path (user/ops/name).
	if user, err := c.User(ctx, t.User); err != nil {
		return err
	} else if user != nil && user.ARN == t.CallerARN {
		return fmt.Errorf("the current credentials are user %s itself; run 'aws iam up' with administrator credentials as another identity", t.User)
	}
	fmt.Fprintf(out, "account_id=%s\n", t.Account)
	if t.IncludeRole {
		if err := awsIAMUpRole(ctx, c, t, out, log); err != nil {
			return err
		}
	}
	if err := awsIAMUpUser(ctx, c, t, out, log); err != nil {
		return err
	}
	if t.CreateAccessKey {
		keys, err := c.AccessKeyIDs(ctx, t.User)
		if err != nil {
			return err
		}
		if len(keys) >= awsIAMMaxAccessKeys {
			return fmt.Errorf("user %s already has %d access keys (IAM maximum); delete one (aws iam delete-access-key --user-name %s --access-key-id <id>) and rerun", t.User, len(keys), t.User)
		}
		key, err := c.CreateAccessKey(ctx, t.User)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "access_key_id=%s\n", key.ID)
		fmt.Fprintf(out, "secret_access_key=%s\n", key.Secret)
		fmt.Fprintf(log, "the secret access key is shown only now; store it, e.g.:\n  aws configure set aws_access_key_id %s --profile %s\n  aws configure set aws_secret_access_key <secret> --profile %s\n", key.ID, t.User, t.User)
	}
	fmt.Fprintf(log, "IAM changes take a few seconds to propagate. Then run etcd-infra as the user, e.g.:\n  export AWS_PROFILE=%s", t.User)
	if t.IncludeRole {
		fmt.Fprintf(log, " ETCD_INFRA_AWS_INSTANCE_PROFILE=%s", awsIAMRoleName)
	}
	fmt.Fprintln(log)
	return nil
}

func awsIAMUpRole(ctx context.Context, c awsIAMClient, t awsIAMTarget, out, log io.Writer) error {
	state, err := c.EnsureManagedPolicy(ctx, t.RoleExecPolicyARN, t.RoleExecPolicyDoc,
		"etcd-infra: tag-scoped execution permissions of the "+awsIAMRoleName+" instance role", t.ManagedTags)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "role_exec_policy_arn=%s\nrole_exec_policy=%s\n", t.RoleExecPolicyARN, state)

	role, err := c.Role(ctx, awsIAMRoleName)
	if err != nil {
		return err
	}
	roleState := "existing"
	if role == nil {
		if err := c.CreateRole(ctx, awsIAMRoleName, awsIAMRoleTrustPolicy, "etcd-infra: role of etcd-infra test instances (SSM, S3 results)", t.ManagedTags); err != nil {
			return err
		}
		roleState = "created"
	} else if !awsIAMManaged(role.Tags) {
		fmt.Fprintf(log, "note: role %s predates 'aws iam' (no %s tag); adopting it, 'aws iam down' will not delete it\n", awsIAMRoleName, awsIAMManagedByTag)
	}
	attached, err := c.RolePolicyARNs(ctx, awsIAMRoleName)
	if err != nil {
		return err
	}
	for _, arn := range append(slices.Clone(t.RoleAWSPolicyARNs), t.RoleExecPolicyARN) {
		if slices.Contains(attached, arn) {
			continue
		}
		if err := c.AttachRolePolicy(ctx, awsIAMRoleName, arn); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "role=%s\nrole_state=%s\n", awsIAMRoleName, roleState)

	profile, err := c.InstanceProfile(ctx, awsIAMRoleName)
	if err != nil {
		return err
	}
	profileState := "existing"
	if profile == nil {
		if err := c.CreateInstanceProfile(ctx, awsIAMRoleName, t.ManagedTags); err != nil {
			return err
		}
		profileState = "created"
		profile = &awsprovider.IAMInstanceProfile{}
	}
	switch {
	case slices.Contains(profile.Roles, awsIAMRoleName):
	case len(profile.Roles) == 0:
		if err := c.AddRoleToInstanceProfile(ctx, awsIAMRoleName, awsIAMRoleName); err != nil {
			return err
		}
	default:
		return fmt.Errorf("instance profile %s holds role %s, not %s; fix it by hand", awsIAMRoleName, strings.Join(profile.Roles, ","), awsIAMRoleName)
	}
	fmt.Fprintf(out, "instance_profile=%s\ninstance_profile_state=%s\n", awsIAMRoleName, profileState)
	return nil
}

func awsIAMUpUser(ctx context.Context, c awsIAMClient, t awsIAMTarget, out, log io.Writer) error {
	state, err := c.EnsureManagedPolicy(ctx, t.UserPolicyARN, t.UserPolicyDocument,
		"etcd-infra: least-privilege permissions (and boundary) of the etcd-infra IAM user", t.ManagedTags)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "user_policy_arn=%s\nuser_policy=%s\n", t.UserPolicyARN, state)

	user, err := c.User(ctx, t.User)
	if err != nil {
		return err
	}
	userState := "existing"
	if user == nil {
		// The boundary is set at creation so the user never exists unbounded.
		if err := c.CreateUser(ctx, t.User, t.UserPolicyARN, t.ManagedTags); err != nil {
			return err
		}
		userState = "created"
	} else {
		// An adopted user keeps a boundary it already had: replacing it
		// could widen the user's permissions.
		managed := awsIAMManaged(user.Tags)
		if !managed {
			fmt.Fprintf(log, "note: user %s predates 'aws iam' (no %s tag); adopting it, 'aws iam down' will not delete it\n", t.User, awsIAMManagedByTag)
		}
		switch user.PermissionsBoundary {
		case t.UserPolicyARN:
		case "":
			if err := c.SetUserPermissionsBoundary(ctx, t.User, t.UserPolicyARN); err != nil {
				return err
			}
		default:
			if !managed {
				return fmt.Errorf("user %s already has permissions boundary %s; refusing to replace it (it may be stricter). Use another --user, or remove it by hand", t.User, user.PermissionsBoundary)
			}
			if err := c.SetUserPermissionsBoundary(ctx, t.User, t.UserPolicyARN); err != nil {
				return err
			}
		}
	}
	attached, err := c.UserPolicyARNs(ctx, t.User)
	if err != nil {
		return err
	}
	if !slices.Contains(attached, t.UserPolicyARN) {
		if err := c.AttachUserPolicy(ctx, t.User, t.UserPolicyARN); err != nil {
			return err
		}
	}
	if extra := slices.DeleteFunc(attached, func(arn string) bool { return arn == t.UserPolicyARN }); len(extra) > 0 {
		fmt.Fprintf(log, "note: user %s also has %s attached; the permissions boundary still caps it at %s\n", t.User, strings.Join(extra, ", "), awsIAMUserPolicyName)
	}
	fmt.Fprintf(out, "user=%s\nuser_state=%s\npermissions_boundary=%s\n", t.User, userState, t.UserPolicyARN)
	return nil
}

// awsIAMStatus reports each resource; "ready=true" means the user and
// role match what "up" would create.
func awsIAMStatus(ctx context.Context, c awsIAMClient, t awsIAMTarget, out io.Writer) error {
	ready := true
	fmt.Fprintf(out, "account_id=%s\n", t.Account)

	policyStatus := func(key, arn, document string) error {
		policy, err := c.ManagedPolicy(ctx, arn)
		if err != nil {
			return err
		}
		if policy == nil {
			ready = false
			fmt.Fprintf(out, "%s=absent\n", key)
			return nil
		}
		current, err := c.PolicyDocumentCurrent(ctx, policy, document)
		if err != nil {
			return err
		}
		ready = ready && current
		fmt.Fprintf(out, "%s=present managed=%t current=%t attachments=%d boundary_uses=%d arn=%s\n",
			key, awsIAMManaged(policy.Tags), current, policy.Attachments, policy.BoundaryUses, arn)
		return nil
	}

	if err := policyStatus("user_policy", t.UserPolicyARN, t.UserPolicyDocument); err != nil {
		return err
	}
	user, err := c.User(ctx, t.User)
	if err != nil {
		return err
	}
	if user == nil {
		ready = false
		fmt.Fprintf(out, "user=absent name=%s\n", t.User)
	} else {
		attached, err := c.UserPolicyARNs(ctx, t.User)
		if err != nil {
			return err
		}
		keys, err := c.AccessKeyIDs(ctx, t.User)
		if err != nil {
			return err
		}
		policyAttached := slices.Contains(attached, t.UserPolicyARN)
		bounded := user.PermissionsBoundary == t.UserPolicyARN
		ready = ready && policyAttached && bounded
		fmt.Fprintf(out, "user=present name=%s managed=%t policy_attached=%t boundary=%t access_keys=%d arn=%s\n",
			t.User, awsIAMManaged(user.Tags), policyAttached, bounded, len(keys), user.ARN)
	}

	if err := policyStatus("role_exec_policy", t.RoleExecPolicyARN, t.RoleExecPolicyDoc); err != nil {
		return err
	}
	role, err := c.Role(ctx, awsIAMRoleName)
	if err != nil {
		return err
	}
	if role == nil {
		ready = false
		fmt.Fprintf(out, "role=absent name=%s\n", awsIAMRoleName)
	} else {
		attached, err := c.RolePolicyARNs(ctx, awsIAMRoleName)
		if err != nil {
			return err
		}
		var missing []string
		for _, arn := range append(slices.Clone(t.RoleAWSPolicyARNs), t.RoleExecPolicyARN) {
			if !slices.Contains(attached, arn) {
				missing = append(missing, arn)
			}
		}
		ready = ready && len(missing) == 0
		fmt.Fprintf(out, "role=present name=%s managed=%t missing_policies=%s\n", awsIAMRoleName, awsIAMManaged(role.Tags), strings.Join(missing, ","))
	}
	profile, err := c.InstanceProfile(ctx, awsIAMRoleName)
	if err != nil {
		return err
	}
	if profile == nil {
		ready = false
		fmt.Fprintf(out, "instance_profile=absent name=%s\n", awsIAMRoleName)
	} else {
		hasRole := slices.Equal(profile.Roles, []string{awsIAMRoleName})
		ready = ready && hasRole
		fmt.Fprintf(out, "instance_profile=present name=%s managed=%t roles=%s\n", awsIAMRoleName, awsIAMManaged(profile.Tags), strings.Join(profile.Roles, ","))
	}
	fmt.Fprintf(out, "ready=%t\n", ready)
	return nil
}

// awsIAMDown deletes the user (and with IncludeRole the role side), but
// only resources carrying the managed-by tag. Policies still attached to
// other principals are kept.
func awsIAMDown(ctx context.Context, c awsIAMClient, t awsIAMTarget, out, log io.Writer) error {
	fmt.Fprintf(out, "account_id=%s\n", t.Account)
	user, err := c.User(ctx, t.User)
	if err != nil {
		return err
	}
	switch {
	case user == nil:
		fmt.Fprintf(out, "user=absent\n")
	case !awsIAMManaged(user.Tags):
		fmt.Fprintf(log, "user %s has no %s tag (not created by 'aws iam up'); leaving it\n", t.User, awsIAMManagedByTag)
		fmt.Fprintf(out, "user=kept\n")
	default:
		fmt.Fprintf(log, "deleting user %s and its access keys\n", t.User)
		if err := c.DeleteUser(ctx, t.User); err != nil {
			return err
		}
		fmt.Fprintf(out, "user=deleted\n")
	}
	if err := awsIAMDownPolicy(ctx, c, "user_policy", t.UserPolicyARN, out, log); err != nil {
		return err
	}
	if !t.IncludeRole {
		fmt.Fprintf(log, "kept role and instance profile %s (pass --role to delete them)\n", awsIAMRoleName)
		return nil
	}

	profile, err := c.InstanceProfile(ctx, awsIAMRoleName)
	if err != nil {
		return err
	}
	switch {
	case profile == nil:
		fmt.Fprintf(out, "instance_profile=absent\n")
	case !awsIAMManaged(profile.Tags):
		fmt.Fprintf(log, "instance profile %s has no %s tag; leaving it\n", awsIAMRoleName, awsIAMManagedByTag)
		fmt.Fprintf(out, "instance_profile=kept\n")
	default:
		if err := c.DeleteInstanceProfile(ctx, awsIAMRoleName); err != nil {
			return err
		}
		fmt.Fprintf(out, "instance_profile=deleted\n")
	}
	role, err := c.Role(ctx, awsIAMRoleName)
	if err != nil {
		return err
	}
	switch {
	case role == nil:
		fmt.Fprintf(out, "role=absent\n")
	case !awsIAMManaged(role.Tags):
		fmt.Fprintf(log, "role %s has no %s tag; leaving it\n", awsIAMRoleName, awsIAMManagedByTag)
		fmt.Fprintf(out, "role=kept\n")
	default:
		// Deleting the role would empty every profile holding it; only the
		// profile deleted above is ours to change.
		profiles, err := c.RoleInstanceProfiles(ctx, awsIAMRoleName)
		if err != nil {
			return err
		}
		if len(profiles) > 0 {
			fmt.Fprintf(log, "role %s is still in instance profile(s) %s not created by 'aws iam up'; leaving it\n", awsIAMRoleName, strings.Join(profiles, ", "))
			fmt.Fprintf(out, "role=kept\n")
			break
		}
		if err := c.DeleteRole(ctx, awsIAMRoleName); err != nil {
			return err
		}
		fmt.Fprintf(out, "role=deleted\n")
	}
	return awsIAMDownPolicy(ctx, c, "role_exec_policy", t.RoleExecPolicyARN, out, log)
}

func awsIAMDownPolicy(ctx context.Context, c awsIAMClient, key, arn string, out, log io.Writer) error {
	policy, err := c.ManagedPolicy(ctx, arn)
	if err != nil {
		return err
	}
	switch {
	case policy == nil:
		fmt.Fprintf(out, "%s=absent\n", key)
		return nil
	case !awsIAMManaged(policy.Tags):
		fmt.Fprintf(log, "policy %s has no %s tag; leaving it\n", arn, awsIAMManagedByTag)
		fmt.Fprintf(out, "%s=kept\n", key)
		return nil
	}
	err = c.DeleteManagedPolicy(ctx, arn)
	if errors.Is(err, awsprovider.ErrPolicyInUse) {
		fmt.Fprintf(log, "keeping %s: %v\n", arn, err)
		fmt.Fprintf(out, "%s=kept\n", key)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s=deleted\n", key)
	return nil
}

func printAWSIAMPlan(out io.Writer, opts awsIAMOptions) {
	fmt.Fprintf(out, "plan: IAM setup in the account of the current (administrator) credentials\n")
	if opts.Role {
		fmt.Fprintf(out, "  policy %s (hack/aws-ssm-role-exec.iam-policy.json): create, or update to the file's document\n", awsIAMRoleExecPolicyName)
		fmt.Fprintf(out, "  role + instance profile %s: EC2 trust; attach %s, %s\n", awsIAMRoleName, strings.Join(awsIAMRoleAWSPolicies, ", "), awsIAMRoleExecPolicyName)
	}
	fmt.Fprintf(out, "  policy %s (hack/aws-e2e.iam-policy.json): create, or update to the file's document\n", awsIAMUserPolicyName)
	fmt.Fprintf(out, "  user %s: attach %s and set it as permissions boundary\n", opts.User, awsIAMUserPolicyName)
	if opts.AccessKey {
		fmt.Fprintf(out, "  access key for %s: create and print once\n", opts.User)
	}
	fmt.Fprintf(out, "  created resources are tagged %s=%s; 'aws iam down' deletes only those\n", awsIAMManagedByTag, awsIAMManagedByValue)
	fmt.Fprintln(out, "rerun with --dry-run=false to apply")
}
