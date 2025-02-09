module github.com/dnr/styx

go 1.22.1

replace github.com/DataDog/zstd => github.com/dnr/datadog-zstd-go v0.0.0-20241112014204-5b807b5888a6

require (
	github.com/DataDog/zstd v1.5.5
	github.com/avast/retry-go/v4 v4.6.0
	github.com/aws/aws-lambda-go v1.46.0
	github.com/aws/aws-sdk-go-v2 v1.27.2
	github.com/aws/aws-sdk-go-v2/config v1.27.3
	github.com/aws/aws-sdk-go-v2/service/autoscaling v1.40.11
	github.com/aws/aws-sdk-go-v2/service/s3 v1.51.0
	github.com/aws/aws-sdk-go-v2/service/ssm v1.49.5
	github.com/axiomhq/axiom-go v0.21.1
	github.com/lunixbochs/struc v0.0.0-20200707160740-784aaebc1d40
	github.com/nix-community/go-nix v0.0.0-20231219074122-93cb24a86856
	github.com/phayes/freeport v0.0.0-20220201140144-74d24b5ae9f5
	github.com/spf13/cobra v1.8.1
	github.com/stretchr/testify v1.10.0
	github.com/wneessen/go-mail v0.4.2
	go.etcd.io/bbolt v1.3.11
	go.temporal.io/api v1.43.2
	go.temporal.io/sdk v1.32.1
	golang.org/x/exp v0.0.0-20240904232852-e7e105dedf7e
	golang.org/x/sync v0.8.0
	golang.org/x/sys v0.26.0
	golang.org/x/text v0.18.0
	google.golang.org/grpc v1.66.1
	google.golang.org/protobuf v1.34.2
)

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.6.1 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.17.3 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.15.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.3.9 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.6.9 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.8.0 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.3.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.11.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.3.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.11.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.17.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.20.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.23.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.28.0 // indirect
	github.com/aws/smithy-go v1.20.2 // indirect
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/facebookgo/clock v0.0.0-20150410010913-600d898af40a // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/mock v1.6.0 // indirect
	github.com/google/go-querystring v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/go-grpc-middleware v1.4.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.22.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/klauspost/cpuid/v2 v2.0.9 // indirect
	github.com/minio/sha256-simd v1.0.0 // indirect
	github.com/mr-tron/base58 v1.2.0 // indirect
	github.com/multiformats/go-multihash v0.2.1 // indirect
	github.com/multiformats/go-varint v0.0.6 // indirect
	github.com/nexus-rpc/sdk-go v0.1.1 // indirect
	github.com/pborman/uuid v1.2.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/robfig/cron v1.2.0 // indirect
	github.com/spaolacci/murmur3 v1.1.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/stretchr/objx v0.5.2 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.55.0 // indirect
	go.opentelemetry.io/otel v1.30.0 // indirect
	go.opentelemetry.io/otel/metric v1.30.0 // indirect
	go.opentelemetry.io/otel/trace v1.30.0 // indirect
	golang.org/x/crypto v0.27.0 // indirect
	golang.org/x/net v0.29.0 // indirect
	golang.org/x/time v0.3.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20240903143218-8af14fe29dc1 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240903143218-8af14fe29dc1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	lukechampine.com/blake3 v1.1.6 // indirect
)
