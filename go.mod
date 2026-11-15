module github.com/yinzhenyu/qrypt

go 1.27.4

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/Microsoft/go-winio v0.6.4
	github.com/OpenListTeam/115-sdk-go v0.2.6
	github.com/SheltonZhu/115driver v1.3.5
	github.com/aliyun/aliyun-oss-go-sdk v3.0.2+incompatible
	github.com/aws/aws-sdk-go-v2 v1.48.0
	github.com/aws/aws-sdk-go-v2/credentials v1.21.0
	github.com/aws/aws-sdk-go-v2/service/s3 v1.114.10
	github.com/aws/smithy-go v1.28.9
	github.com/dustinxie/ecc v0.0.0-20210511000915-959544187564
	github.com/go-resty/resty/v2 v2.17.2
	github.com/pkg/sftp v1.13.12
	github.com/rfjakob/eme v1.2.0
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.1.0
	github.com/winfsp/cgofuse v1.6.0
	go.uber.org/goleak v1.3.0
	golang.org/x/crypto v0.59.0
	golang.org/x/sync v0.25.0
	golang.org/x/sys v0.50.0
	golang.org/x/time v0.16.0
	gopkg.in/natefinch/lumberjack.v2 v2.2.1
)

require (
	github.com/aead/ecdh v0.2.0 // indirect
	github.com/andreburgaud/crypt2go v1.8.0 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.27 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.12 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.12 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.25 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.12 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/pierrec/lz4/v4 v4.1.33 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	golang.org/x/mobile v0.0.0-20260709172247-6129f5bee9d5 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/tools v0.50.0 // indirect
	resty.dev/v3 v3.0.0-beta.1 // indirect
)

tool golang.org/x/mobile/cmd/gobind

replace github.com/winfsp/cgofuse => ./third_party/cgofuse
