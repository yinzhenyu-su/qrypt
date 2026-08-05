package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// ContractSandbox is an isolated directory created for behavioral contract
// checks that mutate the backend. Callers must Cleanup it.
type ContractSandbox struct {
	d  Driver
	ID string
}

// NewContractSandbox creates a uniquely named directory under the driver's
// root and returns its handle. It reports an error when the driver cannot
// resolve a root to place the sandbox in.
func NewContractSandbox(ctx context.Context, d Driver) (*ContractSandbox, error) {
	rootID, err := contractRootID(ctx, d)
	if err != nil {
		return nil, fmt.Errorf("contract: resolve root: %w", err)
	}
	name := fmt.Sprintf("__qrypt_contract_%d", time.Now().UnixNano())
	dir, err := d.Mkdir(ctx, rootID, name)
	if err != nil {
		return nil, fmt.Errorf("contract: create sandbox %q: %w", name, err)
	}
	return &ContractSandbox{d: d, ID: dir.ID}, nil
}

// Cleanup removes the sandbox directory (best effort; drives converge
// asynchronously so the caller should not rely on immediate removal).
func (s *ContractSandbox) Cleanup(ctx context.Context) error {
	if s == nil || s.ID == "" {
		return nil
	}
	return s.d.Remove(ctx, Entry{ID: s.ID, IsDir: true, Name: "__qrypt_contract_sandbox"})
}

// contractRootID resolves the driver root, preferring the path resolver and
// falling back to probing common root IDs.
func contractRootID(ctx context.Context, d Driver) (string, error) {
	if HasCapability(d, CapabilityPathResolver) {
		if rootID, err := d.ResolvePath(ctx, "/"); err == nil && rootID != "" {
			return rootID, nil
		}
	}
	for _, candidate := range []string{"", "root", "-11", "0"} {
		entries, err := d.List(ctx, candidate)
		if err != nil {
			continue
		}
		if candidate == "" {
			for _, e := range entries {
				if e.IsDir {
					return e.ID, nil
				}
			}
		}
		return candidate, nil
	}
	return "", fmt.Errorf("no root id found")
}

// ContractViolation describes one failed behavioral contract check.
type ContractViolation struct {
	Name string
	Err  error
}

func (v ContractViolation) Error() string { return v.Name + ": " + v.Err.Error() }

// entryNames returns the names of the entries for readable failure messages.
func entryNames(entries []Entry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return names
}

// CheckListDirectChildren verifies that List returns only direct children:
// a file nested inside a subdirectory must not appear in the parent's listing.
func CheckListDirectChildren(ctx context.Context, d Driver) []ContractViolation {
	sb, err := NewContractSandbox(ctx, d)
	if err != nil {
		return []ContractViolation{{Name: "list_direct_children", Err: err}}
	}
	defer func() { _ = sb.Cleanup(context.WithoutCancel(ctx)) }()

	sub, err := d.Mkdir(ctx, sb.ID, "sub")
	if err != nil {
		return []ContractViolation{{Name: "list_direct_children", Err: fmt.Errorf("mkdir sub: %w", err)}}
	}
	if _, err := d.PutSource(ctx, UploadRequest{
		ParentID: sb.ID, Name: "a.txt", Source: NewBytesReadOnlyFileSource([]byte("aaa")),
	}); err != nil {
		return []ContractViolation{{Name: "list_direct_children", Err: fmt.Errorf("put a.txt: %w", err)}}
	}
	if _, err := d.PutSource(ctx, UploadRequest{
		ParentID: sub.ID, Name: "b.txt", Source: NewBytesReadOnlyFileSource([]byte("bbbb")),
	}); err != nil {
		return []ContractViolation{{Name: "list_direct_children", Err: fmt.Errorf("put sub/b.txt: %w", err)}}
	}

	parent, err := d.List(ctx, sb.ID)
	if err != nil {
		return []ContractViolation{{Name: "list_direct_children", Err: fmt.Errorf("list sandbox: %w", err)}}
	}
	names := map[string]bool{}
	for _, e := range parent {
		names[e.Name] = true
	}
	for _, want := range []string{"a.txt", "sub"} {
		if !names[want] {
			return []ContractViolation{{Name: "list_direct_children", Err: fmt.Errorf("parent listing missing %q, got %v", want, entryNames(parent))}}
		}
	}
	if names["b.txt"] {
		return []ContractViolation{{Name: "list_direct_children", Err: fmt.Errorf("parent listing contains nested sub/b.txt: %v", entryNames(parent))}}
	}

	child, err := d.List(ctx, sub.ID)
	if err != nil {
		return []ContractViolation{{Name: "list_direct_children", Err: fmt.Errorf("list sub: %w", err)}}
	}
	// Directory listings converge asynchronously on some backends (quark
	// stamps created dirs with a delay); retry briefly before failing.
	for attempt := 0; len(child) != 1 || child[0].Name != "b.txt"; attempt++ {
		if attempt >= 2 {
			return []ContractViolation{{Name: "list_direct_children", Err: fmt.Errorf("sub listing = %v, want exactly [b.txt]", entryNames(child))}}
		}
		time.Sleep(time.Duration(attempt+1) * time.Second)
		child, err = d.List(ctx, sub.ID)
		if err != nil {
			return []ContractViolation{{Name: "list_direct_children", Err: fmt.Errorf("list sub: %w", err)}}
		}
	}
	return nil
}

