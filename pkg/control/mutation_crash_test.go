//go:build !windows

package control

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// This file is the crash-recovery harness for write operations (P0 task 0.2).
//
// It runs a real control server in a re-executed test-binary child process and
// kills that child with SIGKILL at a chosen point around a filesystem
// mutation. A second child then retries the same operation ID against the same
// state directory. The harness records every remote effect in an fsync'ed log,
// so the parent can assert that the retry replayed the recorded result instead
// of executing the mutation a second time.
//
// The tests are opt-in while the durable operation ledger does not exist yet
// (P0 task 0.3): with QRYPT_TEST_CRASH_HARNESS=1 the after-effect cases are
// expected to fail, and that failure is the reproducible defect this harness
// guards. Remove the gate when 0.3 lands, so CI runs the matrix by default.
const (
	crashHarnessEnv = "QRYPT_TEST_CRASH_HARNESS"
	crashHelperEnv  = "QRYPT_TEST_MUTATION_HELPER"
	crashPointEnv   = "QRYPT_TEST_MUTATION_CRASH"
	crashOpEnv      = "QRYPT_TEST_MUTATION_OP"
	crashLogEnv     = "QRYPT_TEST_MUTATION_EFFECTS"
	crashSocketEnv  = "QRYPT_TEST_MUTATION_SOCKET"
	crashReadyEnv   = "QRYPT_TEST_MUTATION_READY"
)

// TestMutationCrashHelper is the child-process entry point; it is skipped in
// normal test runs because the parent sets crashHelperEnv only for the child.
func TestMutationCrashHelper(t *testing.T) {
	if os.Getenv(crashHelperEnv) != "1" {
		t.Skip("crash harness child process")
	}
	effects, err := os.OpenFile(os.Getenv(crashLogEnv), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open effects log: %v", err)
	}
	defer effects.Close()
	fs := &crashFS{
		fsStub:  &fsStub{},
		effects: effects,
		point:   os.Getenv(crashPointEnv),
		op:      os.Getenv(crashOpEnv),
	}
	server, err := NewCommandServer(os.Getenv(crashSocketEnv), stubController{})
	if err != nil {
		t.Fatalf("new command server: %v", err)
	}
	server.SetFSService(fs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("start command server: %v", err)
	}
	if err := os.WriteFile(os.Getenv(crashReadyEnv), []byte("ready"), 0o600); err != nil {
		t.Fatalf("write ready file: %v", err)
	}
	<-ctx.Done()
}

// stubController satisfies CommandService; the crash harness only exercises
// the filesystem data plane.
type stubController struct{}

func (stubController) CancelTask(context.Context, string) error { return errors.New("unsupported") }
func (stubController) RetryTask(context.Context, string) error  { return errors.New("unsupported") }

// crashFS wraps the regular filesystem stub. The configured operation applies
// its effect and then records it in an fsync'ed log; depending on point it
// either dies before the effect ("before") or right after it ("after").
type crashFS struct {
	*fsStub
	effects *os.File
	point   string
	op      string
}

func (c *crashFS) record(op string) {
	if c.op != op {
		return
	}
	if _, err := fmt.Fprintln(c.effects, op); err != nil {
		panic("crash harness: write effect: " + err.Error())
	}
	if err := c.effects.Sync(); err != nil {
		panic("crash harness: sync effect: " + err.Error())
	}
}

func (c *crashFS) before(op string) {
	if c.op == op && c.point == "before" {
		killSelf()
	}
}

// after records every applied effect, so the parent can count them across both
// children; only the configured crash point kills the process.
func (c *crashFS) after(op string) {
	c.record(op)
	if c.op == op && c.point == "after" {
		killSelf()
	}
}

func killSelf() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	select {} // SIGKILL is asynchronous; never return after asking for it.
}

func (c *crashFS) Mkdir(ctx context.Context, path string) (FSEntry, error) {
	c.before("mkdir")
	entry, err := c.fsStub.Mkdir(ctx, path)
	c.after("mkdir")
	return entry, err
}

func (c *crashFS) Remove(ctx context.Context, path string, dir bool) error {
	c.before("remove")
	err := c.fsStub.Remove(ctx, path, dir)
	c.after("remove")
	return err
}

func (c *crashFS) Rename(ctx context.Context, source, destination string) error {
	c.before("rename")
	err := c.fsStub.Rename(ctx, source, destination)
	c.after("rename")
	return err
}

func (c *crashFS) Truncate(ctx context.Context, path string, size int64) error {
	c.before("truncate")
	err := c.fsStub.Truncate(ctx, path, size)
	c.after("truncate")
	return err
}

func (c *crashFS) Flush(ctx context.Context, path string) error {
	c.before("flush")
	err := c.fsStub.Flush(ctx, path)
	c.after("flush")
	return err
}

func requireCrashHarness(t *testing.T) {
	t.Helper()
	if os.Getenv(crashHarnessEnv) != "1" {
		t.Skip("crash-recovery matrix is opt-in until the durable operation ledger lands (P0 0.3); set QRYPT_TEST_CRASH_HARNESS=1 and expect the after-effect cases to fail")
	}
}

// mutationCase names one write operation and how to issue it.
type mutationCase struct {
	name string
	op   string
	call func(ctx context.Context, c *Client) error
}

