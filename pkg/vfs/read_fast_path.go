package vfs

func shouldPromoteCachedRange(requestSize int64) bool {
	return requestSize > 0 && requestSize < readChunkSize
}

func (v *VFS) hotChunk(cacheKey string, index int64) ([]byte, bool) {
	return newVFSReadRuntime(v).HotChunk(cacheKey, index)
}
func (v *VFS) putHotChunk(cacheKey string, index int64, data []byte) {
	newVFSReadRuntime(v).PutHotChunk(cacheKey, index, data)
}
func (v *VFS) debugHotChunks() (int, int64) {
	return newVFSReadRuntime(v).HotChunkStats()
}
