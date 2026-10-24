package drivers_test

import (
	"reflect"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/aliyundrive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/baidunetdisk"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/drivers/onedrive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/p115"
	"github.com/yinzhenyu/qrypt/pkg/drivers/p115open"
	"github.com/yinzhenyu/qrypt/pkg/drivers/p123"
	"github.com/yinzhenyu/qrypt/pkg/drivers/quark"
	"github.com/yinzhenyu/qrypt/pkg/drivers/s3"
	"github.com/yinzhenyu/qrypt/pkg/drivers/scopedfs"
	"github.com/yinzhenyu/qrypt/pkg/drivers/sftp"
	"github.com/yinzhenyu/qrypt/pkg/drivers/webdav"
	"github.com/yinzhenyu/qrypt/pkg/drivers/yun139"
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
				drive.CapabilityMtime,
				drive.CapabilityPathResolver,
				drive.CapabilityRemoteNameResolver,
				drive.CapabilityServerSideCopy,
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
				drive.CapabilityServerSideCopy,
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
				drive.CapabilityResumableUploader,
				drive.CapabilityServerSideCopy,
				drive.CapabilitySourceUploader,
				drive.CapabilitySpace,
				drive.CapabilityWriter,
			},
		},
		{
			name: "onedrive",
			drv:  onedrive.New(onedrive.Options{RefreshToken: "token"}),
			want: []drive.Capability{
				drive.CapabilityPathResolver,
				drive.CapabilityRemoteNameResolver,
				drive.CapabilityResumableUploader,
				drive.CapabilityServerSideCopy,
				drive.CapabilitySourceUploader,
				drive.CapabilitySpace,
				drive.CapabilityWriter,
			},
		},
		{
			name: "onedrive_app",
			drv:  onedrive.New(onedrive.Options{AppMode: true, TenantID: "tenant", Email: "user@example.com", ClientID: "client", ClientSecret: "secret"}),
			want: []drive.Capability{
				drive.CapabilityPathResolver,
				drive.CapabilityRemoteNameResolver,
				drive.CapabilityResumableUploader,
				drive.CapabilityServerSideCopy,
				drive.CapabilitySourceUploader,
				drive.CapabilitySpace,
				drive.CapabilityWriter,
			},
		},
		{
			name: "quark",
			drv:  quark.New("cookie", quark.Options{}),
			want: []drive.Capability{
				drive.CapabilityAcceleratedFetch,
				drive.CapabilityOfflineDownload,
				drive.CapabilityOfflineInspect,
				drive.CapabilityOfflineSelection,
				drive.CapabilityPathResolver,
				drive.CapabilityResumableUploader,
				drive.CapabilityServerSideCopy,
				drive.CapabilityShare,
				drive.CapabilityShareFetch,
				drive.CapabilityShareReceive,
				drive.CapabilityShareSave,
				drive.CapabilitySourceUploader,
				drive.CapabilitySpace,
				drive.CapabilityWriter,
			},
		},
		{
			name: "123",
			drv:  p123.New(p123.Options{Username: "user", Password: "password"}),
			want: []drive.Capability{
				drive.CapabilityPathResolver,
				drive.CapabilityResumableUploader,
				drive.CapabilityServerSideCopy,
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
				drive.CapabilityServerSideCopy,
				drive.CapabilitySourceUploader,
				drive.CapabilitySpace,
				drive.CapabilityWriter,
			},
		},
		{
			name: "115",
			drv:  p115.New(p115.Options{Cookie: "k=v"}),
			want: []drive.Capability{
				drive.CapabilityOfflineDownload,
				drive.CapabilityPathResolver,
				drive.CapabilityResumableUploader,
				drive.CapabilityServerSideCopy,
				drive.CapabilitySourceUploader,
				drive.CapabilitySpace,
				drive.CapabilityWriter,
			},
		},
		{
			name: "115_open",
			drv:  p115open.New(p115open.Options{RefreshToken: "token"}),
			want: []drive.Capability{
				drive.CapabilityOfflineDownload,
				drive.CapabilityPathResolver,
				drive.CapabilityRemoteNameResolver,
				drive.CapabilityResumableUploader,
				drive.CapabilityServerSideCopy,
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
				drive.CapabilityResumableUploader,
				drive.CapabilityServerSideCopy,
				drive.CapabilitySourceUploader,
				drive.CapabilityWriter,
			},
		},
		{
			name: "sftp",
			drv:  sftp.New(sftp.Options{Address: "host:22", Username: "user", Password: "password"}),
			want: []drive.Capability{
				drive.CapabilityMtime,
				drive.CapabilityPathResolver,
				drive.CapabilityRemoteNameResolver,
				drive.CapabilityResumableUploader,
				drive.CapabilitySourceUploader,
				drive.CapabilitySpace,
				drive.CapabilityWriter,
			},
		},
		{
			name: "scopedfs",
			drv:  mustScopedFS(t),
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

// TestShareCapabilitiesAreOptIn pins the negative side of the share contract:
// a driver that has not implemented share links and receive work must report no
// share capability and a zero policy, so the CLI can fail with
// share_unsupported instead of calling a half-implemented surface.
func TestShareCapabilitiesAreOptIn(t *testing.T) {
	shareCapabilities := []drive.Capability{
		drive.CapabilityShare,
		drive.CapabilityShareReceive,
		drive.CapabilityShareSave,
		drive.CapabilityShareFetch,
	}
	drivers := []struct {
		name string
		drv  drive.Driver
	}{
		{"localfs", localfs.New(t.TempDir())},
		{"webdav", webdav.New(webdav.Options{URL: "http://example.invalid/"})},
		{"sftp", sftp.New(sftp.Options{Address: "host:22", Username: "user", Password: "password"})},
		{"s3", s3.New(s3.Options{Bucket: "b", Endpoint: "https://example.com"})},
		{"115_open", p115open.New(p115open.Options{RefreshToken: "token"})},
	}
	for _, tt := range drivers {
		t.Run(tt.name, func(t *testing.T) {
			for _, capability := range shareCapabilities {
				if drive.HasCapability(tt.drv, capability) {
					t.Errorf("%s advertises %s without a share implementation", tt.name, capability)
				}
			}
			policy := drive.SharePolicyFor(tt.drv)
			if policy.Passcode != "" || policy.List || policy.Update || policy.VisitLimit {
				t.Errorf("%s declares a share policy: %+v", tt.name, policy)
			}
		})
	}

	quarkDriver := quark.New("cookie", quark.Options{})
	for _, capability := range shareCapabilities {
		if !drive.HasCapability(quarkDriver, capability) {
			t.Errorf("quark does not advertise %s", capability)
		}
	}
	if policy := drive.SharePolicyFor(quarkDriver); policy.Passcode != drive.PolicyOptional || !policy.List {
		t.Fatalf("quark share policy = %+v", policy)
	}
}

func mustScopedFS(t *testing.T) drive.Driver {
	t.Helper()
	drv, err := scopedfs.New(scopedfs.Options{Backend: "capability-test", RootToken: "grant"})
	if err != nil {
		t.Fatal(err)
	}
	return drv
}
