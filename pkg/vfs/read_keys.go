package vfs

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"strconv"
)

func (v *VFS) readLoadKey(entry drive.Entry) string {
	if key := v.readCacheKey(entry); key != "" {
		return key
	}
	return entry.ID
}
func (v *VFS) readCacheKey(entry drive.Entry) string {
	if entry.ID == "" || entry.ModTime.IsZero() {
		return ""
	}
	sum := sha256.Sum256([]byte(v.rootID + "\x00" + entry.ID + "\x00" + strconv.FormatInt(entry.Size, 10) + "\x00" + strconv.FormatInt(entry.ModTime.UTC().UnixNano(), 10)))
	return hex.EncodeToString(sum[:])
}
func readChunkKey(fid string, index int64) string {
	return fid + "\x00" + strconv.FormatInt(index, 10)
}
func readWindowKey(fid string, start, end int64) string {
	return fid + "\x00" + strconv.FormatInt(start, 10) + "\x00" + strconv.FormatInt(end, 10)
}
