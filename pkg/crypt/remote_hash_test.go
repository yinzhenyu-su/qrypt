package crypt

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestEncryptedHashMatchesRealCiphertext(t *testing.T) {
	// The core claim behind non-download hash verification: encrypting the
	// plaintext locally with the nonce stored in the remote header yields the
	// exact ciphertext stored remotely, so its hash matches the remote hash.
	plaintext := bytes.Repeat([]byte("qrypt-check-payload-"), 500) // ~10 KiB
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := cp.GenerateRandomNonce()
	if err != nil {
		t.Fatal(err)
	}

	// Simulate the stored ciphertext: encrypt plaintext with the random
	// nonce, as the upload path does.
	encrypted, err := io.ReadAll(NewEncryptingReader(bytes.NewReader(plaintext), cp, nonce, int64(len(plaintext))))
	if err != nil {
		t.Fatal(err)
	}

	// Backend that "stores" the ciphertext; only the header is ever read.
	raw := &recordingRawDriver{data: encrypted}
	raw.RemoteHashValue = "abc123" // whatever the drive reports; not used here
	wrapped := NewDriver(raw, cp, DriverOptions{})

	entry := drive.Entry{ID: "f1", Size: int64(len(plaintext))}
	got, err := wrapped.EncryptedHash(context.Background(), entry, bytes.NewReader(plaintext), int64(len(plaintext)), drive.HashSHA1)
	if err != nil {
		t.Fatal(err)
	}

	want := sha1.Sum(encrypted)
	if got != hex.EncodeToString(want[:]) {
		t.Fatalf("EncryptedHash = %s, want %s (must equal hash of the real ciphertext)", got, hex.EncodeToString(want[:]))
	}
	// Only the 32-byte header must have been read from the backend.
	if len(raw.reads) != 1 || raw.reads[0].offset != 0 || raw.reads[0].size != int64(FileHeaderSize) {
		t.Fatalf("backend reads = %+v, want a single header read of %d bytes", raw.reads, FileHeaderSize)
	}
}

func TestRemoteHashDelegatesToBackend(t *testing.T) {
	raw := &recordingRawDriver{RemoteHashValue: "deadbeef"}
	raw.RemoteHashAlg = drive.HashSHA1
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	wrapped := NewDriver(raw, cp, DriverOptions{})
	alg, hash, err := wrapped.RemoteHash(context.Background(), drive.Entry{ID: "f1"})
	if err != nil {
		t.Fatal(err)
	}
	if alg != drive.HashSHA1 || hash != "deadbeef" {
		t.Fatalf("RemoteHash = (%s, %s), want (sha1, deadbeef)", alg, hash)
	}
}

func TestRemoteHashUnsupportedBackend(t *testing.T) {
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	// recordingRawDriver without RemoteHash support: drive.UnsupportedOperations
	// does not include RemoteHasher.
	wrapped := NewDriver(&recordingRawDriver{}, cp, DriverOptions{})
	if _, _, err := wrapped.RemoteHash(context.Background(), drive.Entry{ID: "f1"}); err != drive.ErrUnsupported {
		t.Fatalf("RemoteHash err = %v, want ErrUnsupported", err)
	}
}
