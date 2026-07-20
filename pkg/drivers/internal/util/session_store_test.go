package util

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type testUploadSession struct {
	Key       string          `json:"key"`
	UploadID  string          `json:"upload_id"`
	SavedAt   time.Time       `json:"saved_at"`
	Completed map[int]bool    `json:"completed,omitempty"`
	Metadata  map[string]bool `json:"metadata,omitempty"`
}

func newTestUploadSessionStore(t *testing.T, maxEntries int) *UploadSessionStore[testUploadSession] {
	t.Helper()
	return NewUploadSessionStore(UploadSessionStoreOptions[testUploadSession]{
		Store:      drive.NewFileStateStore(filepath.Join(t.TempDir(), "state")),
		File:       "upload_sessions.json",
		MaxAge:     time.Hour,
		MaxEntries: maxEntries,
		Key: func(session testUploadSession) string {
			return session.Key
		},
		Valid: func(key string, session testUploadSession) bool {
			return session.Key != "" && session.UploadID != "" && len(session.Completed) > 0
		},
		UpdatedAt: func(session testUploadSession) time.Time {
			return session.SavedAt
		},
		Touch: func(session *testUploadSession, now time.Time) {
			session.SavedAt = now
		},
		OnError: func(err error) {
			t.Fatalf("unexpected upload session store error: %v", err)
		},
	})
}

func TestUploadSessionStoreSaveLoadDelete(t *testing.T) {
	store := newTestUploadSessionStore(t, 10)
	store.Save(testUploadSession{
		Key:       "session-1",
		UploadID:  "upload-1",
		Completed: map[int]bool{1: true},
	})

	session, ok := store.Load("session-1")
	if !ok {
		t.Fatal("expected upload session to load")
	}
	if session.UploadID != "upload-1" {
		t.Fatalf("unexpected upload id %q", session.UploadID)
	}
	if session.SavedAt.IsZero() {
		t.Fatal("expected save to touch session timestamp")
	}

	store.Delete("session-1")
	if _, ok := store.Load("session-1"); ok {
		t.Fatal("expected deleted upload session to be absent")
	}
}

func TestUploadSessionStorePrunesInvalidExpiredAndOverflow(t *testing.T) {
	now := time.Now()
	store := newTestUploadSessionStore(t, 2)

	sessions, changed := store.PrunedForTest(map[string]testUploadSession{
		"valid-old": {
			Key:       "valid-old",
			UploadID:  "upload-old",
			SavedAt:   now.Add(-30 * time.Minute),
			Completed: map[int]bool{1: true},
		},
		"valid-new": {
			Key:       "valid-new",
			UploadID:  "upload-new",
			SavedAt:   now.Add(-10 * time.Minute),
			Completed: map[int]bool{1: true},
		},
		"valid-newest": {
			Key:       "valid-newest",
			UploadID:  "upload-newest",
			SavedAt:   now,
			Completed: map[int]bool{1: true},
		},
		"expired": {
			Key:       "expired",
			UploadID:  "upload-expired",
			SavedAt:   now.Add(-2 * time.Hour),
			Completed: map[int]bool{1: true},
		},
		"invalid": {
			Key:      "invalid",
			UploadID: "upload-invalid",
			SavedAt:  now,
		},
	}, now)

	if !changed {
		t.Fatal("expected pruning to report changes")
	}
	if _, ok := sessions["valid-new"]; !ok {
		t.Fatal("expected newer valid session to remain")
	}
	if _, ok := sessions["valid-newest"]; !ok {
		t.Fatal("expected newest valid session to remain")
	}
	for _, key := range []string{"valid-old", "expired", "invalid"} {
		if _, ok := sessions[key]; ok {
			t.Fatalf("expected %q to be pruned", key)
		}
	}
}
