package core

import (
	"context"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/core/internal/credentials"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type credentialProbeDriver struct {
	drive.UnsupportedOperations
	params  drive.Params
	session drive.CredentialSession
}

func (d *credentialProbeDriver) InstallCredentialSession(session drive.CredentialSession) {
	d.session = session
}

func (d *credentialProbeDriver) Init(context.Context) error { return nil }

func (d *credentialProbeDriver) Drop(context.Context) error { return nil }

func (d *credentialProbeDriver) Capabilities() []drive.Capability { return nil }

func (d *credentialProbeDriver) List(context.Context, string) ([]drive.Entry, error) {
	return nil, nil
}

func (d *credentialProbeDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, drive.ErrUnsupported
}

func (d *credentialProbeDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}

func (d *credentialProbeDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{}, nil
}

func (d *credentialProbeDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}

var (
	credentialProbeMu       sync.Mutex
	credentialProbeBuilders []*credentialProbeDriver
)

func init() {
	drive.Register("core-credential-probe", func(params drive.Params) (drive.Driver, error) {
		copied := drive.Params{}
		for key, value := range params {
			copied[key] = value
		}
		probe := &credentialProbeDriver{params: copied}
		credentialProbeMu.Lock()
		credentialProbeBuilders = append(credentialProbeBuilders, probe)
		credentialProbeMu.Unlock()
		return probe, nil
	},
		drive.ParamDef{Name: "cookie", Required: true, Secret: true, Scope: drive.ParamScopeCredential},
		drive.ParamDef{Name: "root_path", Scope: drive.ParamScopeMount},
	)
}

func TestBuildMountSharesOneCredentialSession(t *testing.T) {
	tmp := t.TempDir()
	layout := RuntimeLayout{
		RootDir:      tmp,
		ReadCacheDir: filepath.Join(tmp, "read"),
		UploadDir:    filepath.Join(tmp, "upload"),
		StateDir:     filepath.Join(tmp, "state"),
		DriverDir:    filepath.Join(tmp, "state", "driver"),
	}
	cfg := &config.Config{
		Credentials: []config.CredentialConfig{{
			Name:   "shared",
			Type:   "core-credential-probe",
			Params: config.ParamMap{"cookie": "k=v"},
		}},
		Mounts: []config.MountConfig{
			{Name: "a", Type: "core-credential-probe", Credential: "shared", Params: config.ParamMap{"root_path": "/A"}},
			{Name: "b", Type: "core-credential-probe", Credential: "shared", Params: config.ParamMap{"root_path": "/B"}},
		},
	}
	manager := credentials.Open(layout.StateDir)
	first := buildMount(context.Background(), cfg, layout, nil, manager, cfg.Mounts[0])
	second := buildMount(context.Background(), cfg, layout, nil, manager, cfg.Mounts[1])
	if first.err != nil || second.err != nil {
		t.Fatalf("build mounts: %v / %v", first.err, second.err)
	}
	defer closeMounts([]vfs.Mount{first.mount, second.mount})

	driverA, ok := first.driver.(*credentialProbeDriver)
	if !ok {
		t.Fatalf("first driver type = %T", first.driver)
	}
	driverB, ok := second.driver.(*credentialProbeDriver)
	if !ok {
		t.Fatalf("second driver type = %T", second.driver)
	}
	if driverA.session == nil || driverB.session == nil {
		t.Fatal("credential session was not installed")
	}
	if driverA.session != driverB.session {
		t.Fatal("mounts of one credential received different session objects")
	}
	if driverA.params["cookie"] != "k=v" || driverB.params["cookie"] != "k=v" {
		t.Fatalf("credential params = %v / %v", driverA.params, driverB.params)
	}
	if driverA.params["root_path"] != "/A" || driverB.params["root_path"] != "/B" {
		t.Fatalf("mount params = %v / %v", driverA.params, driverB.params)
	}
}
