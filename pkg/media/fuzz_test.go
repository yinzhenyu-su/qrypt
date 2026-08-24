package media

import (
	"context"
	"testing"
)

func FuzzPassthroughVirtualFileBounds(f *testing.F) {
	for _, seed := range [][2]int64{{0, 0}, {1, 0}, {4096, 0}, {4096, 1}, {4096, -1}, {4096, 4096}} {
		f.Add(seed[0], seed[1], int64(0))
	}
	f.Fuzz(func(t *testing.T, sizeInput, offsetInput, lengthInput int64) {
		size := sizeInput & 0x7fff
		length := int(lengthInput & 0x3ff)
		offset := offsetInput
		vf := NewPassthroughVirtualFile(size, func(_ context.Context, _ int64, length int) ([]byte, error) {
			return make([]byte, length), nil
		})
		if _, err := vf.ReadAt(context.Background(), offset, length); offset < 0 && err == nil {
			t.Fatalf("ReadAt(%d, %d) accepted a negative offset", offset, length)
		}
		mappings := vf.ReadMappings(offset, length)
		if offset < 0 || length == 0 || offset >= size {
			if mappings != nil {
				t.Fatalf("ReadMappings(%d, %d) = %+v, want nil", offset, length, mappings)
			}
			return
		}
		if len(mappings) != 1 || mappings[0].VirtualOffset != offset || mappings[0].SourceOffset != offset {
			t.Fatalf("ReadMappings(%d, %d) = %+v, want one identity mapping", offset, length, mappings)
		}
		wantLength := int64(length)
		if remaining := size - offset; wantLength > remaining {
			wantLength = remaining
		}
		if int64(mappings[0].Length) != wantLength {
			t.Fatalf("mapping length = %d, want %d", mappings[0].Length, wantLength)
		}
	})
}