// CheckReadOffsetsAndEOF verifies Read honors offset/size and signals EOF at
// the end of the file.
func CheckReadOffsetsAndEOF(ctx context.Context, d Driver) []ContractViolation {
	sb, err := NewContractSandbox(ctx, d)
	if err != nil {
		return []ContractViolation{{Name: "read_offsets_and_eof", Err: err}}
	}
	defer func() { _ = sb.Cleanup(context.WithoutCancel(ctx)) }()

	// Deterministic 1 MiB pattern: byte value cycles 0..255.
	size := int64(1 << 20)
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i)
	}
	entry, err := d.PutSource(ctx, UploadRequest{
		ParentID: sb.ID, Name: "data.bin", Source: NewBytesReadOnlyFileSource(payload),
	})
	if err != nil {
		return []ContractViolation{{Name: "read_offsets_and_eof", Err: fmt.Errorf("put data.bin: %w", err)}}
	}

	verify := func(offset, length int64) error {
		rc, err := d.Read(ctx, entry, offset, length)
		if err != nil {
			return fmt.Errorf("read(offset=%d,size=%d): %w", offset, length, err)
		}
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil {
			return fmt.Errorf("read(offset=%d,size=%d) body: %w", offset, length, err)
		}
		if int64(len(got)) != length {
			return fmt.Errorf("read(offset=%d,size=%d) returned %d bytes, want %d", offset, length, len(got), length)
		}
		for i, b := range got {
			want := byte((offset + int64(i)) % 256)
			if b != want {
				return fmt.Errorf("read(offset=%d) byte %d = %d, want %d", offset, i, b, want)
			}
		}
		return nil
	}

	var violations []ContractViolation
	for _, v := range []struct {
		offset, length int64
	}{
		{0, 64 * 1024},
		{256 * 1024, 32 * 1024},
		{size - 4096, 4096},
	} {
		if err := verify(v.offset, v.length); err != nil {
			violations = append(violations, ContractViolation{Name: "read_offsets_and_eof", Err: err})
		}
	}

	// EOF: a read starting at the end of the file returns an empty stream.
	rc, err := d.Read(ctx, entry, size, 1)
	if err != nil {
		violations = append(violations, ContractViolation{Name: "read_offsets_and_eof", Err: fmt.Errorf("read at EOF: %w", err)})
		return violations
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		violations = append(violations, ContractViolation{Name: "read_offsets_and_eof", Err: fmt.Errorf("read at EOF body: %w", err)})
		return violations
	}
	if len(got) != 0 {
		violations = append(violations, ContractViolation{Name: "read_offsets_and_eof", Err: fmt.Errorf("read at EOF returned %d bytes, want empty", len(got))})
	}
	return violations
}

