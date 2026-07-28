package compute

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewOp(t *testing.T) {
	t.Parallel()

	t.Run("default values", func(t *testing.T) {
		t.Parallel()
		op := NewOp()
		assert.Equal(t, 22, op.SSHPort, "SSHPort should default to 22")
		assert.Empty(t, op.ID)
		assert.Empty(t, op.Name)
		assert.Empty(t, op.Region)
	})

	t.Run("with single option", func(t *testing.T) {
		t.Parallel()
		op := NewOp(WithName("test-instance"))
		assert.Equal(t, "test-instance", op.Name)
		assert.Equal(t, 22, op.SSHPort)
	})

	t.Run("with multiple options", func(t *testing.T) {
		t.Parallel()
		op := NewOp(
			WithID("i-12345"),
			WithName("test-instance"),
			WithRegion("us-east-1"),
			WithSize("t3.micro"),
		)
		assert.Equal(t, "i-12345", op.ID)
		assert.Equal(t, "test-instance", op.Name)
		assert.Equal(t, "us-east-1", op.Region)
		assert.Equal(t, "t3.micro", op.Size)
	})
}

func TestWithID(t *testing.T) {
	t.Parallel()
	op := NewOp(WithID("test-id-123"))
	assert.Equal(t, "test-id-123", op.ID)
}

func TestWithName(t *testing.T) {
	t.Parallel()
	op := NewOp(WithName("my-instance"))
	assert.Equal(t, "my-instance", op.Name)
}

func TestWithRegion(t *testing.T) {
	t.Parallel()
	op := NewOp(WithRegion("eu-west-1"))
	assert.Equal(t, "eu-west-1", op.Region)
}

func TestWithDatacenter(t *testing.T) {
	t.Parallel()
	op := NewOp(WithDatacenter("dc1"))
	assert.Equal(t, "dc1", op.Datacenter)
}

func TestWithSize(t *testing.T) {
	t.Parallel()
	op := NewOp(WithSize("cx21"))
	assert.Equal(t, "cx21", op.Size)
}

func TestWithImage(t *testing.T) {
	t.Parallel()
	op := NewOp(WithImage("ubuntu-24-04"))
	assert.Equal(t, "ubuntu-24-04", op.Image)
}

func TestWithSSHUser(t *testing.T) {
	t.Parallel()
	op := NewOp(WithSSHUser("root"))
	assert.Equal(t, "root", op.SSHUser)
}

func TestWithSSHPort(t *testing.T) {
	t.Parallel()
	op := NewOp(WithSSHPort(2222))
	assert.Equal(t, 2222, op.SSHPort)
}

func TestWithSSHPrivateKeyPath(t *testing.T) {
	t.Parallel()
	op := NewOp(WithSSHPrivateKeyPath("/path/to/key"))
	assert.Equal(t, "/path/to/key", op.SSHPrivateKeyPath)
}

func TestWithSSHKeys(t *testing.T) {
	t.Parallel()

	t.Run("clone keys", func(t *testing.T) {
		t.Parallel()
		original := []string{"key1", "key2", "key3"}
		op := NewOp(WithSSHKeys(original))

		require.Equal(t, original, op.SSHKeys)

		// Verify it's a clone, not the same slice
		original[0] = "modified" //nolint:goconst // Contextual string usage
		assert.Equal(t, "key1", op.SSHKeys[0], "should be cloned, not shared")
	})

	t.Run("nil keys", func(t *testing.T) {
		t.Parallel()
		op := NewOp(WithSSHKeys(nil))
		assert.Nil(t, op.SSHKeys)
	})

	t.Run("empty keys", func(t *testing.T) {
		t.Parallel()
		op := NewOp(WithSSHKeys([]string{}))
		assert.Nil(t, op.SSHKeys)
	})
}

func TestWithTags(t *testing.T) {
	t.Parallel()

	t.Run("clone tags", func(t *testing.T) {
		t.Parallel()
		original := map[string]string{
			"env":  "prod",
			"team": "platform",
		}
		op := NewOp(WithTags(original))

		require.Equal(t, original, op.Tags)

		// Verify it's a clone
		original["env"] = "dev"
		assert.Equal(t, "prod", op.Tags["env"], "should be cloned, not shared")
	})

	t.Run("nil tags", func(t *testing.T) {
		t.Parallel()
		op := NewOp(WithTags(nil))
		assert.Nil(t, op.Tags)
	})

	t.Run("empty tags", func(t *testing.T) {
		t.Parallel()
		op := NewOp(WithTags(map[string]string{}))
		assert.Nil(t, op.Tags)
	})
}

