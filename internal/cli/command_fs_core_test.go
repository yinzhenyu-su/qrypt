package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFSCommandsThroughCore exercises the data plane: put/list/stat/mv run
// through the resident Core, and the FUSE-visible remote state reflects them.
func TestFSCommandsThroughCore(t *testing.T) {
	base, err := os.MkdirTemp("", "qrypt-fs-core-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	configPath, _ := writeCoreServeTestConfig(t, base)
	remote := filepath.Join(base, "remote")
	t.Setenv("QRYPT_CORE_FS_REQUIRED", "1")

	serveRoot := NewRootCommand()
	var serveOut bytes.Buffer
	serveRoot.SetOut(&serveOut)
	serveRoot.SetErr(&serveOut)
	serveRoot.SetArgs([]string{"core", "serve", "--foreground", "--keep", "--config", configPath})
	done := make(chan error, 1)
	go func() { done <- serveRoot.Execute() }()
	t.Cleanup(func() {
		select {
		case <-done:
			return
		default:
		}
		_, _ = executeRoot(t, "core", "stop", "--force", "--config", configPath)
		<-done
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := executeRoot(t, "core", "status", "--json", "--config", configPath)
		if err == nil && strings.Contains(out, `"state": "running"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("core did not become ready: err=%v out=%s serve=%s", err, out, serveOut.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	local := filepath.Join(base, "local.txt")
	if err := os.WriteFile(local, []byte("core data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := executeRoot(t, "fs", "--config", configPath, "put", "--wait-timeout", "10s", local, "/local/uploaded.txt"); err != nil {
		t.Fatalf("fs put via core: %v", err)
	}
	if _, err := executeRoot(t, "fs", "--config", configPath, "mv", "/local/uploaded.txt", "/local/moved.txt"); err != nil {
		t.Fatalf("fs mv via core: %v", err)
	}
	statOut, err := executeRoot(t, "fs", "--config", configPath, "stat", "--json", "/local/moved.txt")
	if err != nil {
		t.Fatalf("fs stat via core: %v", err)
	}
	var stat struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := json.Unmarshal([]byte(statOut), &stat); err != nil {
		t.Fatalf("decode stat: %v (%s)", err, statOut)
	}
	if stat.Name != "moved.txt" || stat.Size != int64(len("core data")) {
		t.Fatalf("stat = %+v", stat)
	}
	listOut, err := executeRoot(t, "fs", "--config", configPath, "list", "--json", "/local")
	if err != nil {
		t.Fatalf("fs list via core: %v", err)
	}
	if !strings.Contains(listOut, "moved.txt") {
		t.Fatalf("list missing uploaded entry: %s", listOut)
	}
	data, err := os.ReadFile(filepath.Join(remote, "moved.txt"))
	if err != nil {
		t.Fatalf("remote file missing: %v", err)
	}
	if string(data) != "core data" {
		t.Fatalf("remote data = %q", data)
	}

	// The listing was persisted by the Core; an external change is invisible
	// within the TTL and becomes visible after --refresh.
	if err := os.WriteFile(filepath.Join(remote, "external.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cachedOut, err := executeRoot(t, "fs", "--config", configPath, "list", "--json", "/local")
	if err != nil {
		t.Fatalf("cached list via core: %v", err)
	}
	if strings.Contains(cachedOut, "external.txt") {
		t.Fatalf("external change visible before TTL: %s", cachedOut)
	}
	refreshedOut, err := executeRoot(t, "fs", "--config", configPath, "list", "--json", "--refresh", "/local")
	if err != nil {
		t.Fatalf("refresh list via core: %v", err)
	}
	if !strings.Contains(refreshedOut, "external.txt") {
		t.Fatalf("refresh did not refetch: %s", refreshedOut)
	}

	// Remaining read commands must use the same owner runtime.
	catOut, err := executeRoot(t, "fs", "--config", configPath, "cat", "/local/moved.txt")
	if err != nil || catOut != "core data" {
		t.Fatalf("fs cat via core: out=%q err=%v", catOut, err)
	}
	findOut, err := executeRoot(t, "fs", "--config", configPath, "find", "/local", "moved")
	if err != nil || !strings.Contains(findOut, "/local/moved.txt") {
		t.Fatalf("fs find via core: out=%q err=%v", findOut, err)
	}
	duOut, err := executeRoot(t, "fs", "--config", configPath, "du", "--json", "/local")
	if err != nil {
		t.Fatalf("fs du via core: %v", err)
	}
	var du struct {
		Files int   `json:"files"`
		Bytes int64 `json:"bytes"`
	}
	if err := json.Unmarshal([]byte(duOut), &du); err != nil || du.Files < 2 || du.Bytes < int64(len("core data")) {
		t.Fatalf("du = %+v err=%v (%s)", du, err, duOut)
	}
	if _, err := executeRoot(t, "fs", "--config", configPath, "df", "--json"); err != nil {
		t.Fatalf("fs df via core: %v", err)
	}
	pendingOut, err := executeRoot(t, "fs", "--config", configPath, "pending", "--json")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(pendingOut), "[") {
		t.Fatalf("fs pending via core: out=%q err=%v", pendingOut, err)
	}
	big := make([]byte, 2<<20)
	for i := range big {
		big[i] = byte(i * 7)
	}
	bigLocal := filepath.Join(base, "big.bin")
	if err := os.WriteFile(bigLocal, big, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := executeRoot(t, "fs", "--config", configPath, "put", "--wait-timeout", "10s", bigLocal, "/local/big.bin"); err != nil {
		t.Fatalf("fs put big via core: %v", err)
	}
	bigOut, err := executeRoot(t, "fs", "--config", configPath, "cat", "/local/big.bin")
	if err != nil {
		t.Fatalf("fs cat big via core: %v", err)
	}
	if len(bigOut) != len(big) || bigOut != string(big) {
		t.Fatalf("big cat length = %d, want %d", len(bigOut), len(big))
	}

	// Write commands share the same owner Committer and pending projection.
	if _, err := executeRoot(t, "fs", "--config", configPath, "mkdir", "/local/sub"); err != nil {
		t.Fatalf("fs mkdir via core: %v", err)
	}
	smallLocal := filepath.Join(base, "small.txt")
	if err := os.WriteFile(smallLocal, []byte("small payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := executeRoot(t, "fs", "--config", configPath, "put", "--wait-timeout", "10s", smallLocal, "/local/sub/a.txt"); err != nil {
		t.Fatalf("fs put small via core: %v", err)
	}
	if _, err := executeRoot(t, "fs", "--config", configPath, "copy", "/local/sub/a.txt", "/local/sub/b.txt"); err != nil {
		t.Fatalf("fs copy file via core: %v", err)
	}
	copiedOut, err := executeRoot(t, "fs", "--config", configPath, "cat", "/local/sub/b.txt")
	if err != nil || copiedOut != "small payload" {
		t.Fatalf("copied file content = %q err=%v", copiedOut, err)
	}
	if _, err := executeRoot(t, "fs", "--config", configPath, "copy", "-r", "/local/sub", "/local/dir2"); err != nil {
		t.Fatalf("fs copy dir via core: %v", err)
	}
	dirCopiedOut, err := executeRoot(t, "fs", "--config", configPath, "cat", "/local/dir2/sub/a.txt")
	if err != nil || dirCopiedOut != "small payload" {
		t.Fatalf("dir copy content = %q err=%v", dirCopiedOut, err)
	}
	if _, err := executeRoot(t, "fs", "--config", configPath, "rm", "--wait-timeout", "10s", "/local/sub/b.txt"); err != nil {
		t.Fatalf("fs rm via core: %v", err)
	}
	if _, err := executeRoot(t, "fs", "--config", configPath, "stat", "/local/sub/b.txt"); err == nil {
		t.Fatal("removed file should be gone through the owner view")
	}
	gotLocal := filepath.Join(base, "downloaded.bin")
	if _, err := executeRoot(t, "fs", "--config", configPath, "get", "/local/big.bin", gotLocal); err != nil {
		t.Fatalf("fs get via core: %v", err)
	}
	gotBytes, err := os.ReadFile(gotLocal)
	if err != nil || len(gotBytes) != len(big) || string(gotBytes) != string(big) {
		t.Fatalf("downloaded bytes = %d err=%v, want %d", len(gotBytes), err, len(big))
	}

	// Two clients racing to remove the same path must be serialized by the
	// Core's owner VFS: exactly one remove observes the file and succeeds.
	rmErrs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := executeRoot(t, "fs", "--config", configPath, "rm", "/local/dir2/sub/a.txt")
			rmErrs <- err
		}()
	}
	successes := 0
	for range 2 {
		if err := <-rmErrs; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent rm successes = %d, want 1", successes)
	}

	if _, err := executeRoot(t, "fs", "--config", configPath, "stat", "/local/does-not-exist"); err == nil {
		t.Fatal("fs stat of a missing path should fail")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing path error = %v", err)
	}
}
