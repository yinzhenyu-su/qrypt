package p123

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

const p123SessionFile = "123_upload_sessions.json"

const p123SessionMaxAge = 24 * time.Hour

const p123SessionExpiryEvery = time.Hour

// p123UploadToken is the persisted provider upload handle plus a compact local
// confirmation record. 123pan exposes no part-progress query endpoint, so the
// driver falls back to the documented pattern: keep a confirmed-parts bitmap
// (session.ConfirmedBitmap) folded into the binding, and re-upload any part
// whose confirmation was lost. Parts are addressed by number and overwrite
// idempotently, so a lost confirmation costs one redundant PUT, never
// corruption.
type p123UploadToken struct {
	Bucket      string `json:"bucket"`
	Key         string `json:"key"`
	UploadID    string `json:"upload_id"`
	StorageNode string `json:"storage_node"`
	FileID      int64  `json:"file_id"`
	PartSize    int64  `json:"part_size"`
	Confirmed   []byte `json:"confirmed,omitempty"` // bit(n-1) = part n confirmed
}

func (t p123UploadToken) uploadRequest() uploadRequestResp {
	var up uploadRequestResp
	up.Data.Bucket = t.Bucket
	up.Data.Key = t.Key
	up.Data.UploadID = t.UploadID
	up.Data.StorageNode = t.StorageNode
	up.Data.FileID = t.FileID
	return up
}

func (d *Driver) installSessionIndex(store drive.StateStore) {
	d.sessionStoreMu.Lock()
	defer d.sessionStoreMu.Unlock()
	if d.sessionCancel != nil {
		d.sessionCancel()
		d.sessionCancel = nil
	}
	d.sessions = session.NewIndex(store, p123SessionFile, session.IndexOptions{
		OnError: func(err error) {
			logging.L.Warnf("[123] upload session state failed: %v", err)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	d.sessionCancel = cancel
	d.expireSessions()
	go session.RunExpirer(ctx, p123SessionExpiryEvery, d.expireSessions)
}

func (d *Driver) expireSessions() {
	if d.sessions != nil {
		d.sessions.Expire(p123SessionMaxAge, time.Now(), d.reclaimUploadSession)
	}
}

// reclaimUploadSession releases an expired upload. 123pan has no abort
// endpoint, so the provider session simply lapses server-side and reclaiming
// only drops the local binding (idempotent, no side effects).
func (d *Driver) reclaimUploadSession(s session.Session) error {
	return nil
}

// beginUpload returns a resumable provider upload handle for the source. It
// either resumes a persisted binding or reserves one before creating a fresh
// provider upload, so a crash between the two leaves a reclaimable entry
// rather than an untracked orphan.
func (d *Driver) beginUpload(ctx context.Context, sessionKey string, parentID int64, name string, size int64, etag string) (uploadRequestResp, map[int]bool, error) {
	if d.sessions != nil {
		if binding, ok := d.sessions.Get(sessionKey); ok {
			var tok p123UploadToken
			if err := json.Unmarshal(binding.Token, &tok); err == nil && tok.UploadID != "" && tok.PartSize == uploadChunkSize {
				d.sessions.Touch(sessionKey)
				bitmap := session.ConfirmedBitmap(size, uploadChunkSize, tok.Confirmed)
				return tok.uploadRequest(), session.ConfirmedParts(bitmap), nil
			}
			// An empty token is a reservation that never reached the provider;
			// a mismatched one belongs to a different chunking scheme.
			d.sessions.Delete(sessionKey)
		}
		if err := d.sessions.Create(sessionKey, nil); err != nil {
			return uploadRequestResp{}, nil, fmt.Errorf("persist upload session: %w", err)
		}
	}

	up, err := d.cl.uploadRequest(ctx, parentID, name, size, etag)
	if err != nil {
		d.deleteSession(sessionKey)
		return uploadRequestResp{}, nil, err
	}
	if up.Data.Reuse || up.Data.Key == "" || hasStaticCredentials(up) {
		// Nothing to resume: an instant link, a static-credential S3 upload, or
		// a provider response the caller will surface as an error.
		d.deleteSession(sessionKey)
		return up, nil, nil
	}

	tok := p123UploadToken{
		Bucket:      up.Data.Bucket,
		Key:         up.Data.Key,
		UploadID:    up.Data.UploadID,
		StorageNode: up.Data.StorageNode,
		FileID:      up.Data.FileID,
		PartSize:    uploadChunkSize,
	}
	tok.Confirmed = session.ConfirmedBitmap(size, uploadChunkSize, nil)
	if d.sessions != nil {
		if err := d.sessions.Create(sessionKey, mustJSON(tok)); err != nil {
			return uploadRequestResp{}, nil, fmt.Errorf("persist upload session: %w", err)
		}
	}
	return up, session.ConfirmedParts(tok.Confirmed), nil
}

// markPartConfirmed records one uploaded part in the session bitmap. TouchWith
// serialises the read-modify-write under the index lock and throttles the disk
// write to one per interval.
func (d *Driver) markPartConfirmed(sessionKey string, size int64, part int) {
	if d.sessions == nil || sessionKey == "" {
		return
	}
	d.sessions.TouchWith(sessionKey, func(s *session.Session) {
		var tok p123UploadToken
		if err := json.Unmarshal(s.Token, &tok); err != nil || tok.UploadID == "" {
			return
		}
		tok.Confirmed = session.ConfirmedBitmap(size, uploadChunkSize, tok.Confirmed)
		session.MarkConfirmed(tok.Confirmed, part)
		if raw, err := json.Marshal(tok); err == nil {
			s.Token = raw
		}
		s.UpdatedAt = time.Now()
	})
}

func (d *Driver) deleteSession(sessionKey string) {
	if d.sessions != nil && sessionKey != "" {
		d.sessions.Delete(sessionKey)
	}
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return raw
}