func TestWithTagList(t *testing.T) {
	t.Parallel()

	t.Run("clone tag list", func(t *testing.T) {
		t.Parallel()
		original := []string{"production", "k8s"}
		op := NewOp(WithTagList(original))

		require.Equal(t, original, op.TagList)

		// Verify it's a clone
		original[0] = "staging"
		assert.Equal(t, "production", op.TagList[0])
	})

	t.Run("nil tag list", func(t *testing.T) {
		t.Parallel()
		op := NewOp(WithTagList(nil))
		assert.Nil(t, op.TagList)
	})
}

func TestWithUserData(t *testing.T) {
	t.Parallel()
	script := "#!/bin/bash\necho hello"
	op := NewOp(WithUserData(script))
	assert.Equal(t, script, op.UserData)
}

func TestWithMounts(t *testing.T) {
	t.Parallel()

	t.Run("clone mounts", func(t *testing.T) {
		t.Parallel()
		original := []Mount{
			{HostPath: "/host/path", GuestPath: "/guest/path", Writable: true},
			{HostPath: "/another", GuestPath: "/mnt", Writable: false},
		}
		op := NewOp(WithMounts(original))

		require.Equal(t, original, op.Mounts)

		// Verify it's a clone
		original[0].HostPath = "/modified" //nolint:goconst // Contextual string usage
		assert.Equal(t, "/host/path", op.Mounts[0].HostPath)
	})

	t.Run("nil mounts", func(t *testing.T) {
		t.Parallel()
		op := NewOp(WithMounts(nil))
		assert.Nil(t, op.Mounts)
	})
}

func TestWithProvisionScripts(t *testing.T) {
	t.Parallel()

	t.Run("clone scripts", func(t *testing.T) {
		t.Parallel()
		original := []ProvisionScript{
			{Mode: "system", Script: "apt-get update"},
			{Mode: "user", Script: "echo done"},
		}
		op := NewOp(WithProvisionScripts(original))

		require.Equal(t, original, op.ProvisionScripts)

		// Verify it's a clone
		original[0].Script = "modified"
		assert.Equal(t, "apt-get update", op.ProvisionScripts[0].Script)
	})

	t.Run("nil scripts", func(t *testing.T) {
		t.Parallel()
		op := NewOp(WithProvisionScripts(nil))
		assert.Nil(t, op.ProvisionScripts)
	})
}

func TestWithProvisionScript(t *testing.T) {
	t.Parallel()

	t.Run("append single script", func(t *testing.T) {
		t.Parallel()
		op := NewOp(
			WithProvisionScript("system", "apt-get update"),
			WithProvisionScript("user", "echo hello"),
		)

		require.Len(t, op.ProvisionScripts, 2)
		assert.Equal(t, "system", op.ProvisionScripts[0].Mode)
		assert.Equal(t, "apt-get update", op.ProvisionScripts[0].Script)
		assert.Equal(t, "user", op.ProvisionScripts[1].Mode)
		assert.Equal(t, "echo hello", op.ProvisionScripts[1].Script)
	})

	t.Run("append to existing scripts", func(t *testing.T) {
		t.Parallel()
		op := NewOp(
			WithProvisionScripts([]ProvisionScript{
				{Mode: "system", Script: "first"},
			}),
			WithProvisionScript("user", "second"),
		)

		require.Len(t, op.ProvisionScripts, 2)
		assert.Equal(t, "first", op.ProvisionScripts[0].Script)
		assert.Equal(t, "second", op.ProvisionScripts[1].Script)
	})
}

