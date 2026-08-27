package crypt

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rclonePath returns the rclone binary path or skips the test if it is not
// installed. These live tests run only when the real rclone binary is
// available; the golden-vector tests in rclone_compat_test.go cover the same
// ground without it.
func rclonePath(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone binary not found in PATH; skipping live interop test")
	}
	return path
}

// rcloneRun runs rclone with a time-bounded context and returns combined
// output.
func rcloneRun(t *testing.T, ctx context.Context, path, config string, args ...string) []byte {
	t.Helper()
	fullArgs := append([]string{"--config", config, "--log-level", "ERROR"}, args...)
	cmd := exec.CommandContext(ctx, path, fullArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rclone %s: %v\n%s", strings.Join(fullArgs, " "), err, out)
	}
	return out
}

// writeRcloneCryptConfig writes an rclone config file whose remote obscures
// password and password2 with THIS implementation's ObscureRcloneConfigValue,
// so rclone must reveal them correctly for any subsequent command to work.
// This cross-tests the obscure format in both directions.
func writeRcloneCryptConfig(t *testing.T, remote, vault, password, salt, encoding, mode string) string {
	t.Helper()
	pw, err := ObscureRcloneConfigValue(password)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ObscureRcloneConfigValue(salt)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("[%s]\ntype = crypt\nremote = %s\npassword = %s\npassword2 = %s\nfilename_encoding = %s\nfilename_encryption = %s\n",
		remote, vault, pw, s, encoding, mode)
	path := filepath.Join(t.TempDir(), "rclone.conf")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// interopPlaintext returns the deterministic byte pattern used by the live
// data tests, matching the fixture corpus generator.
func interopPlaintext(size int) []byte {
	row := []byte("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ\n")
	out := make([]byte, size)
	for i := range out {
		out[i] = row[i%len(row)]
	}
	return out
}

// TestRcloneInteropFilenames verifies that names encrypted by this package are
// byte-identical to the names rclone v1.73.3 stores, and that rclone
// cryptdecode recovers the plaintext from them.
func TestRcloneInteropFilenames(t *testing.T) {
	rc := rclonePath(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	vault := filepath.Join(t.TempDir(), "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	config := writeRcloneCryptConfig(t, "qcrypt", vault, "testpassword", "testsalt", "base32", "standard")
	cp, err := NewRcloneCipher("testpassword", "testsalt")
	if err != nil {
		t.Fatal(err)
	}

	names := []string{
		"README.md", "电影.mp4", "a", "hello world.txt", ".hidden",
		"file-with-dashes_underscores.js", "1234567890", "quote'apostrophe.txt",
		"测试中文文件名.md", "emoji 🚀 test.png", "semi;colon.txt",
	}

	// Upload each name through the real rclone crypt remote.
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(plain, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rcloneRun(t, ctx, rc, config, "copy", plain, "qcrypt:")

	vaultNames := map[string]bool{}
	ents, err := os.ReadDir(vault)
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range ents {
		vaultNames[ent.Name()] = true
	}
	for _, name := range names {
		our := cp.EncryptSegment(name)
		if !vaultNames[our] {
			t.Errorf("rclone stored %q; expected our EncryptSegment(%q) = %q to be stored", name, name, our)
		}
		// rclone cryptdecode must accept our encrypted name and print the
		// plaintext back.
		out := rcloneRun(t, ctx, rc, config, "cryptdecode", "qcrypt:", our)
		if !strings.Contains(strings.TrimSpace(string(out)), name) {
			t.Errorf("rclone cryptdecode(%q) = %q, want it to contain %q", our, strings.TrimSpace(string(out)), name)
		}
	}
}

// TestRcloneInteropQryptToRcloneData encrypts plaintext with this package
// (fixed nonce) and verifies the real rclone binary decrypts the resulting
// file to the original plaintext, across the block-size boundaries.
func TestRcloneInteropQryptToRcloneData(t *testing.T) {
	rc := rclonePath(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	sizes := []int{0, 1, 100, BlockDataSize, BlockDataSize + 1, 200000}
	type conf struct {
		encoding, mode string
	}
	for _, c := range []conf{{"base32", "standard"}, {"base64", "standard"}, {"base32", "obfuscate"}} {
		name := fmt.Sprintf("%s/%s", c.encoding, c.mode)
		t.Run(name, func(t *testing.T) {
			vault := filepath.Join(t.TempDir(), "vault")
			if err := os.MkdirAll(vault, 0o755); err != nil {
				t.Fatal(err)
			}
			config := writeRcloneCryptConfig(t, "qcrypt", vault, "testpassword", "testsalt", c.encoding, c.mode)
			cp, err := NewRcloneCipher("testpassword", "testsalt", c.encoding, c.mode)
			if err != nil {
				t.Fatal(err)
			}
			var nonce [FileNonceSize]byte
			for i := range nonce {
				nonce[i] = byte(i)
			}
			for _, size := range sizes {
				plain := interopPlaintext(size)
				ciphertext, err := io.ReadAll(NewEncryptingReader(bytes.NewReader(plain), cp, nonce, int64(size)))
				if err != nil {
					t.Fatal(err)
				}
				if got := cp.EncryptedSize(int64(size)); got != int64(len(ciphertext)) {
					t.Fatalf("size %d: EncryptedSize=%d len=%d", size, got, len(ciphertext))
				}
				file := fmt.Sprintf("file-%d.bin", size)
				if err := os.WriteFile(filepath.Join(vault, cp.EncryptSegment(file)), ciphertext, 0o644); err != nil {
					t.Fatal(err)
				}

				// rclone must decrypt the whole file to the original bytes.
				out := rcloneRun(t, ctx, rc, config, "cat", "qcrypt:"+file)
				if !bytes.Equal(out, plain) {
					t.Errorf("size %d: rclone decrypted %d bytes, plaintext has %d", size, len(out), len(plain))
				}
				// the on-disk ciphertext must be exactly what we wrote.
				if st, err := os.Stat(filepath.Join(vault, cp.EncryptSegment(file))); err != nil {
					t.Fatal(err)
				} else if st.Size() != int64(len(ciphertext)) {
					t.Errorf("size %d: vault file is %d bytes, want %d", size, st.Size(), len(ciphertext))
				}
			}
		})
	}
}

// TestRcloneInteropRcloneToQryptData encrypts plaintext with the real rclone
// binary and verifies this package decrypts it, including a byte-identical
// re-encryption using the nonce rclone stored in the header (i.e. block
// layout, secretbox addressing and size accounting all match exactly).
func TestRcloneInteropRcloneToQryptData(t *testing.T) {
	rc := rclonePath(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	vault := filepath.Join(t.TempDir(), "vault")
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	config := writeRcloneCryptConfig(t, "qcrypt", vault, "testpassword", "testsalt", "base32", "standard")
	cp, err := NewRcloneCipher("testpassword", "testsalt")
	if err != nil {
		t.Fatal(err)
	}

	sizes := []int{0, 100, BlockDataSize, BlockDataSize + 1, 200000}
	plainBySize := map[int][]byte{}
	for _, size := range sizes {
		data := interopPlaintext(size)
		plainBySize[size] = data
		if err := os.WriteFile(filepath.Join(plain, fmt.Sprintf("f-%d.bin", size)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rcloneRun(t, ctx, rc, config, "copy", plain, "qcrypt:")

	ents, err := os.ReadDir(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != len(sizes) {
		t.Fatalf("vault has %d files, want %d", len(ents), len(sizes))
	}
	for _, ent := range ents {
		name := ent.Name()
		ciphertext, err := os.ReadFile(filepath.Join(vault, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(ciphertext[:FileMagicSize]) != FileMagic {
			t.Fatalf("%s: bad magic", name)
		}
		var nonce [FileNonceSize]byte
		copy(nonce[:], ciphertext[FileMagicSize:FileHeaderSize])
		got, err := io.ReadAll(NewDecryptingReader(bytes.NewReader(ciphertext[FileHeaderSize:]), cp, nonce))
		if err != nil {
			t.Fatalf("%s: decrypt: %v", name, err)
		}
		plainSize, err := cp.DecryptedSize(int64(len(ciphertext)))
		if err != nil {
			t.Fatalf("%s: DecryptedSize: %v", name, err)
		}
		want := plainBySize[int(plainSize)]
		if want == nil {
			t.Fatalf("%s: unexpected decrypted size %d", name, plainSize)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: decrypted content mismatch (%d bytes)", name, len(got))
		}

		// Re-encrypt with the nonce rclone chose: must reproduce rclone's
		// ciphertext byte for byte, proving the block format is identical.
		reenc, err := io.ReadAll(NewEncryptingReader(bytes.NewReader(want), cp, nonce, int64(len(want))))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(reenc, ciphertext) {
			t.Errorf("%s: re-encryption with rclone's nonce differs from rclone's ciphertext", name)
		}
	}
}
