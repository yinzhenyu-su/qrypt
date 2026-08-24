// Package limits defines resource limits shared by independent public packages.
package limits

const (
	// DefaultReadRequestBytes is the maximum size of one Core random-read request.
	DefaultReadRequestBytes = 3 << 20
	// DefaultMetadataReadChunkBytes is the chunk size used while loading large
	// media metadata through a bounded read function.
	DefaultMetadataReadChunkBytes = DefaultReadRequestBytes
	// DefaultDownloadBufferBytes is the default buffer size for Core downloads.
	DefaultDownloadBufferBytes = DefaultReadRequestBytes
)
