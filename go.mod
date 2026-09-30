module git.tbd/etcd-infra

// https://go.dev/dl/
go 1.26.1

// Go standard library adjuncts and core runtime dependencies.
require (
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/time v0.16.0
	google.golang.org/grpc v1.84.0
	google.golang.org/protobuf v1.36.12
)

// Cloud provider SDKs.
require (
	github.com/aws/aws-sdk-go-v2 v1.47.1
	github.com/aws/aws-sdk-go-v2/config v1.33.6
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.337.0
	github.com/aws/aws-sdk-go-v2/service/sts v1.51.1
)

// CLI and UX helpers.
require (
	github.com/dustin/go-humanize v1.1.0
	github.com/sirupsen/logrus v1.10.2
)

// Versioning and logging.
require (
	github.com/coreos/go-semver v0.3.1
	go.uber.org/zap v1.28.0
)

// Etcd storage and clients.
require (
	go.etcd.io/bbolt v1.5.0
	go.etcd.io/etcd/api/v3 v3.7.2
	go.etcd.io/etcd/client/pkg/v3 v3.7.2
	go.etcd.io/etcd/client/v3 v3.7.2
)

// Testing.
require (
	github.com/bytedance/mockey v1.4.6
	github.com/stretchr/testify v1.12.1
)

// Indirect dependencies (managed by go mod tidy).
require (
	github.com/aws/aws-sdk-go-v2/credentials v1.20.6 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.4 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.19 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.10.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.38.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.43.1 // indirect
	github.com/aws/smithy-go v1.28.2
	github.com/coreos/go-systemd/v22 v22.7.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.31.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260928230214-8a89bd6388cc // indirect
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/aws/aws-sdk-go-v2/service/autoscaling v1.78.1
	github.com/aws/aws-sdk-go-v2/service/iam v1.64.1
	github.com/aws/aws-sdk-go-v2/service/ssm v1.79.0
)

require (
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.4 // indirect
	github.com/gopherjs/gopherjs v1.21.0 // indirect
	github.com/jtolds/gls v4.20.0+incompatible // indirect
	github.com/smarty/assertions v1.16.0 // indirect
	github.com/smartystreets/goconvey v1.8.1 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/arch v0.29.0 // indirect
)
