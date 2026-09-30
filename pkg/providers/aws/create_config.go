package aws

// CreateConfig contains AWS-only EC2 launch settings.
type CreateConfig struct {
	IAMInstanceProfile string
	// DataVolumeSizeGB, when positive, adds a dedicated EBS data volume. By
	// default it has DeleteOnTermination=false, so a later replacement keeps
	// the data and teardown deletes it by recorded ID.
	DataVolumeSizeGB int
	// DataVolumeDeleteOnTermination makes EC2 delete the data volume with the
	// instance, for ephemeral hosts whose data must never outlive them.
	DataVolumeDeleteOnTermination bool
	// PrivateIPAddress pins the instance's private IP inside the subnet.
	// Used by standalone replacement to preserve member identity.
	PrivateIPAddress string
	// DataVolumeID attaches an existing volume (a preserved data volume from
	// a replaced instance) instead of creating a new one.
	DataVolumeID string
}
