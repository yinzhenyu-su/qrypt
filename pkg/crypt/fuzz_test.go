package crypt_test

import (
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/crypt"
)

// FuzzCipherSegmentRoundtrip pins the filename-encryption contract: any
// input segment encrypts and decrypts back to the exact original bytes
// (the cipher is a bijection over arbitrary byte strings).
func FuzzCipherSegmentRoundtrip(f *testing.F) {
	c, err := crypt.NewRcloneCipher("fuzz-password", "fuzz-salt")
	if err != nil {
		panic(err)
	}
	seeds := []string{
		"", "a", "hello world", "中文名.txt", "a/b/../c", "  spaces  ",
		"UPPER/lower.MIXED", "x\x00y", strings.Repeat("long", 100),
		"\xfe\xff\x01", "under_score-dash", "emoji 😀.jpg",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		enc := c.EncryptSegment(name)
		dec, err := c.DecryptSegment(enc)
		if err != nil {
			t.Fatalf("decrypt failed for %q -> %q: %v", name, enc, err)
		}
		if dec != name {
			t.Fatalf("roundtrip mismatch: %q -> %q -> %q", name, enc, dec)
		}
	})
}
