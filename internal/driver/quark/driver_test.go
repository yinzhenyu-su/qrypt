package quark

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestDriverInitListAndResolveRootPath(t *testing.T) {
	var seenCookie string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenCookie = r.Header.Get("Cookie")
		if r.URL.Path != "/file/sort" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		parent := r.URL.Query().Get("pdir_fid")
		switch parent {
		case "0":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"list": []map[string]any{
						{"fid": "root-docs", "file_name": "Docs", "file": false, "size": 0},
					},
				},
				"metadata": map[string]any{"_total": 1},
			})
		case "root-docs":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"list": []map[string]any{
						{"fid": "file-1", "file_name": "a.txt", "file": true, "file_size": 12, "updated_at": 1700000000000},
					},
				},
				"metadata": map[string]any{"_total": 1},
			})
		default:
			t.Fatalf("unexpected parent: %s", parent)
		}
	}))
	defer server.Close()

	driver := New("k=v", Options{RootPath: "/Docs", BaseURL: server.URL, V2URL: server.URL})
	if err := driver.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seenCookie != "k=v" {
		t.Fatalf("cookie header = %q, want k=v", seenCookie)
	}

	entries, err := driver.List(context.Background(), "0")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.ID != "file-1" || entry.ParentID != "root-docs" || entry.Name != "a.txt" || entry.IsDir || entry.Size != 12 {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

func TestRegisterQuarkDriver(t *testing.T) {
	driver, err := drive.New("quark", drive.Params{
		"cookie":   "k=v",
		"base_url": "http://127.0.0.1",
		"v2_url":   "http://127.0.0.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := driver.(*Driver); !ok {
		t.Fatalf("driver type = %T, want *quark.Driver", driver)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}
