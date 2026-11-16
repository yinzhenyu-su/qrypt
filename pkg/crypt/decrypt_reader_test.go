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

// TestDecryptingReaderLimitKeepsUnknownLength: a backend that cannot report a
// size passes -1. Treating that as zero would stop the reader at the header and
// report a non-empty file as empty content.
func TestDecryptingReaderLimitKeepsUnknownLength(t *testing.T) {
	cipher, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	var nonce [FileNonceSize]byte
	plaintext := bytes.Repeat([]byte("c"), 1024)
	encrypted, err := cipher.EncryptBlock(plaintext, 0, nonce)
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int64{-1, -42} {
		reader := NewDecryptingReaderLimit(bytes.NewReader(encrypted), cipher, nonce, limit)
		got, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Fatalf("limit %d: got %d plaintext bytes, want %d", limit, len(got), len(plaintext))
		}
	}
}

// TestDecryptingReaderAtLimitKeepsUnknownLength: the ReaderAt variant shares the
// unknown-length sentinel.
func TestDecryptingReaderAtLimitKeepsUnknownLength(t *testing.T) {
	cipher, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	var nonce [FileNonceSize]byte
	plaintext := bytes.Repeat([]byte("d"), BlockDataSize)
	encrypted, err := cipher.EncryptBlock(plaintext, 0, nonce)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewDecryptingReaderAtLimit(bytes.NewReader(encrypted), cipher, nonce, 0, -1)
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("got %d plaintext bytes, want %d", len(got), len(plaintext))
	}
}

// TestDecryptingReaderRejectsBodyEndAtBlockBoundary: with a known length, a body
// that stops exactly after a whole block is still short, so it must be reported
// as a truncation for the read layer to replay instead of ending the stream.
func TestDecryptingReaderRejectsBodyEndAtBlockBoundary(t *testing.T) {
	cipher, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	var nonce [FileNonceSize]byte
	encrypted, err := cipher.EncryptBlock(bytes.Repeat([]byte("e"), BlockDataSize), 0, nonce)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewDecryptingReaderLimit(bytes.NewReader(encrypted), cipher, nonce, 2*BlockSize)
	if _, err := io.ReadAll(reader); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v, want io.ErrUnexpectedEOF after one of two blocks", err)
	}

	// An empty body with a known non-zero length is truncated as well.
	empty := NewDecryptingReaderLimit(bytes.NewReader(nil), cipher, nonce, BlockSize)
	if _, err := io.ReadAll(empty); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("empty body error = %v, want io.ErrUnexpectedEOF", err)
	}

	// A known zero length is a clean EOF: the header is the whole file.
	zero := NewDecryptingReaderLimit(bytes.NewReader(nil), cipher, nonce, 0)
	if _, err := io.ReadAll(zero); err != nil {
		t.Fatalf("zero-length stream error = %v, want nil", err)
	}
}
