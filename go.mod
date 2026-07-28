module git.tbd/etcd-infra

// https://go.dev/dl/
go 1.26.1

// Go standard library adjuncts and core runtime dependencies.
require (
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/time v0.15.0
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.11
)

// Cloud provider SDKs.
require (
	github.com/aws/aws-sdk-go-v2 v1.43.0
	github.com/aws/aws-sdk-go-v2/config v1.32.31
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.317.0
	github.com/aws/aws-sdk-go-v2/service/sts v1.45.0 // indirect
)

// CLI and UX helpers.
require (
	github.com/dustin/go-humanize v1.0.1
	github.com/sirupsen/logrus v1.9.4
)

// Versioning and logging.
require (
	github.com/coreos/go-semver v0.3.1
	go.uber.org/zap v1.28.0
)

// Etcd storage and clients.
require (
	go.etcd.io/bbolt v1.5.0
	go.etcd.io/etcd/api/v3 v3.7.1
	go.etcd.io/etcd/client/pkg/v3 v3.7.1
	go.etcd.io/etcd/client/v3 v3.7.1
)

// Testing.
require (
	github.com/bytedance/mockey v1.4.6
	github.com/stretchr/testify v1.11.1
)

// Indirect dependencies (managed by go mod tidy).
require (
	github.com/aws/aws-sdk-go-v2/credentials v1.19.30 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.31 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.31 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.31 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.31 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.5.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.33.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.38.0 // indirect
	github.com/aws/smithy-go v1.27.5
	github.com/coreos/go-systemd/v22 v22.7.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.29.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260727163830-6c54dddc4772 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260727163830-6c54dddc4772 // indirect
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/aws/aws-sdk-go-v2/service/autoscaling v1.70.0
	github.com/aws/aws-sdk-go-v2/service/ssm v1.73.0
)

require (
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.32 // indirect
	github.com/gopherjs/gopherjs v1.21.0 // indirect
	github.com/jtolds/gls v4.20.0+incompatible // indirect
	github.com/smarty/assertions v1.16.0 // indirect
	github.com/smartystreets/goconvey v1.8.1 // indirect
	golang.org/x/arch v0.29.0 // indirect
)
