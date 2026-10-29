package drime

import (
	"strings"
	"testing"
)

func FuzzRemoteNameRoundTrip(f *testing.F) {
	for _, seed := range []string{"plain.bin", " leading.bin", "trailing.bin ", `back\slash.bin`, "qrypt~name", "文件.txt", ".", "..", "a/b"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, plain string) {
		remote, err := encodeRemoteName(plain)
		if err != nil {
			return
		}
		if len(remote) > 255 {
			t.Fatalf("encoded name %q is %d bytes", remote, len(remote))
		}
		if got := decodeRemoteName(remote); got != plain {
			t.Fatalf("round-trip %q -> %q -> %q", plain, remote, got)
		}
	})
}

func FuzzParseShareLink(f *testing.F) {
	for _, seed := range []string{
		"https://dri.me/abc?pwd=x",
		"https://app.drime.cloud/drive/s/abc:def?password=y",
		"https://example.com/abc",
		"://bad",
		"",
	} {
		f.Add(seed)
	}
	d := New(Options{AccessToken: "token"})
	f.Fuzz(func(t *testing.T, raw string) {
		link, ok := d.ParseShareLink(raw)
		if ok && link.LinkID == "" {
			t.Fatalf("parsed empty link id from %q", raw)
		}
	})
}

func FuzzCleanMountPath(f *testing.F) {
	for _, seed := range []string{"/", "/a/b", "a/b", "../x", "/a/../b", "", "//"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		clean, err := cleanMountPath(raw)
		if err != nil {
			return
		}
		if !strings.HasPrefix(clean, "/") {
			t.Fatalf("clean %q = %q", raw, clean)
		}
		for _, part := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
			if part == ".." {
				t.Fatalf("clean %q kept ..", raw)
			}
		}
	})
}