func TestWithPortMappings(t *testing.T) {
	t.Parallel()

	t.Run("clone port mappings", func(t *testing.T) {
		t.Parallel()
		original := []PortMapping{
			{ContainerPort: 6443, HostPort: 6443, HostIP: "127.0.0.1", Protocol: "tcp"},
			{ContainerPort: 443, HostPort: 0, Protocol: "tcp"},
		}
		op := NewOp(WithPortMappings(original))

		require.Equal(t, original, op.PortMappings)

		original[0].HostIP = "0.0.0.0"
		assert.Equal(t, "127.0.0.1", op.PortMappings[0].HostIP)
	})

	t.Run("nil port mappings", func(t *testing.T) {
		t.Parallel()
		op := NewOp(WithPortMappings(nil))
		assert.Nil(t, op.PortMappings)
	})

	t.Run("empty port mappings", func(t *testing.T) {
		t.Parallel()
		op := NewOp(WithPortMappings([]PortMapping{}))
		assert.Nil(t, op.PortMappings)
	})
}

func TestWithVPCID(t *testing.T) {
	t.Parallel()
	op := NewOp(WithVPCID("vpc-12345"))
	assert.Equal(t, "vpc-12345", op.VPCID)
}

func TestWithSubnetID(t *testing.T) {
	t.Parallel()
	op := NewOp(WithSubnetID("subnet-67890"))
	assert.Equal(t, "subnet-67890", op.SubnetID)
}

func TestWithSecurityGroupIDs(t *testing.T) {
	t.Parallel()

	t.Run("clone security groups", func(t *testing.T) {
		t.Parallel()
		original := []string{"sg-1", "sg-2"}
		op := NewOp(WithSecurityGroupIDs(original))

		require.Equal(t, original, op.SecurityGroupIDs)

		// Verify it's a clone
		original[0] = "sg-modified"
		assert.Equal(t, "sg-1", op.SecurityGroupIDs[0])
	})

	t.Run("nil security groups", func(t *testing.T) {
		t.Parallel()
		op := NewOp(WithSecurityGroupIDs(nil))
		assert.Nil(t, op.SecurityGroupIDs)
	})
}

func TestWithCPUs(t *testing.T) {
	t.Parallel()
	op := NewOp(WithCPUs(8))
	assert.Equal(t, 8, op.CPUs)
}

func TestWithMemory(t *testing.T) {
	t.Parallel()
	op := NewOp(WithMemory("16GiB"))
	assert.Equal(t, "16GiB", op.Memory)
}

func TestWithDisk(t *testing.T) {
	t.Parallel()
	op := NewOp(WithDisk("100GB"))
	assert.Equal(t, "100GB", op.Disk)
}

func TestWithArch(t *testing.T) {
	t.Parallel()
	op := NewOp(WithArch("aarch64"))
	assert.Equal(t, "aarch64", op.Arch)
}

func TestWithNetwork(t *testing.T) {
	t.Parallel()
	op := NewOp(WithNetwork("bridge"))
	assert.Equal(t, "bridge", op.Network)
}

func TestWithProviderConfig(t *testing.T) {
	t.Parallel()
	type customConfig struct {
		Field string
	}
	cfg := &customConfig{Field: "value"}

	op := NewOp(WithProviderConfig(cfg))
	assert.Equal(t, cfg, op.ProviderConfig)
}

