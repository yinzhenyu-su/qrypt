package session

import (
	"reflect"
	"testing"
)

func TestConfirmedBitmapSizesForPartition(t *testing.T) {
	// 整除：10B/2B = 5 片。
	if got := ConfirmedBitmap(10, 2, nil); len(got) != 1 {
		t.Fatalf("10/2 bitmap len = %d, want 1", len(got))
	}
	// 不整除：10B/3B = 4 片（向上取整）。
	if got := ConfirmedBitmap(10, 3, nil); len(got) != 1 {
		t.Fatalf("10/3 bitmap len = %d, want 1", len(got))
	}
	// 空文件按 1 片处理。
	if got := ConfirmedBitmap(0, 3, nil); len(got) != 1 {
		t.Fatalf("0/3 bitmap len = %d, want 1", len(got))
	}
	// 跨字节：100 片需要 13 字节。
	if got := ConfirmedBitmap(100, 1, nil); len(got) != 13 {
		t.Fatalf("100/1 bitmap len = %d, want 13", len(got))
	}
	// 单字节边界：8 片 1 字节、9 片 2 字节。
	if got := ConfirmedBitmap(8, 1, nil); len(got) != 1 {
		t.Fatalf("8/1 bitmap len = %d, want 1", len(got))
	}
	if got := ConfirmedBitmap(9, 1, nil); len(got) != 2 {
		t.Fatalf("9/1 bitmap len = %d, want 2", len(got))
	}
}

func TestConfirmedBitmapGrowsAndPreservesExisting(t *testing.T) {
	existing := []byte{0b00000001} // 分片 1 已确认
	got := ConfirmedBitmap(100, 1, existing)
	if len(got) != 13 {
		t.Fatalf("grew bitmap len = %d, want 13", len(got))
	}
	if got[0] != 0b00000001 {
		t.Fatalf("bitmap[0] = %b, want existing bits preserved", got[0])
	}
	// 容量足够时原样返回（不复制）。
	same := ConfirmedBitmap(8, 1, existing)
	if len(same) != 1 || same[0] != 0b00000001 {
		t.Fatalf("same-size bitmap = %v, want untouched existing", same)
	}
}

func TestConfirmedBitmapInvalidPartSizeReturnsExisting(t *testing.T) {
	existing := []byte{0xFF}
	if got := ConfirmedBitmap(10, 0, existing); !reflect.DeepEqual(got, existing) {
		t.Fatalf("partSize 0 bitmap = %v, want existing untouched", got)
	}
	if got := ConfirmedBitmap(10, -1, nil); got != nil {
		t.Fatalf("negative partSize bitmap = %v, want nil", got)
	}
}

func TestMarkConfirmedSetsExactBit(t *testing.T) {
	// 8 片单字节：bit(n-1)。
	bitmap := ConfirmedBitmap(8, 1, nil)
	for _, part := range []int{1, 2, 3, 8} {
		MarkConfirmed(bitmap, part)
	}
	if bitmap[0] != 0b10000111 {
		t.Fatalf("bitmap = %08b, want 10000111", bitmap[0])
	}
	// 跨字节：第 9 片落在第二个字节的 bit 0。
	two := ConfirmedBitmap(9, 1, nil)
	MarkConfirmed(two, 9)
	if two[0] != 0 || two[1] != 0b00000001 {
		t.Fatalf("two-byte bitmap = %v, want [0, 1]", two)
	}
	// 非正分片号忽略。
	MarkConfirmed(two, 0)
	MarkConfirmed(two, -3)
	if two[1] != 0b00000001 {
		t.Fatalf("invalid part numbers changed bitmap: %v", two)
	}
}

func TestConfirmedPartsRoundTrip(t *testing.T) {
	bitmap := ConfirmedBitmap(64, 1, nil) // 64 片 / 8 字节，覆盖字节边界
	want := map[int]bool{1: true, 8: true, 9: true, 63: true, 64: true}
	for part := range want {
		MarkConfirmed(bitmap, part)
	}
	// 幂等：重复标记不改变结果。
	for part := range want {
		MarkConfirmed(bitmap, part)
	}
	if got := ConfirmedParts(bitmap); !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfirmedParts = %v, want %v", got, want)
	}
}

func TestConfirmedPartsEmpty(t *testing.T) {
	for _, bitmap := range [][]byte{nil, {}, make([]byte, 2)} {
		if got := ConfirmedParts(bitmap); len(got) != 0 {
			t.Fatalf("ConfirmedParts(%v) = %v, want empty", bitmap, got)
		}
	}
}