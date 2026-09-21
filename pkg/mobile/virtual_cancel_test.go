package mobile

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// virtualCancelDriver is a test seam standing between the virtual read path
// and the backend: it extends drive.FakeDriver with a one-shot blocking Read,
// so a test can hold an in-flight virtual read inside the mobile read stack
// and then release it through CancelVirtualReadJSON (the per-read context) -
// no sleeps, fully channel-synchronized.
type virtualCancelDriver struct {
	mu          sync.Mutex
	armed       bool
	entered     chan struct{} // closed once an armed Read is parked
	release     chan struct{} // closed at cleanup so no read is ever stuck
	enteredOnce sync.Once
	releaseOnce sync.Once
	*drive.FakeDriver
}

func newVirtualCancelDriver(content string) *virtualCancelDriver {
	d := &virtualCancelDriver{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	d.FakeDriver = drive.NewFakeDriver()
	if err := d.Seed(map[string]string{"video.bin": content}); err != nil {
		panic(err) // seeded content is static test data
	}
	return d
}

// arm makes the next Read wait until the per-read context is canceled or the
// release channel is closed. Only one read blocks; later reads pass through
// to the fake driver untouched, which is what lets the test verify that a
// canceled handle remains usable.
func (d *virtualCancelDriver) arm() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.armed = true
}

func (d *virtualCancelDriver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	d.mu.Lock()
	armed := d.armed
	if armed {
		d.armed = false
	}
	d.mu.Unlock()
	if armed {
		d.enteredOnce.Do(func() { close(d.entered) })
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.release:
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return d.FakeDriver.Read(ctx, entry, offset, size)
}

// unblock releases any parked read. Safe to call multiple times and from
// cleanup, so failure paths never leak the read goroutine.
func (d *virtualCancelDriver) unblock() {
	d.releaseOnce.Do(func() { close(d.release) })
}

// openVirtualCancelCore opens a core whose quark mount is backed by the given
// driver, registered under a name unique to this test.
func openVirtualCancelCore(t *testing.T, driver *virtualCancelDriver) string {
	t.Helper()
	driverName := "mobile-virtual-cancel"
	drive.Register(driverName, func(_ drive.Params) (drive.Driver, error) { return driver, nil })
	configPath := filepath.Join(t.TempDir(), "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "quark"
type = "`+driverName+`"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	coreID, err := openCore(configPath, testRuntimeJSON(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeCore(coreID) })
	return coreID
}

func openVirtualHandleJSON(t *testing.T, coreID string) string {
	t.Helper()
	raw := OpenVirtualFileJSON(coreID, "/quark/video.bin", "passthrough", 0)
	var opened struct {
		OK   bool              `json:"ok"`
		Data virtualOpenResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &opened); err != nil {
		t.Fatal(err)
	}
	if !opened.OK || opened.Data.Handle == "" {
		t.Fatalf("OpenVirtualFileJSON = %s, want handle", raw)
	}
	t.Cleanup(func() { _ = CloseVirtualFileJSON(opened.Data.Handle) })
	return opened.Data.Handle
}

func TestMobileCancelVirtualReadJSON(t *testing.T) {
	content := []byte("0123456789abcdef")
	driver := newVirtualCancelDriver(string(content))
	defer driver.unblock()
	coreID := openVirtualCancelCore(t, driver)
	handleID := openVirtualHandleJSON(t, coreID)

	driver.arm()
	type readResult struct {
		n   int
		err error
	}
	results := make(chan readResult, 1)
	go func() {
		buf := make([]byte, len(content))
		n, err := ReadVirtualFileAtInto(handleID, 0, buf, 0)
		results <- readResult{n: n, err: err}
	}()

	// Wait until the read is parked inside the driver, then cancel it.
	<-driver.entered
	if raw := CancelVirtualReadJSON(handleID); !stringsHasOK(raw) {
		t.Fatalf("CancelVirtualReadJSON = %s, want ok", raw)
	}

	select {
	case res := <-results:
		if res.err == nil {
			t.Fatalf("canceled read n=%d err=%v, want a context cancellation error", res.n, res.err)
		}
	case <-t.Context().Done():
		t.Fatal("read did not return after CancelVirtualReadJSON")
	}

	// The handle stays usable: a later read completes normally with the
	// backend bytes even though the earlier read was aborted.
	buf := make([]byte, len(content))
	n, err := ReadVirtualFileAtInto(handleID, 0, buf, 0)
	if err != nil {
		t.Fatalf("read after cancel: %v", err)
	}
	if n != len(content) || !bytes.Equal(buf[:n], content) {
		t.Fatalf("read after cancel n=%d data=%q, want %q", n, buf[:n], content)
	}
}

func stringsHasOK(raw string) bool {
	var env struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return false
	}
	return env.OK
}
