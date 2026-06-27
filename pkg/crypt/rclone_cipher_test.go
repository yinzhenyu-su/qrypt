package crypt

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/crypto/nacl/secretbox"
)

func TestRcloneCipher_KeyDerivation(t *testing.T) {
	password := "testpassword"
	salt := "testsalt"
	c, err := NewRcloneCipher(password, salt)
	if err != nil {
		t.Fatalf("Failed to create cipher: %v", err)
	}

	if len(c.dataKey) != 32 || len(c.nameKey) != 32 || len(c.nameTweak) != 16 {
		t.Errorf("Internal keys have incorrect length")
	}
}

func TestRcloneCipher_FilenameEncryption(t *testing.T) {
	password := "password"
	salt := ""

	for _, enc := range []string{"base32", "base64"} {
		c, _ := NewRcloneCipher(password, salt, enc)

		testNames := []string{
			"README.md",
			"电影.mp4",
			"a",
			"very_long_filename_that_exceeds_multiple_blocks_of_eme_encryption.txt",
		}

		for _, name := range testNames {
			encrypted := c.EncryptSegment(name)
			decrypted, err := c.DecryptSegment(encrypted)
			if err != nil {
				t.Errorf("[%s] Decryption failed for %s: %v", enc, name, err)
				continue
			}
			if decrypted != name {
				t.Errorf("[%s] Name mismatch! Original: %s, Decrypted: %s", enc, name, decrypted)
			}
		}
	}
}

func TestRcloneCipher_ObfuscateMode(t *testing.T) {
	password := "password"
	salt := ""
	c, _ := NewRcloneCipher(password, salt, "base32", "obfuscate")

	testNames := []string{
		"README.md",
		"电影.mp4",
		"a",
		"hello world",
		"测试中文文件名",
		"very_long_filename_that_exceeds_multiple_blocks.txt",
	}

	for _, name := range testNames {
		encrypted := c.EncryptSegment(name)
		if len(encrypted) > len(name)+10 {
			t.Errorf("[obfuscate] output too long for %s: %d vs %d", name, len(encrypted), len(name))
		}
		decrypted, err := c.DecryptSegment(encrypted)
		if err != nil {
			t.Errorf("[obfuscate] decryption failed for %s: %v", name, err)
			continue
		}
		if decrypted != name {
			t.Errorf("[obfuscate] name mismatch! Original: %s, Decrypted: %s", name, decrypted)
		}
	}
}

func TestRcloneCipher_Obfuscate_EdgeCases(t *testing.T) {
	c, _ := NewRcloneCipher("password", "", "base32", "obfuscate")

	t.Run("empty string", func(t *testing.T) {
		enc := c.EncryptSegment("")
		if enc != "" {
			t.Errorf("expected empty, got %q", enc)
		}
		dec, err := c.DecryptSegment("")
		if err != nil || dec != "" {
			t.Errorf("expected empty, got %q err=%v", dec, err)
		}
	})

	t.Run("single character", func(t *testing.T) {
		name := "a"
		enc := c.EncryptSegment(name)
		dec, err := c.DecryptSegment(enc)
		if err != nil || dec != name {
			t.Errorf("single char: %q -> %q err=%v", name, dec, err)
		}
	})

	t.Run("special characters", func(t *testing.T) {
		names := []string{
			"file with spaces.txt",
			"file_with_underscores.js",
			"file.with.dots",
			"hello!world",
			"test!!double",
			"a!!b!!c",
		}
		for _, name := range names {
			enc := c.EncryptSegment(name)
			dec, err := c.DecryptSegment(enc)
			if err != nil || dec != name {
				t.Errorf("special chars: %q -> %q err=%v", name, dec, err)
			}
		}
	})

	t.Run("numbers only", func(t *testing.T) {
		name := "12345"
		enc := c.EncryptSegment(name)
		dec, err := c.DecryptSegment(enc)
		if err != nil || dec != name {
			t.Errorf("numbers: %q -> %q err=%v", name, dec, err)
		}
	})

	t.Run("uppercase and lowercase", func(t *testing.T) {
		names := []string{
			"README.md",
			"Index.HTML",
			"Makefile",
			".gitignore",
		}
		for _, name := range names {
			enc := c.EncryptSegment(name)
			dec, err := c.DecryptSegment(enc)
			if err != nil || dec != name {
				t.Errorf("case: %q -> %q err=%v", name, dec, err)
			}
		}
	})

	t.Run("very long name", func(t *testing.T) {
		name := strings.Repeat("文件名", 50)
		enc := c.EncryptSegment(name)
		if len(enc) > len(name)+10 {
			t.Errorf("too long: %d vs %d", len(enc), len(name))
		}
		dec, err := c.DecryptSegment(enc)
		if err != nil || dec != name {
			t.Errorf("long name: decryption failed err=%v", err)
		}
	})


}

func TestRcloneCipher_Obfuscate_Determinism(t *testing.T) {
	c1, _ := NewRcloneCipher("password", "", "base32", "obfuscate")
	c2, _ := NewRcloneCipher("password", "", "base32", "obfuscate")

	names := []string{"test.txt", "电影.mp4", "a"}
	for _, name := range names {
		enc1 := c1.EncryptSegment(name)
		enc2 := c2.EncryptSegment(name)
		if enc1 != enc2 {
			t.Errorf("determinism failed for %q: %q vs %q", name, enc1, enc2)
		}
	}
}

func TestRcloneCipher_Obfuscate_KeySensitivity(t *testing.T) {
	c1, _ := NewRcloneCipher("password1", "", "base32", "obfuscate")
	c2, _ := NewRcloneCipher("password2", "", "base32", "obfuscate")

	names := []string{"test.txt", "电影.mp4"}
	for _, name := range names {
		enc1 := c1.EncryptSegment(name)
		enc2 := c2.EncryptSegment(name)
		if enc1 == enc2 {
			t.Errorf("different keys should produce different output for %q", name)
		}
	}
}

