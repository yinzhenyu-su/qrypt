package vfs

import (
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs/read"
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

type encryptedMarker interface {
	Encrypted() bool
}
