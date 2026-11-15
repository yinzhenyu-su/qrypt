package crypt

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// TestDecryptingReaderRejectsTruncatedBlock: a ciphertext that ends inside a
// block must surface io.ErrUnexpectedEOF instead of attempting to authenticate
// a partial block. Otherwise a stalled or truncated Range response becomes a
// non-retryable decryption error.
func TestDecryptingReaderRejectsTruncatedBlock(t *testing.T) {
	cipher, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	var nonce [FileNonceSize]byte
	truncated := bytes.NewReader(make([]byte, BlockSize-1))
	reader := NewDecryptingReaderAtLimit(truncated, cipher, nonce, 0, BlockSize)

	_, err = io.ReadAll(reader)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v, want io.ErrUnexpectedEOF", err)
	}
}

// TestDecryptingReaderAcceptsShortFinalBlockWithoutLimit: rclone does not pad
// the final block, so an unknown-length stream may end inside a block.
func TestDecryptingReaderAcceptsShortFinalBlockWithoutLimit(t *testing.T) {
	cipher, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	var nonce [FileNonceSize]byte
	plaintext := bytes.Repeat([]byte("b"), 1024)
	encrypted, err := cipher.EncryptBlock(plaintext, 0, nonce)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewDecryptingReader(bytes.NewReader(encrypted), cipher, nonce)

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("plaintext differs: got %d bytes, want %d", len(got), len(plaintext))
	}
}

// TestDecryptingReaderDecryptsCompleteBlock: the normal path is unchanged.
func TestDecryptingReaderDecryptsCompleteBlock(t *testing.T) {
	cipher, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	var nonce [FileNonceSize]byte
	plaintext := bytes.Repeat([]byte("a"), BlockDataSize)
	encrypted, err := cipher.EncryptBlock(plaintext, 0, nonce)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewDecryptingReader(bytes.NewReader(encrypted), cipher, nonce)

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("plaintext differs: got %d bytes, want %d", len(got), len(plaintext))
	}
}

// TestDecryptingReaderEmptyStream: an empty body is a clean EOF.
func TestDecryptingReaderEmptyStream(t *testing.T) {
	cipher, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	var nonce [FileNonceSize]byte
	reader := NewDecryptingReader(bytes.NewReader(nil), cipher, nonce)
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("error = %v, want nil for an empty stream", err)
	}
}
