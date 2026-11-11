module github.com/jhoblitt/rgw-go

go 1.27

require (
	github.com/aws/aws-sdk-go-v2 v1.47.6
	github.com/aws/aws-sdk-go-v2/credentials v1.20.14
	github.com/aws/aws-sdk-go-v2/service/s3 v1.114.7
	github.com/aws/smithy-go v1.28.8
	github.com/ceph/go-ceph v0.39.0
	github.com/cespare/xxhash/v2 v2.3.0
	github.com/klauspost/compress v1.20.3
	github.com/onsi/ginkgo/v2 v2.34.0
	github.com/onsi/gomega v1.44.0
	github.com/pierrec/lz4/v4 v4.1.33
	github.com/prometheus/client_golang v1.25.0
	github.com/prometheus/common v0.73.1
	github.com/spf13/cobra v1.10.2
	github.com/spf13/viper v1.21.0
	go.yaml.in/yaml/v3 v3.0.6
	golang.org/x/mod v0.42.0
	golang.org/x/sync v0.24.0
)

require (
	github.com/Masterminds/semver/v3 v3.5.0 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.26 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.9 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.9 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.9 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.24 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.10 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.9 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.9 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-task/slim-sprig/v3 v3.0.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/pprof v0.0.0-20261105003125-4bf881d7fb0b // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/maxbrunsfeld/counterfeiter/v6 v6.12.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pandatix/go-abnf v0.5.5 // indirect
	github.com/pelletier/go-toml/v2 v2.4.3 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/procfs v0.23.0 // indirect
	github.com/sagikazarmark/locafero v0.12.0 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

tool github.com/maxbrunsfeld/counterfeiter/v6

replace github.com/ceph/go-ceph => github.com/jhoblitt/go-ceph v0.0.0-20261019213543-2bd6901ea775