// CheckNotFoundClassification verifies that deleting an entry and then
// reading it yields an error classified as drive.ErrNotFound.
func CheckNotFoundClassification(ctx context.Context, d Driver) []ContractViolation {
	sb, err := NewContractSandbox(ctx, d)
	if err != nil {
		return []ContractViolation{{Name: "not_found_classification", Err: err}}
	}
	defer func() { _ = sb.Cleanup(context.WithoutCancel(ctx)) }()

	entry, err := d.PutSource(ctx, UploadRequest{
		ParentID: sb.ID, Name: "gone.txt", Source: NewBytesReadOnlyFileSource([]byte("bye")),
	})
	if err != nil {
		return []ContractViolation{{Name: "not_found_classification", Err: fmt.Errorf("put gone.txt: %w", err)}}
	}
	if err := d.Remove(ctx, entry); err != nil {
		return []ContractViolation{{Name: "not_found_classification", Err: fmt.Errorf("remove gone.txt: %w", err)}}
	}

	// Deletion converges asynchronously on most backends; retry briefly.
	// yun139 can take several seconds to stop serving a deleted file.
	const maxAttempts = 5
	for attempt := range maxAttempts {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		rc, err := d.Read(ctx, entry, 0, 1)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			if rc != nil {
				rc.Close()
			}
			if attempt == maxAttempts-1 {
				return []ContractViolation{{Name: "not_found_classification", Err: fmt.Errorf("read after remove = %v, want %w", err, ErrNotFound)}}
			}
			continue
		}
		rc.Close()
	}
	return []ContractViolation{{Name: "not_found_classification", Err: fmt.Errorf("read after remove still succeeds after %d attempts", maxAttempts)}}
}

// CheckContextCancellation verifies that a cancelled context fails fast with
// an error instead of hanging or ignoring the cancellation.
func CheckContextCancellation(ctx context.Context, d Driver) []ContractViolation {
	sb, err := NewContractSandbox(ctx, d)
	if err != nil {
		return []ContractViolation{{Name: "context_cancellation", Err: err}}
	}
	defer func() { _ = sb.Cleanup(context.WithoutCancel(ctx)) }()

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	start := time.Now()
	_, err = d.List(cancelled, sb.ID)
	elapsed := time.Since(start)
	if err == nil {
		return []ContractViolation{{Name: "context_cancellation", Err: fmt.Errorf("list on cancelled context returned nil error")}}
	}
	if elapsed > 2*time.Second {
		return []ContractViolation{{Name: "context_cancellation", Err: fmt.Errorf("list on cancelled context took %s, want fast failure", elapsed)}}
	}
	return nil
}

// CheckUploadReturnsFinalEntry verifies PutSource returns a finalized Entry
// with a non-empty id, the requested name, and the uploaded size.
func CheckUploadReturnsFinalEntry(ctx context.Context, d Driver) []ContractViolation {
	sb, err := NewContractSandbox(ctx, d)
	if err != nil {
		return []ContractViolation{{Name: "upload_returns_final_entry", Err: err}}
	}
	defer func() { _ = sb.Cleanup(context.WithoutCancel(ctx)) }()

	payload := []byte("final entry payload 0123456789")
	entry, err := d.PutSource(ctx, UploadRequest{
		ParentID: sb.ID, Name: "final.bin", Source: NewBytesReadOnlyFileSource(payload),
	})
	if err != nil {
		return []ContractViolation{{Name: "upload_returns_final_entry", Err: fmt.Errorf("put final.bin: %w", err)}}
	}
	if entry.ID == "" {
		return []ContractViolation{{Name: "upload_returns_final_entry", Err: fmt.Errorf("PutSource returned entry with empty ID")}}
	}
	if entry.Name != "final.bin" {
		return []ContractViolation{{Name: "upload_returns_final_entry", Err: fmt.Errorf("PutSource returned name %q, want %q", entry.Name, "final.bin")}}
	}
	if entry.Size != int64(len(payload)) {
		return []ContractViolation{{Name: "upload_returns_final_entry", Err: fmt.Errorf("PutSource returned size %d, want %d", entry.Size, len(payload))}}
	}
	return nil
}

// RunBehaviorChecks executes every behavioral contract check against the
// driver, each in its own sandbox.
func RunBehaviorChecks(ctx context.Context, d Driver) []ContractViolation {
	var violations []ContractViolation
	for _, check := range []func(context.Context, Driver) []ContractViolation{
		CheckListDirectChildren,
		CheckReadOffsetsAndEOF,
		CheckNotFoundClassification,
		CheckContextCancellation,
		CheckUploadReturnsFinalEntry,
	} {
		violations = append(violations, check(ctx, d)...)
	}
	return violations
}
