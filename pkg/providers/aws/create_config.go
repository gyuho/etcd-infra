package aws

// CreateConfig contains AWS-only EC2 launch settings.
type CreateConfig struct {
	IAMInstanceProfile string
	// DataVolumeSizeGB, when positive, adds a dedicated EBS data volume with
	// DeleteOnTermination=false, so a later replacement keeps the data.
	DataVolumeSizeGB int
	// PrivateIPAddress pins the instance's private IP inside the subnet.
	// Used by standalone replacement to preserve member identity.
	PrivateIPAddress string
	// DataVolumeID attaches an existing volume (a preserved data volume from
	// a replaced instance) instead of creating a new one.
	DataVolumeID string
}