func TestCloneFunctions(t *testing.T) {
	t.Parallel()

	t.Run("cloneMounts", func(t *testing.T) {
		t.Parallel()
		original := []Mount{
			{HostPath: "/a", GuestPath: "/b", Writable: true},
		}
		cloned := cloneMounts(original)

		require.Equal(t, original, cloned)
		original[0].HostPath = "/modified"
		assert.Equal(t, "/a", cloned[0].HostPath)
	})

	t.Run("cloneMounts nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, cloneMounts(nil))
	})

	t.Run("cloneMounts empty", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, cloneMounts([]Mount{}))
	})

	t.Run("cloneProvisionScripts", func(t *testing.T) {
		t.Parallel()
		original := []ProvisionScript{
			{Mode: "system", Script: "test"},
		}
		cloned := cloneProvisionScripts(original)

		require.Equal(t, original, cloned)
		original[0].Script = "modified"
		assert.Equal(t, "test", cloned[0].Script)
	})

	t.Run("cloneProvisionScripts nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, cloneProvisionScripts(nil))
	})

	t.Run("cloneStringSlice", func(t *testing.T) {
		t.Parallel()
		original := []string{"a", "b", "c"}
		cloned := cloneStringSlice(original)

		require.Equal(t, original, cloned)
		original[0] = "modified"
		assert.Equal(t, "a", cloned[0])
	})

	t.Run("cloneStringSlice nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, cloneStringSlice(nil))
	})

	t.Run("cloneStringMap", func(t *testing.T) {
		t.Parallel()
		original := map[string]string{"key": "value"}
		cloned := cloneStringMap(original)

		require.Equal(t, original, cloned)
		original["key"] = "modified"
		assert.Equal(t, "value", cloned["key"])
	})

	t.Run("cloneStringMap nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, cloneStringMap(nil))
	})

	t.Run("clonePortMappings", func(t *testing.T) {
		t.Parallel()
		original := []PortMapping{
			{ContainerPort: 8080, HostPort: 8080, Protocol: "tcp"},
		}
		cloned := clonePortMappings(original)

		require.Equal(t, original, cloned)
		original[0].HostPort = 9090
		assert.Equal(t, 8080, cloned[0].HostPort)
	})

	t.Run("clonePortMappings nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, clonePortMappings(nil))
	})

	t.Run("clonePortMappings empty", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, clonePortMappings([]PortMapping{}))
	})
}

func TestComplexOpConfiguration(t *testing.T) {
	t.Parallel()

	t.Run("cloud instance configuration", func(t *testing.T) {
		t.Parallel()
		op := NewOp(
			WithID("i-abc123"),
			WithName("web-server-1"),
			WithRegion("us-west-2"),
			WithSize("m5.large"),
			WithImage("ami-12345"),
			WithSSHUser("ec2-user"),
			WithSSHKeys([]string{"my-key"}),
			WithVPCID("vpc-123"),
			WithSubnetID("subnet-456"),
			WithSecurityGroupIDs([]string{"sg-web", "sg-ssh"}),
			WithTags(map[string]string{
				"Environment": "production",
				"Service":     "web",
			}),
			WithUserData("#!/bin/bash\napt-get update"),
		)

		assert.Equal(t, "i-abc123", op.ID)
		assert.Equal(t, "web-server-1", op.Name)
		assert.Equal(t, "us-west-2", op.Region)
		assert.Equal(t, "m5.large", op.Size)
		assert.Equal(t, "ami-12345", op.Image)
		assert.Equal(t, "ec2-user", op.SSHUser)
		assert.Equal(t, 22, op.SSHPort)
		assert.Equal(t, []string{"my-key"}, op.SSHKeys)
		assert.Equal(t, "vpc-123", op.VPCID)
		assert.Equal(t, "subnet-456", op.SubnetID)
		assert.Equal(t, []string{"sg-web", "sg-ssh"}, op.SecurityGroupIDs)
		assert.Equal(t, "production", op.Tags["Environment"])
		assert.Contains(t, op.UserData, "apt-get update")
	})

	t.Run("local container configuration", func(t *testing.T) {
		t.Parallel()
		op := NewOp(
			WithName("container-dev"),
			WithCPUs(4),
			WithMemory("8GiB"),
			WithDisk("50GiB"),
			WithArch("x86_64"),
			WithNetwork("bridge"),
			WithMounts([]Mount{
				{HostPath: "/Users/dev/code", GuestPath: "/code", Writable: true},
			}),
			WithProvisionScript("system", "apt-get install -y docker"),
			WithProvisionScript("user", "docker pull nginx"),
		)

		assert.Equal(t, "container-dev", op.Name)
		assert.Equal(t, 4, op.CPUs)
		assert.Equal(t, "8GiB", op.Memory)
		assert.Equal(t, "50GiB", op.Disk)
		assert.Equal(t, "x86_64", op.Arch)
		assert.Equal(t, "bridge", op.Network)
		require.Len(t, op.Mounts, 1)
		assert.Equal(t, "/Users/dev/code", op.Mounts[0].HostPath)
		assert.True(t, op.Mounts[0].Writable)
		require.Len(t, op.ProvisionScripts, 2)
		assert.Equal(t, "system", op.ProvisionScripts[0].Mode)
		assert.Contains(t, op.ProvisionScripts[0].Script, "docker")
	})
}
