package ownerlock

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAcquireReleaseAndPeek(t *testing.T) {
	root := t.TempDir()
	lease, err := Acquire(root, Meta{PID: 4242, ConfigPath: "/tmp/qrypt.toml", Fingerprint: "sha256:aaa"}, nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if _, err := os.Stat(Path(root)); err != nil {
		t.Fatalf("lock file: %v", err)
	}
	held, meta := Peek(root)
	if !held {
		t.Fatal("Peek: lock should be held")
	}
	if meta.PID != 4242 || meta.Fingerprint != "sha256:aaa" {
		t.Fatalf("Peek meta = %+v", meta)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	held, _ = Peek(root)
	if held {
		t.Fatal("Peek: lock should be released")
	}
	// Release is idempotent.
	if err := lease.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

func TestAcquireReportsRunningOwner(t *testing.T) {
	root := t.TempDir()
	first, err := Acquire(root, Meta{PID: 7, Fingerprint: "sha256:aaa"}, nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = first.Release() }()

	_, err = Acquire(root, Meta{PID: 8, Fingerprint: "sha256:aaa"}, nil)
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("err = %v, want HeldError", err)
	}
	if held.Holder.PID != 7 {
		t.Fatalf("holder pid = %d, want 7", held.Holder.PID)
	}
}

func TestAcquireReportsFingerprintMismatch(t *testing.T) {
	root := t.TempDir()
	first, err := Acquire(root, Meta{PID: 7, Fingerprint: "sha256:aaa"}, nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = first.Release() }()

	_, err = Acquire(root, Meta{PID: 8, Fingerprint: "sha256:bbb"}, nil)
	var mismatch *MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want MismatchError", err)
	}
	if mismatch.Holder.Fingerprint != "sha256:aaa" || mismatch.Want != "sha256:bbb" {
		t.Fatalf("mismatch = %+v", mismatch)
	}
}

func TestPeekWithoutLockFile(t *testing.T) {
	held, meta := Peek(t.TempDir())
	if held {
		t.Fatal("missing lock file reported as held")
	}
	if meta.PID != 0 {
		t.Fatalf("meta = %+v, want zero", meta)
	}
}

func TestFingerprintFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	if err := os.WriteFile(path, []byte("version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := FingerprintFile(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := FingerprintFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Fatalf("fingerprint not stable: %q != %q", first, again)
	}
	if err := os.WriteFile(path, []byte("version = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := FingerprintFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("fingerprint did not change with config content")
	}
	if got := FingerprintBytes(nil); got == "" {
		t.Fatal("empty fingerprint")
	}
}

func TestValidateDisjoint(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "run")
	cases := []struct {
		name    string
		dirs    []Dir
		wantErr bool
	}{
		{name: "siblings", dirs: []Dir{{Label: "a", Path: filepath.Join(root, "cache", "read")}, {Label: "b", Path: filepath.Join(root, "upload")}}},
		{name: "nested", dirs: []Dir{{Label: "root", Path: root}, {Label: "run", Path: runDir}}, wantErr: true},
		{name: "equal", dirs: []Dir{{Label: "a", Path: root}, {Label: "b", Path: root}}, wantErr: true},
		{name: "missing suffix", dirs: []Dir{{Label: "a", Path: filepath.Join(root, "state")}, {Label: "b", Path: filepath.Join(root, "state", "driver")}}, wantErr: true},
		{name: "empty skipped", dirs: []Dir{{Label: "a", Path: ""}, {Label: "b", Path: root}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateDisjoint(tc.dirs)
			if tc.wantErr && err == nil {
				t.Fatal("want overlap error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected overlap error: %v", err)
			}
		})
	}
}

func TestValidateDisjointResolvesSymlinks(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := ValidateDisjoint([]Dir{{Label: "real", Path: realDir}, {Label: "link", Path: filepath.Join(link, "child")}}); err == nil {
		t.Fatal("symlinked ancestor overlap not detected")
	}
}

func TestAcquireRejectsOverlappingLayout(t *testing.T) {
	root := t.TempDir()
	_, err := Acquire(root, Meta{PID: 1}, []Dir{
		{Label: "root", Path: root},
		{Label: "run", Path: filepath.Join(root, "run")},
	})
	if err == nil {
		t.Fatal("want overlap error")
	}
	if _, statErr := os.Stat(Path(root)); !os.IsNotExist(statErr) {
		t.Fatalf("lock file must not be created for an invalid layout (stat err = %v)", statErr)
	}
}

func TestAcquireSharesLockWithinProcess(t *testing.T) {
	root := t.TempDir()
	meta := Meta{PID: os.Getpid(), ConfigPath: "/tmp/qrypt.toml", Fingerprint: "sha256:aaa", WorkDir: root}
	first, err := Acquire(root, meta, nil)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	second, err := Acquire(root, meta, nil)
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	held, peeked := Peek(root)
	if !held {
		t.Fatal("Peek: lock should be held while two leases exist")
	}
	if peeked.PID != meta.PID {
		t.Fatalf("Peek pid = %d, want %d", peeked.PID, meta.PID)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release first: %v", err)
	}
	if held, _ := Peek(root); !held {
		t.Fatal("lock released while the second lease is still held")
	}
	if err := second.Release(); err != nil {
		t.Fatalf("release second: %v", err)
	}
	if held, _ := Peek(root); held {
		t.Fatal("lock should be released after the last lease")
	}
}

func TestAcquireSameProcessFingerprintMismatch(t *testing.T) {
	root := t.TempDir()
	pid := os.Getpid()
	first, err := Acquire(root, Meta{PID: pid, Fingerprint: "sha256:aaa"}, nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = first.Release() }()

	_, err = Acquire(root, Meta{PID: pid, Fingerprint: "sha256:bbb"}, nil)
	var mismatch *MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want MismatchError", err)
	}
	if mismatch.Want != "sha256:bbb" || mismatch.Holder.Fingerprint != "sha256:aaa" {
		t.Fatalf("mismatch = %+v", mismatch)
	}
}

func TestConcurrentAcquireWithinProcess(t *testing.T) {
	root := t.TempDir()
	meta := Meta{PID: os.Getpid(), Fingerprint: "sha256:aaa"}
	const count = 8
	leases := make([]*Lease, count)
	errs := make([]error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			leases[i], errs[i] = Acquire(root, meta, nil)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Acquire[%d]: %v", i, err)
		}
	}
	if held, _ := Peek(root); !held {
		t.Fatal("Peek: lock should be held")
	}
	for i, lease := range leases {
		if err := lease.Release(); err != nil {
			t.Fatalf("Release[%d]: %v", i, err)
		}
	}
	if held, _ := Peek(root); held {
		t.Fatal("lock should be released after all leases")
	}
}
