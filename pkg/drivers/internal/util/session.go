package util

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"time"
)

func UploadSessionKey(parentID, name string, size int64, hashes ...string) string {
	parts := make([]string, 0, 3+len(hashes))
	parts = append(parts, parentID, name, strconv.FormatInt(size, 10))
	parts = append(parts, hashes...)
	return SessionKey(parts...)
}

func SessionKey(parts ...string) string {
	h := sha256.New()
	for i, part := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(part))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func PruneSessions[T any](sessions map[string]T, now time.Time, maxAge time.Duration, maxEntries int, valid func(string, T) bool, updatedAt func(T) time.Time) bool {
	if sessions == nil {
		return false
	}
	changed := false
	for key, session := range sessions {
		if key == "" || !valid(key, session) || sessionExpired(updatedAt(session), now, maxAge) {
			delete(sessions, key)
			changed = true
		}
	}
	if maxEntries <= 0 || len(sessions) <= maxEntries {
		return changed
	}
	type item struct {
		key       string
		updatedAt time.Time
	}
	items := make([]item, 0, len(sessions))
	for key, session := range sessions {
		items = append(items, item{key: key, updatedAt: updatedAt(session)})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].updatedAt.After(items[j].updatedAt)
	})
	for _, item := range items[maxEntries:] {
		delete(sessions, item.key)
		changed = true
	}
	return changed
}

func sessionExpired(updatedAt, now time.Time, maxAge time.Duration) bool {
	if maxAge <= 0 || updatedAt.IsZero() {
		return false
	}
	return now.Sub(updatedAt) > maxAge
}