func TestRcloneCipher_New_OptDefaults(t *testing.T) {
	c, err := NewRcloneCipher("p", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.filenameEncoding != "base32" {
		t.Errorf("expected base32, got %s", c.filenameEncoding)
	}
	if c.filenameEncryption != "standard" {
		t.Errorf("expected standard, got %s", c.filenameEncryption)
	}

	c2, _ := NewRcloneCipher("p", "", "base64")
	if c2.filenameEncoding != "base64" || c2.filenameEncryption != "standard" {
		t.Errorf("unexpected defaults: enc=%s mode=%s", c2.filenameEncoding, c2.filenameEncryption)
	}

	c3, _ := NewRcloneCipher("p", "", "base64", "obfuscate")
	if c3.filenameEncoding != "base64" || c3.filenameEncryption != "obfuscate" {
		t.Errorf("unexpected: enc=%s mode=%s", c3.filenameEncoding, c3.filenameEncryption)
	}

	c4, _ := NewRcloneCipher("p", "", "", "")
	if c4.filenameEncoding != "base32" || c4.filenameEncryption != "standard" {
		t.Errorf("empty opts should become defaults: enc=%s mode=%s", c4.filenameEncoding, c4.filenameEncryption)
	}
}

func TestRcloneCipher_OffMode(t *testing.T) {
	password := "password"
	salt := ""
	c, _ := NewRcloneCipher(password, salt, "base32", "off")

	testNames := []string{
		"README.md",
		"电影.mp4",
		"hello world",
	}

	for _, name := range testNames {
		encrypted := c.EncryptSegment(name)
		if encrypted != name {
			t.Errorf("[off] encrypt should be no-op, got %s", encrypted)
		}
		decrypted, err := c.DecryptSegment(encrypted)
		if err != nil {
			t.Errorf("[off] decrypt failed: %v", err)
		}
		if decrypted != name {
			t.Errorf("[off] name mismatch: %s vs %s", name, decrypted)
		}
	}
}

func TestRcloneCipher_CrossEncodingDecrypt(t *testing.T) {
	password := "password"
	salt := ""
	testNames := []string{
		"README.md",
		"电影.mp4",
		"a",
	}

	c64, _ := NewRcloneCipher(password, salt, "base64")
	c32, _ := NewRcloneCipher(password, salt, "base32")

	for _, name := range testNames {
		encrypted := c64.EncryptSegment(name)
		decrypted, err := c32.DecryptSegment(encrypted)
		if err != nil {
			t.Errorf("[base32 from base64] Decryption failed for %s: %v", name, err)
			continue
		}
		if decrypted != name {
			t.Errorf("[base32 from base64] Name mismatch! Original: %s, Decrypted: %s", name, decrypted)
		}
	}

	for _, name := range testNames {
		encrypted := c32.EncryptSegment(name)
		decrypted, err := c64.DecryptSegment(encrypted)
		if err != nil {
			t.Errorf("[base64 from base32] Decryption failed for %s: %v", name, err)
			continue
		}
		if decrypted != name {
			t.Errorf("[base64 from base32] Name mismatch! Original: %s, Decrypted: %s", name, decrypted)
		}
	}
}

func TestRcloneCipher_BlockDecryption(t *testing.T) {
	c, _ := NewRcloneCipher("password", "")

	var fileNonce [24]byte
	copy(fileNonce[:], []byte("123456789012345678901234"))

	plaintext := make([]byte, BlockDataSize)
	for i := range plaintext {
		plaintext[i] = byte(i % 256)
	}

	nonce := fileNonce

	fromSecretbox := secretbox.Seal(nil, plaintext, &nonce, &c.dataKey)

	gotPlaintext, err := c.DecryptBlock(fromSecretbox, 0, fileNonce)
	if err != nil {
		t.Fatalf("DecryptBlock failed: %v", err)
	}

	if !bytes.Equal(plaintext, gotPlaintext) {
		t.Errorf("Plaintext mismatch after block decryption")
	}
}

func TestRcloneCipher_BlockEncryption(t *testing.T) {
	c, _ := NewRcloneCipher("password", "")

	var fileNonce [24]byte
	copy(fileNonce[:], []byte("123456789012345678901234"))

	plaintext := []byte("hello rclone")

	ciphertext, err := c.EncryptBlock(plaintext, 5, fileNonce)
	if err != nil {
		t.Fatalf("EncryptBlock failed: %v", err)
	}

	gotPlaintext, err := c.DecryptBlock(ciphertext, 5, fileNonce)
	if err != nil {
		t.Fatalf("DecryptBlock failed: %v", err)
	}

	if !bytes.Equal(plaintext, gotPlaintext) {
		t.Errorf("Plaintext mismatch! Expected: %s, Got: %s", string(plaintext), string(gotPlaintext))
	}
}

func TestSizeMapping(t *testing.T) {
	c, _ := NewRcloneCipher("p", "")

	testSizes := []int64{0, 1, 100, BlockDataSize, BlockDataSize + 1, 10 * 1024 * 1024}
	for _, size := range testSizes {
		enc := c.EncryptedSize(size)
		dec, err := c.DecryptedSize(enc)
		if err != nil {
			t.Errorf("DecryptedSize failed for size %d: %v", size, err)
			continue
		}
		if dec != size {
			t.Errorf("Size mismatch! Original: %d, Decrypted: %d (Encrypted was %d)", size, dec, enc)
		}
	}
}
