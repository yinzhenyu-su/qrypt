package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/util"
)

// TestThumbnailInfoJSONFields pins the exported DTO's JSON shape: field
// names, order and omitempty behavior are part of the mobile contract.
func TestThumbnailInfoJSONFields(t *testing.T) {
	hit, err := json.Marshal(ThumbnailInfo{
		Hit:    true,
		Path:   "/cached/thumb.jpg",
		Mime:   "image/jpeg",
		Size:   9,
		Preset: "grid-128",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"hit":true,"path":"/cached/thumb.jpg","mime":"image/jpeg","size":9,"preset":"grid-128"}`; string(hit) != want {
		t.Fatalf("hit json = %s, want %s", hit, want)
	}
	miss, err := json.Marshal(ThumbnailInfo{Hit: false, Preset: "grid-128"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"hit":false,"preset":"grid-128"}`; string(miss) != want {
		t.Fatalf("miss json = %s, want %s", miss, want)
	}
	zero, err := json.Marshal(ThumbnailInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"hit":false}`; string(zero) != want {
		t.Fatalf("zero json = %s, want %s", zero, want)
	}
}

// TestThumbnailFacadeErrorText pins the error identity and wording clients
// observe through the Core facade after the service extraction.
func TestThumbnailFacadeErrorText(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "photo.jpg"), []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(remote, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
	[[mounts]]
	name = "quark"
	type = "localfs"
	[mounts.params]
	root_path = `+util.TOMLPath(remote)+`
	`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Open(ctx, Options{ConfigPath: configPath, Runtime: testRuntimeLayout(tmp)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)

	source := "/quark/photo.jpg"
	tests := []struct {
		name string
		call func() error
		want string
	}{
		{
			name: "blank preset",
			call: func() error { _, err := c.GetThumbnailFile(ctx, source, "   "); return err },
			want: "core: thumbnail preset required",
		},
		{
			name: "slash preset",
			call: func() error { _, err := c.GetThumbnailFile(ctx, source, "grid/128"); return err },
			want: `core: invalid thumbnail preset "grid/128"`,
		},
		{
			name: "dotdot preset",
			call: func() error { _, err := c.PutThumbnailFile(ctx, source, "..", "image/jpeg", "ignored"); return err },
			want: `core: invalid thumbnail preset ".."`,
		},
		{
			name: "blank local path",
			call: func() error { _, err := c.PutThumbnailFile(ctx, source, "grid-128", "image/jpeg", "   "); return err },
			want: "core: thumbnail local path required",
		},
		{
			name: "directory source",
			call: func() error { _, err := c.GetThumbnailFile(ctx, "/quark/folder", "grid-128"); return err },
			want: "core: /quark/folder is a directory",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if err == nil || err.Error() != tt.want {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

// TestThumbnailClosedCoreErrors pins the lifecycle error for a nil core,
// matching the mobile session error envelope's "core: closed".
func TestThumbnailClosedCoreErrors(t *testing.T) {
	var c *Core
	for name, call := range map[string]func() error{
		"get": func() error { _, err := c.GetThumbnailFile(context.Background(), "/x", "grid-128"); return err },
		"put": func() error {
			_, err := c.PutThumbnailFile(context.Background(), "/x", "grid-128", "image/jpeg", "/y")
			return err
		},
	} {
		if err := call(); err == nil || err.Error() != "core: closed" {
			t.Fatalf("%s on nil core err = %v, want core: closed", name, err)
		}
	}
}
