package session

// ConfirmedBitmap 返回适配 size/partSize 分片数的确认位图；按需扩容并保留
// 旧位（bit(n-1) = 分片 n 已确认）。供无分片查询接口的驱动做“本地确认记录”
// 压缩存储，经 Index.TouchWith 节流落盘。
func ConfirmedBitmap(size, partSize int64, existing []byte) []byte {
	if partSize <= 0 {
		return existing
	}
	partCount := int((size + partSize - 1) / partSize)
	if partCount == 0 {
		partCount = 1
	}
	need := (partCount + 7) / 8
	if len(existing) >= need {
		return existing
	}
	bitmap := make([]byte, need)
	copy(bitmap, existing)
	return bitmap
}

// MarkConfirmed 置位分片 partNumber 的确认位。
func MarkConfirmed(bitmap []byte, partNumber int) {
	if partNumber <= 0 {
		return
	}
	bitmap[(partNumber-1)/8] |= 1 << ((partNumber - 1) % 8)
}

// ConfirmedParts 把位图展开为已完成分片编号集合。
func ConfirmedParts(bitmap []byte) map[int]bool {
	out := make(map[int]bool, 0)
	for index, bits := range bitmap {
		for bit := 0; bit < 8; bit++ {
			if bits&(1<<bit) != 0 {
				out[index*8+bit+1] = true
			}
		}
	}
	return out
}
