package sync

import "testing"

func FuzzParseCompareMode(f *testing.F) {
	for _, mode := range []string{"", "size-mtime", "mtime-only", "hash", "HASH", " size-mtime ", "\x00"} {
		f.Add(mode)
	}
	f.Fuzz(func(t *testing.T, mode string) {
		skipSize, forceHash, err := ParseCompareMode(mode)
		switch mode {
		case "size-mtime":
			if err != nil || skipSize || forceHash {
				t.Fatalf("size-mtime = (%v, %v, %v), want (false, false, nil)", skipSize, forceHash, err)
			}
		case "mtime-only":
			if err != nil || !skipSize || forceHash {
				t.Fatalf("mtime-only = (%v, %v, %v), want (true, false, nil)", skipSize, forceHash, err)
			}
		case "hash":
			if err != nil || skipSize || !forceHash {
				t.Fatalf("hash = (%v, %v, %v), want (false, true, nil)", skipSize, forceHash, err)
			}
		default:
			if err == nil {
				t.Fatalf("invalid mode %q unexpectedly succeeded", mode)
			}
		}
	})
}
