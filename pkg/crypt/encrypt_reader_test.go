package crypt

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestNewEncryptingReader(t *testing.T) {
	c, _ := NewRcloneCipher("password", "salt")
	var nonce [24]byte
	r := NewEncryptingReader(strings.NewReader("hello"), c, nonce, 5)
	if r == nil {
		t.Fatal("expected non-nil reader")
	}
}

func TestEncryptingReader_Read_Empty(t *testing.T) {
	c, _ := NewRcloneCipher("password", "salt")
	var nonce [24]byte
	r := NewEncryptingReader(strings.NewReader(""), c, nonce, 0)

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	// Should produce only the header (no data blocks for empty plaintext)
	if len(out) < FileHeaderSize {
		t.Errorf("output too short: %d < header %d", len(out), FileHeaderSize)
	}
	if string(out[:FileMagicSize]) != FileMagic {
		t.Errorf("missing file magic at header start")
	}
	_ = out[:FileHeaderSize] // verify at least header exists
}

func TestEncryptingReader_SingleBlockRoundTrip(t *testing.T) {
	c, _ := NewRcloneCipher("password", "salt")
	var nonce [24]byte
	// Use crypto/rand to fill nonce for production, but deterministic for test
	copy(nonce[:], []byte("012345678901234567890123"))

	plaintext := []byte("hello rclone encrypt reader")
	r := NewEncryptingReader(bytes.NewReader(plaintext), c, nonce, int64(len(plaintext)))

	encrypted, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	// Verify header
	if len(encrypted) < FileHeaderSize {
		t.Fatalf("encrypted data too short: %d", len(encrypted))
	}
	if string(encrypted[:FileMagicSize]) != FileMagic {
		t.Errorf("bad magic")
	}
	if !bytes.Equal(encrypted[FileMagicSize:FileHeaderSize], nonce[:]) {
		t.Errorf("nonce mismatch in header")
	}

	// Verify block: strip header, decrypt block
	blockData := encrypted[FileHeaderSize:]
	decrypted, err := c.DecryptBlock(blockData, 0, nonce)
	if err != nil {
		t.Fatalf("DecryptBlock failed: %v", err)
	}
	if !bytes.Equal(plaintext, decrypted) {
		t.Errorf("round-trip mismatch: got %q, want %q", string(decrypted), string(plaintext))
	}
}

func TestEncryptingReader_MultiBlock(t *testing.T) {
	c, _ := NewRcloneCipher("password", "salt")
	var nonce [24]byte
	copy(nonce[:], []byte("abcdefghijklmnopqrstuvwx"))

	// Three full blocks of data
	plaintext := make([]byte, BlockDataSize*3)
	for i := range plaintext {
		plaintext[i] = byte(i % 251)
	}

	r := NewEncryptingReader(bytes.NewReader(plaintext), c, nonce, int64(len(plaintext)))

	encrypted, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if len(encrypted) < FileHeaderSize {
		t.Fatalf("encrypted data too short: %d", len(encrypted))
	}
	if string(encrypted[:FileMagicSize]) != FileMagic {
		t.Errorf("bad magic")
	}

	// Decrypt each block
	blockOffset := FileHeaderSize
	blockIndex := uint64(0)
	decryptedTotal := make([]byte, 0, len(plaintext))
	for blockOffset < len(encrypted) {
		blockEnd := blockOffset + BlockSize
		if blockEnd > len(encrypted) {
			blockEnd = len(encrypted)
		}
		blockData := encrypted[blockOffset:blockEnd]
		decBlock, err := c.DecryptBlock(blockData, blockIndex, nonce)
		if err != nil {
			t.Fatalf("DecryptBlock %d failed: %v", blockIndex, err)
		}
		decryptedTotal = append(decryptedTotal, decBlock...)
		blockOffset += len(blockData)
		blockIndex++
	}

	if !bytes.Equal(plaintext, decryptedTotal) {
		t.Errorf("multi-block round-trip mismatch")
	}
}

func TestEncryptingReader_PartialBlock(t *testing.T) {
	c, _ := NewRcloneCipher("password", "salt")
	var nonce [24]byte
	copy(nonce[:], []byte("partialblocktest0001"))

	// Less than one full block
	plaintext := []byte("short")
	r := NewEncryptingReader(bytes.NewReader(plaintext), c, nonce, int64(len(plaintext)))

	encrypted, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	blockData := encrypted[FileHeaderSize:]
	decrypted, err := c.DecryptBlock(blockData, 0, nonce)
	if err != nil {
		t.Fatalf("DecryptBlock failed: %v", err)
	}
	if !bytes.Equal(plaintext, decrypted) {
		t.Errorf("partial block round-trip mismatch: got %q, want %q", string(decrypted), string(plaintext))
	}
}

func TestEncryptingReader_ReadSmallBuffer(t *testing.T) {
	c, _ := NewRcloneCipher("password", "salt")
	var nonce [24]byte
	copy(nonce[:], []byte("smallbufferrtest1234"))

	plaintext := make([]byte, BlockDataSize*2)
	for i := range plaintext {
		plaintext[i] = byte(i % 256)
	}

	r := NewEncryptingReader(bytes.NewReader(plaintext), c, nonce, int64(len(plaintext)))

	// Read in tiny chunks to test buffering
	buf := make([]byte, 1)
	var encrypted []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			encrypted = append(encrypted, buf[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
	}

	// Verify header
	if string(encrypted[:FileMagicSize]) != FileMagic {
		t.Errorf("bad magic in small-buffer read")
	}

	// Decrypt all blocks
	blockOffset := FileHeaderSize
	blockIndex := uint64(0)
	decryptedTotal := make([]byte, 0, len(plaintext))
	for blockOffset < len(encrypted) {
		blockEnd := blockOffset + BlockSize
		if blockEnd > len(encrypted) {
			blockEnd = len(encrypted)
		}
		blockData := encrypted[blockOffset:blockEnd]
		decBlock, err := c.DecryptBlock(blockData, blockIndex, nonce)
		if err != nil {
			t.Fatalf("DecryptBlock %d failed: %v", blockIndex, err)
		}
		decryptedTotal = append(decryptedTotal, decBlock...)
		blockOffset += len(blockData)
		blockIndex++
	}

	if !bytes.Equal(plaintext, decryptedTotal) {
		t.Errorf("small-buffer round-trip mismatch")
	}
}

// TestEncryptingReader_NonceCompatibility verifies the encrypting reader's
// output header contains the correct nonce.
func TestEncryptingReader_NonceCompatibility(t *testing.T) {
	c, _ := NewRcloneCipher("password", "salt")
	var nonce [24]byte
	copy(nonce[:], []byte("noncecompatest12345678"))

	plaintext := []byte("test")
	r := NewEncryptingReader(bytes.NewReader(plaintext), c, nonce, int64(len(plaintext)))

	encrypted, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if !bytes.Equal(encrypted[FileMagicSize:FileHeaderSize], nonce[:]) {
		t.Errorf("nonce in header does not match")
	}
}
