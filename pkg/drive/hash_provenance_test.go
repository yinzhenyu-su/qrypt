package drive_test

import (
	"context"
	"errors"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type legacyHasher struct {
	algorithm drive.HashAlgorithm
	hash      string
	err       error
}

func (h legacyHasher) RemoteHash(context.Context, drive.Entry) (drive.HashAlgorithm, string, error) {
	return h.algorithm, h.hash, h.err
}

type provenanceHasher struct {
	info drive.RemoteHashInfo
	err  error
}

func (h provenanceHasher) RemoteHash(context.Context, drive.Entry) (drive.HashAlgorithm, string, error) {
	return h.info.Algorithm, h.info.Hash, h.err
}

func (h provenanceHasher) RemoteHashInfo(context.Context, drive.Entry) (drive.RemoteHashInfo, error) {
	return h.info, h.err
}

func TestRemoteHashInfoForTreatsLegacyHashesAsDeclared(t *testing.T) {
	info, err := drive.RemoteHashInfoFor(context.Background(),
		legacyHasher{algorithm: drive.HashSHA1, hash: "abc"}, drive.Entry{ID: "f"})
	if err != nil {
		t.Fatal(err)
	}
	if info.Verified {
		t.Fatal("legacy RemoteHasher must not claim a verified hash")
	}
	if info.Source != drive.RemoteHashSourceDeclared {
		t.Fatalf("source = %q, want declared", info.Source)
	}
	if info.Algorithm != drive.HashSHA1 || info.Hash != "abc" {
		t.Fatalf("info = %+v", info)
	}
}

func TestRemoteHashInfoForKeepsProvenance(t *testing.T) {
	info, err := drive.RemoteHashInfoFor(context.Background(), provenanceHasher{info: drive.RemoteHashInfo{
		Algorithm: drive.HashSHA256,
		Hash:      "def",
		Verified:  true,
	}}, drive.Entry{ID: "f"})
	if err != nil {
		t.Fatal(err)
	}
	if !info.Verified || info.Source != drive.RemoteHashSourceProvider {
		t.Fatalf("info = %+v, want verified provider hash", info)
	}
}

func TestRemoteHashInfoForNormalizesAndRejectsEmptyHashes(t *testing.T) {
	info, err := drive.RemoteHashInfoFor(context.Background(), provenanceHasher{info: drive.RemoteHashInfo{
		Algorithm: drive.HashMD5,
		Hash:      "abc",
		Verified:  false,
	}}, drive.Entry{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Source != drive.RemoteHashSourceDeclared {
		t.Fatalf("unverified provenance source = %q, want declared", info.Source)
	}

	if _, err := drive.RemoteHashInfoFor(context.Background(), provenanceHasher{}, drive.Entry{}); !errors.Is(err, drive.ErrUnsupported) {
		t.Fatalf("empty provenance hash err = %v, want ErrUnsupported", err)
	}
	if _, err := drive.RemoteHashInfoFor(context.Background(), legacyHasher{err: drive.ErrUnsupported}, drive.Entry{}); !errors.Is(err, drive.ErrUnsupported) {
		t.Fatalf("legacy unsupported err = %v, want ErrUnsupported", err)
	}
	if _, err := drive.RemoteHashInfoFor(context.Background(), struct{}{}, drive.Entry{}); !errors.Is(err, drive.ErrUnsupported) {
		t.Fatalf("missing capability err = %v, want ErrUnsupported", err)
	}
}
