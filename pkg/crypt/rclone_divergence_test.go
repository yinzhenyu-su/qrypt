package crypt

import (
	"strings"
	"testing"
)

// This file pins the places where this implementation intentionally differs
// from rclone v1.73.3. These are behavior documentation, not compatibility
// claims: if one of them starts failing it means the behavior changed, and the
// divergence note below must be updated to keep the record accurate.

// TestRcloneDivergence_LongNameSegmentation documents that names longer than
// 1000 bytes are segmented here but not by rclone.
//
// rclone v1.73.3 (backend/crypt/cipher.go) encrypts an entire path segment in
// a single EME transform and rejects decoded ciphertexts over 2048 bytes
// (decryptSegment: ErrorTooLongAfterDecode). It has no length-based splitting.
// This implementation splits names longer than maxSegmentPlainLen (1000 bytes)
// into independently encrypted segments joined with the ";" separator, which
// rclone cannot decrypt. Because rclone itself can only store names whose
// encrypted form fits the backend limit (255 bytes on most filesystems), both
// implementations agree on every name that can realistically exist on a
// backend; the divergence only matters for exotic backends with huge name
// limits.
func TestRcloneDivergence_LongNameSegmentation(t *testing.T) {
	c, _ := NewRcloneCipher("p", "s")
	long := strings.Repeat("a", 1200) + ".txt"
	enc := c.EncryptSegment(long)
	if !strings.Contains(enc, segmentSeparator) {
		t.Fatalf("expected segmented output for %d-char name, got %q", len(long), enc)
	}
	back, err := c.DecryptSegment(enc)
	if err != nil || back != long {
		t.Fatalf("segmented round-trip: %q err=%v", back, err)
	}
	for _, n := range []string{strings.Repeat("b", 1000), "short.txt"} {
		if e := c.EncryptSegment(n); strings.Contains(e, segmentSeparator) {
			t.Errorf("unexpected segmentation for %d-char name %q", len(n), e)
		}
	}
}

// TestRcloneDivergence_OffMode documents that filename_encryption=off is a
// no-op here, whereas rclone appends a ".bin" suffix (configurable via the
// suffix option) to stored names and strips it again on read
// (cipher.go EncryptFileName/DecryptFileName).
func TestRcloneDivergence_OffMode(t *testing.T) {
	c, _ := NewRcloneCipher("p", "", "base32", "off")
	if got := c.EncryptSegment("README.md"); got != "README.md" {
		t.Fatalf("expected no-op encrypt, got %q", got)
	}
	if got, err := c.DecryptSegment("README.md"); err != nil || got != "README.md" {
		t.Fatalf("expected no-op decrypt, got %q err=%v", got, err)
	}
}

// TestRcloneDivergence_VersionSuffix documents that rclone strips a trailing
// "-vYYYY-MM-DD-HHMMSS-###" version marker from the final path segment before
// encrypting it (lib/version) and re-appends the marker afterwards, while this
// implementation encrypts the name verbatim.
//
// Captured from rclone v1.73.3: the file "photo-v2024-01-02-030405-006.jpg"
// encrypts to "fernkggb3e4oe2uvd55390seoc-v2024-01-02-030405-006". Here the
// whole name is encrypted, so the output is the 26-character EME of the full
// string and contains no plaintext marker.
func TestRcloneDivergence_VersionSuffix(t *testing.T) {
	c, _ := NewRcloneCipher("testpassword", "testsalt")
	name := "photo-v2024-01-02-030405-006.jpg"
	got := c.EncryptSegment(name)
	if strings.Contains(got, "-v2024-01-02-030405-006") {
		t.Fatalf("implementation unexpectedly handled the rclone version marker: %s", got)
	}
	back, err := c.DecryptSegment(got)
	if err != nil || back != name {
		t.Fatalf("round-trip failed: %q err=%v", back, err)
	}
	t.Logf("rclone v1.73.3 encrypts this name as fernkggb3e4oe2uvd55390seoc-v2024-01-02-030405-006; this implementation produces %s", got)
}

// TestRcloneDivergence_Base32768 documents that rclone supports the
// base32768 filename encoding (for backends that count UTF-16 code units, e.g.
// Onedrive/Dropbox) while this implementation only accepts base32 and base64:
// the config layer rejects it, and if the raw constructor is asked for it the
// output silently falls back to base32 rather than producing base32768 text.
func TestRcloneDivergence_Base32768(t *testing.T) {
	cfg := Config{Password: "p", FileNameEncoding: "base32768"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected base32768 to be rejected by Validate")
	}
	c, _ := NewRcloneCipher("p", "", "base32768")
	enc := c.EncryptSegment("1")
	for _, r := range enc {
		if r > 0x7f {
			t.Fatalf("base32768 produced non-ASCII output %q; rclone would emit CJK characters here", enc)
			break
		}
	}
}

// TestRcloneDivergence_PathLevelAPIs documents that the segment-level Cipher
// API leaves path splitting, directory-name encryption and version handling to
// callers; rclone's Cipher handles full paths (strings.Split on "/" in
// encryptFileName/decryptFileName). A name containing "/" is therefore
// encrypted here as a single segment with the "/" inside it.
func TestRcloneDivergence_PathLevelAPI(t *testing.T) {
	c, _ := NewRcloneCipher("testpassword", "testsalt")
	name := "a/b.txt"
	enc := c.EncryptSegment(name)
	if strings.Contains(enc, "/") {
		t.Fatalf("expected no path splitting at segment level, got %q", enc)
	}
	if back, err := c.DecryptSegment(enc); err != nil || back != name {
		t.Fatalf("round-trip failed: %q err=%v", back, err)
	}
	// rclone's encryptFileName would split "a/b.txt" on "/" and encrypt the
	// two segments separately; the segment-level API has no notion of paths.
}
