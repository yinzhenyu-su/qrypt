package selfupdate

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ParseChecksums reads a GoReleaser-style checksum file: one
// "<hex digest>  <asset name>" line per asset. Blank lines and comments are
// ignored. The returned map is keyed by asset name with lowercase digests.
func ParseChecksums(r io.Reader) (map[string]string, error) {
	sums := make(map[string]string)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		sums[name] = strings.ToLower(fields[0])
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read checksums: %w", err)
	}
	return sums, nil
}

// HashFile returns the lowercase hex sha256 digest of the file at path.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyChecksum compares the sha256 of the file at path against want.
func VerifyChecksum(path, want string) error {
	got, err := HashFile(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, strings.TrimSpace(want)) {
		return fmt.Errorf("%w: %s", ErrChecksumMismatch, filepath.Base(path))
	}
	return nil
}