func crashMutationCases() []mutationCase {
	return []mutationCase{
		{name: "mkdir", op: "mkdir", call: func(ctx context.Context, c *Client) error {
			_, err := c.FSMkdir(ctx, "/local/crash-dir")
			return err
		}},
		{name: "remove", op: "remove", call: func(ctx context.Context, c *Client) error {
			return c.FSRemove(ctx, "/local/crash-file", false)
		}},
		{name: "rename", op: "rename", call: func(ctx context.Context, c *Client) error {
			return c.FSRename(ctx, "/local/crash-src", "/local/crash-dst")
		}},
		{name: "truncate", op: "truncate", call: func(ctx context.Context, c *Client) error {
			return c.FSTruncate(ctx, "/local/crash-file", 0)
		}},
		// put is the create → write → flush sequence; only the flush commits the
		// upload, so that is where the crash point sits. Each step carries its
		// own operation ID, exactly like the CLI does per request.
		{name: "put", op: "flush", call: func(ctx context.Context, c *Client) error {
			if err := c.FSCreate(WithOperationID(ctx, "operation-put-create"), "/local/crash-put"); err != nil {
				return err
			}
			if _, err := c.FSWriteAt(WithOperationID(ctx, "operation-put-write"), "/local/crash-put", 0, []byte("payload")); err != nil {
				return err
			}
			return c.FSFlush(ctx, "/local/crash-put")
		}},
	}
}

// crashHarness runs one child at a time against a short state directory.
type crashHarness struct {
	t      *testing.T
	dir    string
	name   string
	socket string
	log    string
	cmd    *exec.Cmd
	stop   sync.Once
}

func newCrashHarness(t *testing.T, name string) *crashHarness {
	t.Helper()
	// Keep the unix socket path short: sun_path is limited to 104 bytes on
	// macOS, and t.TempDir() is usually too long.
	dir, err := os.MkdirTemp("", "qrypt-crash-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return &crashHarness{t: t, dir: dir, name: name, socket: filepath.Join(dir, name+".sock"), log: filepath.Join(dir, "effects.log")}
}

func (h *crashHarness) start(point, op string) *Client {
	h.t.Helper()
	ready := filepath.Join(h.dir, h.name+".ready")
	_ = os.Remove(ready)
	cmd := exec.Command(os.Args[0], "-test.run=^TestMutationCrashHelper$")
	cmd.Env = append(os.Environ(),
		crashHelperEnv+"=1",
		crashSocketEnv+"="+h.socket,
		crashReadyEnv+"="+ready,
		crashLogEnv+"="+h.log,
		crashPointEnv+"="+point,
		crashOpEnv+"="+op,
	)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		h.t.Fatalf("start helper: %v", err)
	}
	h.cmd = cmd
	h.t.Cleanup(func() {
		h.stop.Do(func() {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		})
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("helper did not become ready: %s", output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	client, err := NewClient(h.socket)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { client.CloseIdleConnections() })
	return client
}

// waitKilled blocks until the current child has been killed by SIGKILL.
func (h *crashHarness) waitKilled() {
	h.t.Helper()
	done := make(chan error, 1)
	go func() { done <- h.cmd.Wait() }()
	select {
	case err := <-done:
		h.stop.Do(func() {}) // Wait already reaped the process.
		if err == nil {
			h.t.Fatalf("helper exited without a crash")
		}
		if sig, ok := err.(*exec.ExitError); !ok || sig.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
			h.t.Fatalf("helper exit error = %v, want SIGKILL", err)
		}
	case <-time.After(10 * time.Second):
		h.t.Fatal("helper still running after the crash trigger")
	}
}

func (h *crashHarness) effectCount(op string) int {
	h.t.Helper()
	data, err := os.ReadFile(h.log)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		h.t.Fatal(err)
	}
	return strings.Count(string(data), op+"\n")
}

// TestMutationRetryAfterCrashReplays is the crash-point matrix: retry the same
// operation ID after a SIGKILL and assert the remote effect happened once.
func TestMutationRetryAfterCrashReplays(t *testing.T) {
	requireCrashHarness(t)
	for _, tc := range crashMutationCases() {
		t.Run(tc.name, func(t *testing.T) {
			h := newCrashHarness(t, tc.name)
			client := h.start("after", tc.op)
			ctx := WithOperationID(context.Background(), "operation-"+tc.name)
			if err := tc.call(ctx, client); err == nil {
				t.Fatal("first attempt unexpectedly succeeded across a crash")
			}
			h.waitKilled()

			retry := h.start("", tc.op)
			if err := tc.call(ctx, retry); err != nil {
				t.Fatalf("retry with the same operation ID failed: %v", err)
			}
			if got := h.effectCount(tc.op); got != 1 {
				t.Fatalf("remote effects after retry = %d, want 1 (the retry must replay the recorded result instead of writing again)", got)
			}
		})
	}
}

// TestMutationRetryBeforeCrashIsSafe covers the other side of the matrix: a
// crash before the remote effect leaves no object, so the retry must apply it
// exactly once.
func TestMutationRetryBeforeCrashIsSafe(t *testing.T) {
	requireCrashHarness(t)
	h := newCrashHarness(t, "mkdir-before")
	client := h.start("before", "mkdir")
	ctx := WithOperationID(context.Background(), "operation-before")
	if _, err := client.FSMkdir(ctx, "/local/crash-before"); err == nil {
		t.Fatal("mkdir unexpectedly succeeded across a crash")
	}
	h.waitKilled()

	retry := h.start("", "mkdir")
	if _, err := retry.FSMkdir(ctx, "/local/crash-before"); err != nil {
		t.Fatalf("retry after a pre-effect crash failed: %v", err)
	}
	if got := h.effectCount("mkdir"); got != 1 {
		t.Fatalf("remote effects after retry = %d, want 1", got)
	}
}
