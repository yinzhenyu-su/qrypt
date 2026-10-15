package sftp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
)

func expiredSessionStore(t *testing.T, remote string) drive.StateStore {
	t.Helper()
	store := drive.NewFileStateStore(t.TempDir())
	token, err := json.Marshal(sftpToken{StagingPath: remote})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-sftpSessionMaxAge - time.Hour)
	if err := store.SaveJSON(sftpSessionFile, map[string]session.Session{
		"expired": {Key: "expired", Token: token, CreatedAt: old, UpdatedAt: old},
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSessionExpiryStartsOnlyAfterConnectionAndReclaims(t *testing.T) {
	for _, installFirst := range []bool{true, false} {
		name := "install-after-init"
		if installFirst {
			name = "install-before-init"
		}
		t.Run(name, func(t *testing.T) {
			server := newMockSFTPServer(t)
			local := filepath.Join(server.root, ".qrypt-sftp-upload-expired")
			if err := os.WriteFile(local, []byte("partial"), 0o600); err != nil {
				t.Fatal(err)
			}
			d := New(Options{Address: server.listener.Addr().String(), Username: server.user, PrivateKey: server.keyPath, KnownHosts: server.knownHostsPath, RootPath: server.root})
			// Persist protocol paths, not host paths: on Windows the remote
			// absolute path is /C:/... rather than C:\\... .
			remote := path.Join(d.rootPath, filepath.Base(local))
			store := expiredSessionStore(t, remote)
			t.Cleanup(func() {
				if err := d.Drop(context.Background()); err != nil {
					t.Error(err)
				}
			})
			if installFirst {
				d.InstallStateStore(store)
				if d.sessionCancel != nil {
					t.Fatal("expiry started before connection initialization")
				}
				if _, err := os.Stat(local); err != nil {
					t.Fatalf("pre-init install touched staging: %v", err)
				}
			}
			if err := d.Init(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !installFirst {
				d.InstallStateStore(store)
			}
			if d.sessionCancel == nil {
				t.Fatal("expiry did not start after initialization")
			}
			if _, err := os.Stat(local); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("expired staging not reclaimed: %v", err)
			}
			if _, ok := d.sessions.Get("expired"); ok {
				t.Fatal("reclaimed binding remains in memory")
			}
			var persisted map[string]session.Session
			if err := store.LoadJSON(sftpSessionFile, &persisted); err != nil || len(persisted) != 0 {
				t.Fatalf("reclaimed binding remains on disk: %+v, %v", persisted, err)
			}
		})
	}
}

func TestCancelledInitDoesNotStartSessionExpiry(t *testing.T) {
	d := New(Options{})
	d.InstallStateStore(expiredSessionStore(t, "/missing-staging"))
	t.Cleanup(func() { _ = d.Drop(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.Init(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Init = %v, want cancellation", err)
	}
	if d.sessionCancel != nil {
		t.Fatal("expiry started despite unsuccessful initialization")
	}
	if _, ok := d.sessions.Get("expired"); !ok {
		t.Fatal("unsuccessful initialization discarded a binding")
	}
}
