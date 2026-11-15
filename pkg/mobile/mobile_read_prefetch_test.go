package mobile

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// gatedPrefetchDriver lets the foreground read at chunk 1 complete while
// blocking the read-ahead at chunk 2. That is the exact state issue #20
// describes: the handle read is done, speculative prefetch is still in
// flight, and closing or canceling the handle must stop it.
type gatedPrefetchDriver struct {
	*drive.FakeDriver
	gateAt   int64
	entered  chan struct{}
	canceled chan struct{}
	once     sync.Once
	cancel   sync.Once
}

func (d *gatedPrefetchDriver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if offset >= d.gateAt {
		d.once.Do(func() { close(d.entered) })
		<-ctx.Done()
		d.cancel.Do(func() { close(d.canceled) })
		return nil, ctx.Err()
	}
	return d.FakeDriver.Read(ctx, entry, offset, size)
}

func TestMobileCloseFileStopsSessionPrefetch(t *testing.T) {
	runMobilePrefetchLifecycle(t, "close", CloseFileJSON, false)
}

func TestMobileCancelFileReadStopsSessionPrefetch(t *testing.T) {
	runMobilePrefetchLifecycle(t, "cancel", CancelFileReadJSON, true)
}

func runMobilePrefetchLifecycle(t *testing.T, name string, action func(string) string, handleRemains bool) {
	t.Helper()
	base := drive.NewFakeDriver()
	content := make([]byte, 4<<20)
	for i := range content {
		content[i] = byte(i)
	}
	if err := base.Seed(map[string]string{"file.bin": string(content)}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	driver := &gatedPrefetchDriver{
		FakeDriver: base,
		gateAt:     1 << 20,
		entered:    make(chan struct{}),
		canceled:   make(chan struct{}),
	}
	driverName := "mobile-prefetch-" + name
	drive.Register(driverName, func(drive.Params) (drive.Driver, error) { return driver, nil })

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "quark"
type = "`+driverName+`"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatalf("openCore: %v", err)
	}
	defer func() { _ = closeCore(coreID) }()

	var opened struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(OpenFileJSON(coreID, "/quark/file.bin", "")), &opened); err != nil {
		t.Fatalf("OpenFileJSON: %v", err)
	}
	if !opened.OK || opened.Data == "" {
		t.Fatalf("OpenFileJSON = %+v, want handle", opened)
	}
	handleID := opened.Data

	// Read chunk 1, then let the read-ahead move to chunk 2 and block there.
	buf := make([]byte, 1)
	if n, err := ReadAtInto(handleID, int64(512<<10)+100, buf, 0); err != nil || n != 1 {
		t.Fatalf("ReadAtInto n=%d err=%v, want one byte", n, err)
	}
	select {
	case <-driver.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("read-ahead did not start after the handle read")
	}

	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(action(handleID)), &result); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if !result.OK {
		t.Fatalf("%s = %+v, want ok", name, result)
	}
	select {
	case <-driver.canceled:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not stop the session read-ahead", name)
	}

	if handleRemains {
		// CancelFileReadJSON leaves the handle usable; a plain read still works.
		if n, err := ReadAtInto(handleID, 0, buf, 0); err != nil || n != 1 {
			t.Fatalf("ReadAtInto after cancel n=%d err=%v, want usable handle", n, err)
		}
		if raw := CloseFileJSON(handleID); !json.Valid([]byte(raw)) {
			t.Fatalf("CloseFileJSON after cancel = %q, want JSON", raw)
		}
	}
}
