package util

import (
	"testing"
	"time"
)

type testSession struct {
	valid     bool
	updatedAt time.Time
}

func TestUploadSessionKeyIsStableAndDelimited(t *testing.T) {
	got := UploadSessionKey("parent", "name", 12, "sha1")
	if got != UploadSessionKey("parent", "name", 12, "sha1") {
		t.Fatal("upload session key is not stable")
	}
	if got == UploadSessionKey("parent\x00name", "", 12, "sha1") {
		t.Fatal("upload session key should delimit fields")
	}
	if got == UploadSessionKey("parent", "name", 13, "sha1") {
		t.Fatal("upload session key should include size")
	}
	if got == UploadSessionKey("parent", "name", 12, "other") {
		t.Fatal("upload session key should include hashes")
	}
}

func TestPruneSessionsDropsInvalidExpiredAndOldestOverflow(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	sessions := map[string]testSession{
		"":        {valid: true, updatedAt: now},
		"invalid": {valid: false, updatedAt: now},
		"expired": {valid: true, updatedAt: now.Add(-2 * time.Hour)},
		"old":     {valid: true, updatedAt: now.Add(-20 * time.Minute)},
		"new":     {valid: true, updatedAt: now.Add(-10 * time.Minute)},
		"newest":  {valid: true, updatedAt: now},
	}

	changed := PruneSessions(sessions, now, time.Hour, 2, func(key string, session testSession) bool {
		return session.valid
	}, func(session testSession) time.Time {
		return session.updatedAt
	})

	if !changed {
		t.Fatal("PruneSessions changed = false, want true")
	}
	for _, key := range []string{"", "invalid", "expired", "old"} {
		if _, ok := sessions[key]; ok {
			t.Fatalf("session %q was not pruned: %+v", key, sessions)
		}
	}
	for _, key := range []string{"new", "newest"} {
		if _, ok := sessions[key]; !ok {
			t.Fatalf("session %q was pruned unexpectedly: %+v", key, sessions)
		}
	}
}
