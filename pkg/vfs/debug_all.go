package vfs

import (
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs/read"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"sync"
	"time"
)

const uploadSnapshotHistoryLimit = 100
const debugReadHistoryLimit = read.HistoryLimit

var debugStartedAt = time.Now()
var debugStartedAtMu sync.RWMutex

func DebugStartedAt() time.Time {
	debugStartedAtMu.RLock()
	defer debugStartedAtMu.RUnlock()
	return debugStartedAt
}

func ResetDebugStartedAt() time.Time {
	debugStartedAtMu.Lock()
	defer debugStartedAtMu.Unlock()
	debugStartedAt = util.Now()
	return debugStartedAt
}

const (
	uploadSnapshotStatePreparing  = upload.SnapshotStatePreparing
	uploadSnapshotStateUploading  = upload.SnapshotStateUploading
	uploadSnapshotStateCommitting = upload.SnapshotStateCommitting
	uploadSnapshotStateCompleted  = upload.SnapshotStateCompleted
	uploadSnapshotStateFailed     = upload.SnapshotStateFailed
	uploadSnapshotStateSuperseded = upload.SnapshotStateSuperseded
)

type encryptedMarker interface {
	Encrypted() bool
}
