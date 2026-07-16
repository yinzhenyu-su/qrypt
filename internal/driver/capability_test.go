package driver_test

import (
	"reflect"
	"testing"

	"github.com/yinzhenyu/qrypt/internal/driver/aliyundrive"
	"github.com/yinzhenyu/qrypt/internal/driver/baidunetdisk"
	"github.com/yinzhenyu/qrypt/internal/driver/localfs"
	"github.com/yinzhenyu/qrypt/internal/driver/p115"
	"github.com/yinzhenyu/qrypt/internal/driver/quark"
	"github.com/yinzhenyu/qrypt/internal/driver/s3"
	"github.com/yinzhenyu/qrypt/internal/driver/webdav"
	"github.com/yinzhenyu/qrypt/internal/driver/yun139"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestBuiltinDriverCapabilities(t *testing.T) {
	tests := []struct {
		name string
		drv  drive.Driver
		want []drive.Capability
	}{
		{
			name: "localfs",
			drv:  localfs.New(t.TempDir()),
			want: []drive.Capability{
				drive.CapabilityPathResolver,
				drive.CapabilityRemoteNameResolver,
				drive.CapabilitySourceUploader,
				drive.CapabilitySpace,
				drive.CapabilityWriter,
			},
		},
		{
			name: "aliyundrive",
			drv:  aliyundrive.New(aliyundrive.Options{RefreshToken: "token", DriveID: "drive"}),
			want: []drive.Capability{
				drive.CapabilityPathResolver,
				drive.CapabilityResumableUploader,
				drive.CapabilitySourceUploader,
				drive.CapabilitySpace,
				drive.CapabilityWriter,
			},
		},
		{
			name: "baidu_netdisk",
			drv:  baidunetdisk.New(baidunetdisk.Options{RefreshToken: "token"}),
			want: []drive.Capability{
				drive.CapabilityPathResolver,
				drive.CapabilitySourceUploader,
				drive.CapabilitySpace,
				drive.CapabilityWriter,
			},
		},
		{
			name: "quark",
			drv:  quark.New("cookie", quark.Options{}),
			want: []drive.Capability{
				drive.CapabilityPathResolver,
				drive.CapabilityResumableUploader,
				drive.CapabilitySourceUploader,
				drive.CapabilitySpace,
				drive.CapabilityWriter,
			},
		},
		{
			name: "yun139",
			drv:  yun139.New("authorization", "", ""),
			want: []drive.Capability{
				drive.CapabilityPathResolver,
				drive.CapabilityResumableUploader,
				drive.CapabilitySourceUploader,
				drive.CapabilitySpace,
				drive.CapabilityWriter,
			},
		},
		{
			name: "webdav",
			drv:  webdav.New(webdav.Options{URL: "http://example.invalid/"}),
			want: []drive.Capability{
				drive.CapabilityPathResolver,
				drive.CapabilitySourceUploader,
				drive.CapabilitySpace,
				drive.CapabilityWriter,
			},
		},
		{
			name: "115",
			drv:  p115.New(p115.Options{Cookie: "k=v"}),
			want: []drive.Capability{
				drive.CapabilityPathResolver,
				drive.CapabilitySourceUploader,
				drive.CapabilitySpace,
				drive.CapabilityWriter,
			},
		},
		{
			name: "s3",
			drv:  s3.New(s3.Options{Bucket: "b", Endpoint: "https://example.com"}),
			want: []drive.Capability{
				drive.CapabilityPathResolver,
				drive.CapabilityRemoteNameResolver,
				drive.CapabilitySourceUploader,
				drive.CapabilityWriter,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := drive.Capabilities(tt.drv)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("capabilities = %+v, want %+v", got, tt.want)
			}
			if violations := drive.CheckUnsupportedCapabilities(t.Context(), tt.drv); len(violations) != 0 {
				t.Fatalf("negative capability contract violations = %+v", violations)
			}
		})
	}
}
