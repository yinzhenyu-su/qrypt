package core

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestNormalizeOfflineSourceMagnet(t *testing.T) {
	t.Parallel()

	normalized, err := NormalizeOfflineSource("magnet:?xt=urn:btih:ABCDEF0123&dn=Example")
	if err != nil {
		t.Fatalf("NormalizeOfflineSource: %v", err)
	}
	if normalized.Kind != drive.OfflineSourceMagnet {
		t.Fatalf("kind = %q, want magnet", normalized.Kind)
	}
	if normalized.Identity != "magnet:btih:abcdef0123" {
		t.Fatalf("identity = %q", normalized.Identity)
	}
}

func TestNormalizeOfflineSourceHTTPFragmentAndQuery(t *testing.T) {
	t.Parallel()

	withFragment, err := NormalizeOfflineSource("https://example.com/file.iso#frag")
	if err != nil {
		t.Fatalf("fragment: %v", err)
	}
	withoutFragment, err := NormalizeOfflineSource("https://example.com/file.iso")
	if err != nil {
		t.Fatalf("no fragment: %v", err)
	}
	if withFragment.Identity != withoutFragment.Identity {
		t.Fatalf("fragment must not change identity: %q vs %q", withFragment.Identity, withoutFragment.Identity)
	}
	withQuery, err := NormalizeOfflineSource("https://example.com/file.iso?token=1")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if withQuery.Identity == withoutFragment.Identity {
		t.Fatal("a query change must change the source identity")
	}
}

func TestNormalizeOfflineSourceRejectsShareLinkAndScheme(t *testing.T) {
	t.Parallel()

	if _, err := NormalizeOfflineSource("https://pan.quark.cn/s/abc123"); err == nil {
		t.Fatal("share link must be rejected")
	} else {
		var input *OfflineInputError
		if !errors.As(err, &input) || input.Code != OfflineInputSourceShareLink {
			t.Fatalf("share link error = %v, want share-link code", err)
		}
	}
	if _, err := NormalizeOfflineSource("thunder://xyz"); err == nil {
		t.Fatal("unsupported scheme must be rejected")
	}
	if _, err := NormalizeOfflineSource("   "); err == nil {
		t.Fatal("empty source must be rejected")
	}
}

func TestNormalizeOfflineSourceTorrentEqualsMagnet(t *testing.T) {
	t.Parallel()

	info := []byte("d4:name4:test6:lengthi5ee")
	torrent := append([]byte("d8:announce16:http://tracker/x4:info"), info...)
	torrent = append(torrent, 'e')
	hash := sha1.Sum(info)
	magnet := "magnet:?xt=urn:btih:" + hex.EncodeToString(hash[:])

	path := filepath.Join(t.TempDir(), "example.torrent")
	if err := os.WriteFile(path, torrent, 0o644); err != nil {
		t.Fatal(err)
	}
	fromTorrent, err := NormalizeOfflineSource(path)
	if err != nil {
		t.Fatalf("NormalizeOfflineSource(torrent): %v", err)
	}
	fromMagnet, err := NormalizeOfflineSource(magnet)
	if err != nil {
		t.Fatalf("NormalizeOfflineSource(magnet): %v", err)
	}
	if fromTorrent.Identity != fromMagnet.Identity {
		t.Fatalf("torrent identity %q != magnet identity %q", fromTorrent.Identity, fromMagnet.Identity)
	}
	if fromTorrent.Kind != drive.OfflineSourceMagnet {
		t.Fatalf("torrent kind = %q, want magnet (it is submitted as a magnet)", fromTorrent.Kind)
	}
	if !strings.Contains(fromTorrent.Source, "dn=test") {
		t.Fatalf("torrent magnet should carry dn: %q", fromTorrent.Source)
	}
	if !strings.Contains(fromTorrent.Source, "tr=http") {
		t.Fatalf("torrent magnet should carry tr: %q", fromTorrent.Source)
	}
}

// TestOfflineFingerprint pins the active de-duplication identity: mount +
// parse directory + source identity + selection, with the final destination
// excluded.
func TestOfflineFingerprint(t *testing.T) {
	t.Parallel()

	base := offlineDetail{
		ParseMount:     "quark",
		ParseDir:       "/A",
		SourceKind:     "magnet",
		SourceIdentity: "magnet:btih:abc",
	}
	same := base
	if offlineFingerprint(base) != offlineFingerprint(same) {
		t.Fatal("identical offline details must share a fingerprint")
	}

	// A different parse directory splits the fingerprint: the same source is
	// staged separately on /A and /B.
	otherDir := base
	otherDir.ParseDir = "/B"
	if offlineFingerprint(base) == offlineFingerprint(otherDir) {
		t.Fatal("different parse directories must not share a fingerprint")
	}
	// The default provider directory is distinct from the mount root.
	defaultDir := base
	defaultDir.ParseDir = ""
	rootDir := base
	rootDir.ParseDir = "/"
	if offlineFingerprint(defaultDir) == offlineFingerprint(rootDir) {
		t.Fatal("the provider default directory must not collide with the mount root")
	}

	// The final destination is not part of the fingerprint.
	fetchA := base
	fetchA.DownloadTo = "/local/a"
	fetchA.DownloadMount = "local"
	fetchB := base
	fetchB.DownloadTo = "/local/b"
	fetchB.DownloadMount = "local"
	if offlineFingerprint(fetchA) != offlineFingerprint(fetchB) {
		t.Fatal("a different final destination must not split the fingerprint")
	}

	// A different parse mount splits the fingerprint.
	otherMount := base
	otherMount.ParseMount = "other"
	if offlineFingerprint(base) == offlineFingerprint(otherMount) {
		t.Fatal("different parse mounts must not share a fingerprint")
	}

	// A different selection splits the fingerprint.
	largest := base
	largest.Selection = offlineDetailSelection{Mode: "largest", Largest: 2}
	if offlineFingerprint(base) == offlineFingerprint(largest) {
		t.Fatal("different selections must not share a fingerprint")
	}
}
