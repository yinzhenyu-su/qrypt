package vfs_test

import (
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"time"
)

const testReadChunkSize = 1024 * 1024
const testUploadDelay = 10 * time.Millisecond

var _ drive.Driver = (*localfs.Driver)(nil)
