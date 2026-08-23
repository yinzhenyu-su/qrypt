package vfs_test

import (
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	vfsread "github.com/yinzhenyu/qrypt/pkg/vfs/read"
)

const testReadChunkSize = vfsread.ChunkSize
const testUploadDelay = 10 * time.Millisecond

var _ drive.Driver = (*localfs.Driver)(nil)
