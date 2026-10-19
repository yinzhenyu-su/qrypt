package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleChecksums = `# qrypt checksums
990d2509b1481fd067c0a6181ecb34c7f03adb67fca02af76ba06d8698e6b128  qrypt_0.5.3_darwin_amd64.tar.gz
9c13ae0fd05f0ab7797ecd0ae03f1ee5caf8ed8d6f1c8cff1cedffa6a0c332b5  qrypt_0.5.3_darwin_arm64.tar.gz

2fb70a2bd0e798b94dc9fa7d978d5ea877aa2a8447610ddfd7819ce66817a054 *qrypt_0.5.3_linux_amd64.tar.gz
`

func TestParseChecksums(t *testing.T) {
	t.Parallel()
	sums, err := ParseChecksums(strings.NewReader(sampleChecksums))
	if err != nil {
		t.Fatalf("ParseChecksums error: %v", err)
	}
	if got := sums["qrypt_0.5.3_darwin_arm64.tar.gz"]; got != "9c13ae0fd05f0ab7797ecd0ae03f1ee5caf8ed8d6f1c8cff1cedffa6a0c332b5" {
		t.Fatalf("darwin_arm64 checksum = %q", got)
	}
	// A binary-mode "*" prefix must be stripped.
	if got := sums["qrypt_0.5.3_linux_amd64.tar.gz"]; got != "2fb70a2bd0e798b94dc9fa7d978d5ea877aa2a8447610ddfd7819ce66817a054" {
		t.Fatalf("linux_amd64 checksum = %q", got)
	}
	if _, ok := sums["#"]; ok {
		t.Fatal("comment line leaked into the checksum map")
	}
}

func TestVerifyChecksumMatchAndMismatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.tar.gz")
	content := []byte("payload")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	good := hex.EncodeToString(digest[:])

	if err := VerifyChecksum(path, good); err != nil {
		t.Fatalf("VerifyChecksum(match) error: %v", err)
	}
	if err := VerifyChecksum(path, strings.ToUpper(good)); err != nil {
		t.Fatalf("VerifyChecksum must accept an uppercase digest: %v", err)
	}
	err := VerifyChecksum(path, strings.Repeat("0", 64))
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("VerifyChecksum(mismatch) error = %v, want ErrChecksumMismatch", err)
	}
}

func TestHashFileMatchesSum(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// sha256("abc")
	if got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("HashFile = %q", got)
	}
}
