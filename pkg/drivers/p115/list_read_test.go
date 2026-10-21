package p115

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type localSDKTransport struct {
	endpoint *url.URL
	base     http.RoundTripper
}

func (r localSDKTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = r.endpoint.Scheme
	clone.URL.Host = r.endpoint.Host
	clone.Host = r.endpoint.Host
	return r.base.RoundTrip(clone)
}

// Redirect only this SDK instance, not global transports or provider URLs.
// Every request, including a fallback endpoint, stays on the local server.
func newLocalSDKDriver(t *testing.T, handler http.HandlerFunc) *Driver {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	d := New(Options{RootID: "root"})
	d.cl = driver115.New()
	d.cl.Client.SetTransport(localSDKTransport{endpoint: endpoint, base: server.Client().Transport})
	return d
}

func TestListPaginatesAndPreservesMetadata(t *testing.T) {
	t.Parallel()
	offsets := make(chan string, 2)
	d := newLocalSDKDriver(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != http.MethodGet || q.Get("cid") != "root" || q.Get("limit") != "1000" {
			t.Errorf("unexpected list request: %s %s", r.Method, r.URL)
		}
		offsets <- q.Get("offset")
		files := make([]map[string]any, 0, 1000)
		switch q.Get("offset") {
		case "0":
			files = append(files, map[string]any{"cid": "subdir", "pid": "root", "n": "folder", "t": "1700000000"})
			for i := 1; i < 1000; i++ {
				files = append(files, map[string]any{"fid": fmt.Sprint(i), "cid": "root", "n": fmt.Sprintf("file-%d", i), "s": "7", "sha": "abc", "pc": "pick", "t": "2026-01-02 03:04"})
			}
		case "1000":
			files = append(files, map[string]any{"fid": "last", "cid": "root", "n": "last.txt", "s": "3"})
		default:
			t.Errorf("unexpected page offset: %s", q.Get("offset"))
		}
		w.Header().Set("Content-Type", "application/json")
		offset, _ := strconv.Atoi(q.Get("offset"))
		_ = json.NewEncoder(w).Encode(map[string]any{"state": true, "cid": "root", "count": 1001, "offset": offset, "data": files})
	})
	entries, err := d.List(t.Context(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1001 {
		t.Fatalf("entries = %d, want all 1001", len(entries))
	}
	if got := []string{<-offsets, <-offsets}; !reflect.DeepEqual(got, []string{"0", "1000"}) {
		t.Fatalf("page offsets = %v", got)
	}
	if dir := entries[0]; !dir.IsDir || dir.ID != "subdir" || dir.ParentID != "root" || dir.Name != "folder" || dir.ModTime.Unix() != 1700000000 {
		t.Fatalf("directory mapping = %+v", dir)
	}
	file := entries[1]
	if file.IsDir || file.ID != "1" || file.ParentID != "root" || file.Size != 7 || file.ModTime.IsZero() || !file.UpdatedAt.Equal(file.ModTime) {
		t.Fatalf("file mapping = %+v", file)
	}
	// Exercise list metadata through its real consumers, including wrappers.
	file.Extra = drive.EntryExtraWrapper{RemoteName: file.Name, Raw: file.Extra}
	if pick, err := d.pickCode(t.Context(), file); err != nil || pick != "pick" {
		t.Fatalf("listed pick code = %q err=%v", pick, err)
	}
	if _, hash, err := d.RemoteHash(t.Context(), file); err != nil || hash != "ABC" {
		t.Fatalf("listed remote hash = %q err=%v", hash, err)
	}
	if entries[1000].ID != "last" {
		t.Fatalf("last page missing: %+v", entries[1000])
	}
}

func TestResolvePathWalksDirectoriesOnly(t *testing.T) {
	t.Parallel()
	d := newLocalSDKDriver(t, func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("cid")
		var files []map[string]any
		switch id {
		case "root":
			files = []map[string]any{
				{"fid": "decoy", "cid": "root", "n": "folder"},
				{"cid": "dir", "pid": "root", "n": "folder"},
			}
		case "dir":
			files = []map[string]any{{"cid": "child", "pid": "dir", "n": "nested"}}
		default:
			t.Errorf("unexpected directory id %q", id)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"state": true, "cid": id, "count": len(files), "data": files})
	})
	if id, err := d.ResolvePath(t.Context(), "/folder/nested/"); err != nil || id != "child" {
		t.Fatalf("ResolvePath = %q err=%v", id, err)
	}
	if _, err := d.ResolvePath(t.Context(), "/missing"); !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("missing path error = %v, want ErrNotFound", err)
	}
}

func TestDriverMutationsUseIDsAndPreserveMetadata(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"move", "rename", "remove", "copy"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail=%t", operation, fail), func(t *testing.T) {
				paths := map[string]string{"move": "/files/move", "rename": "/files/batch_rename", "remove": "/rb/delete", "copy": "/files/copy"}
				called := make(chan struct{}, 1)
				d := newLocalSDKDriver(t, func(w http.ResponseWriter, r *http.Request) {
					called <- struct{}{}
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					if r.Method != http.MethodPost || r.URL.Path != paths[operation] {
						t.Errorf("unexpected mutation: %s %s", r.Method, r.URL)
					}
					if operation == "rename" {
						if r.PostForm.Get("fid") != "file-id" || r.PostForm.Get("file_name") != "renamed" {
							t.Errorf("rename form = %v", r.PostForm)
						}
					} else if r.PostForm.Get("fid[0]") != "file-id" {
						t.Errorf("mutation lost backend id: %v", r.PostForm)
					}
					if (operation == "move" || operation == "copy") && r.PostForm.Get("pid") != "root" {
						t.Errorf("destination root not resolved: %v", r.PostForm)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"state": !fail, "errno": 999999, "error": "injected failure"})
				})
				src := drive.Entry{ID: "file-id", ParentID: "old-parent", Name: "original", Size: 7}
				var got drive.Entry
				var err error
				switch operation {
				case "move":
					got, err = d.Move(t.Context(), src, "/")
				case "rename":
					got, err = d.Rename(t.Context(), src, "renamed")
				case "remove":
					err = d.Remove(t.Context(), src)
				case "copy":
					got, err = d.Copy(t.Context(), src, "/", src.Name)
				}
				if fail {
					if err == nil {
						t.Fatal("injected mutation failure was ignored")
					}
					snapshot, snapshotErr := d.DebugSnapshot(t.Context())
					if snapshotErr != nil || snapshot.Health != drive.HealthLevelDegraded {
						t.Fatalf("failed mutation health = %+v err=%v", snapshot, snapshotErr)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					switch operation {
					case "move":
						if got.ID != src.ID || got.ParentID != "root" || got.Name != src.Name || got.Size != src.Size {
							t.Fatalf("move result = %+v", got)
						}
					case "rename":
						if got.ID != src.ID || got.ParentID != src.ParentID || got.Name != "renamed" || got.Size != src.Size {
							t.Fatalf("rename result = %+v", got)
						}
					case "copy":
						if got.Name != src.Name || got.Size != src.Size || got.ModTime.IsZero() {
							t.Fatalf("copy result = %+v", got)
						}
					}
				}
				select {
				case <-called:
				default:
					t.Fatal("SDK endpoint was not called")
				}
			})
		}
	}
}

func TestReadValidatesBeforeNetwork(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name         string
		offset, size int64
		wantErr      bool
	}{
		{name: "negative offset", offset: -1, wantErr: true},
		{name: "negative size", size: -1, wantErr: true},
		{name: "EOF", offset: 10},
		{name: "past EOF", offset: 11},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := New(Options{}) // No SDK client: any network access would panic.
			entry := drive.Entry{Size: 4, Extra: drive.EntryExtraWrapper{Raw: driver115.File{Size: 10}}}
			body, err := d.Read(t.Context(), entry, tt.offset, tt.size)
			if tt.wantErr {
				if err == nil || body != nil {
					t.Fatalf("invalid range returned body=%v err=%v", body, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer body.Close()
			data, err := io.ReadAll(body)
			if err != nil || len(data) != 0 {
				t.Fatalf("EOF read = %q err=%v", data, err)
			}
		})
	}
}

func TestReadDownloadInfoFailure(t *testing.T) {
	t.Parallel()
	for _, notFound := range []bool{true, false} {
		t.Run(fmt.Sprint(notFound), func(t *testing.T) {
			d := newLocalSDKDriver(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/app/chrome/downurl" || r.Method != http.MethodPost {
					t.Errorf("unexpected download-info request: %s %s", r.Method, r.URL)
				}
				if notFound {
					_, _ = io.WriteString(w, `{"state":false,"errno":50015,"error":"文件不存在或已删除。"}`)
				} else {
					_, _ = io.WriteString(w, `{"state":false,"errno":999999,"error":"backend failure"}`)
				}
			})
			entry := drive.Entry{ID: "file", Size: 10, Extra: &driver115.File{PickCode: "pick", Size: 10}}
			body, err := d.Read(t.Context(), entry, 0, 5)
			if body != nil || err == nil {
				t.Fatalf("download info failure returned body=%v err=%v", body, err)
			}
			if errors.Is(err, drive.ErrNotFound) != notFound {
				t.Fatalf("error classification = %v, want not-found=%t", err, notFound)
			}
			if !notFound && !strings.Contains(err.Error(), "download info") {
				t.Fatalf("error missing operation context: %v", err)
			}
		})
	}
}

func TestMkdirReturnsMetadataOrCreatedDirectoryFallback(t *testing.T) {
	t.Parallel()
	for _, metadataFails := range []bool{false, true} {
		t.Run(fmt.Sprint(metadataFails), func(t *testing.T) {
			d := newLocalSDKDriver(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/files/add":
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					if r.PostForm.Get("pid") != "root" || r.PostForm.Get("cname") != "new-dir" {
						t.Errorf("mkdir form = %v", r.PostForm)
					}
					_, _ = io.WriteString(w, `{"state":true,"cid":"created"}`)
				case "/files/get_info":
					if r.URL.Query().Get("file_id") != "created" {
						t.Errorf("metadata queried wrong id: %s", r.URL)
					}
					if metadataFails {
						_, _ = io.WriteString(w, `{"state":false,"errno":999999}`)
					} else {
						_, _ = io.WriteString(w, `{"state":true,"data":[{"cid":"created","pid":"root","n":"new-dir","t":"1700000000"}]}`)
					}
				default:
					t.Errorf("unexpected mkdir endpoint: %s", r.URL)
					http.NotFound(w, r)
				}
			})
			entry, err := d.Mkdir(t.Context(), "/", "new-dir")
			if err != nil || entry.ID != "created" || entry.ParentID != "root" || entry.Name != "new-dir" || !entry.IsDir {
				t.Fatalf("mkdir result = %+v err=%v", entry, err)
			}
			if !metadataFails && entry.ModTime.Unix() != 1700000000 {
				t.Fatalf("mkdir lost server timestamp: %+v", entry)
			}
		})
	}
}

func TestPickCodeFetchesMissingMetadata(t *testing.T) {
	t.Parallel()
	for _, response := range []string{
		`{"state":true,"data":[{"fid":"file","cid":"root","pc":"fetched"}]}`,
		`{"state":true,"data":[{"fid":"file","cid":"root"}]}`,
		`{"state":false,"errno":999999}`,
	} {
		t.Run(response, func(t *testing.T) {
			d := newLocalSDKDriver(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/files/get_info" || r.URL.Query().Get("file_id") != "file" {
					t.Errorf("unexpected metadata request: %s", r.URL)
				}
				_, _ = io.WriteString(w, response)
			})
			pick, err := d.pickCode(t.Context(), drive.Entry{ID: "file"})
			if strings.Contains(response, "fetched") {
				if err != nil || pick != "fetched" {
					t.Fatalf("fetched pick code = %q err=%v", pick, err)
				}
			} else if err == nil || pick != "" {
				t.Fatalf("missing metadata error ignored: pick=%q err=%v", pick, err)
			}
		})
	}
}

func TestWaitUploadedFileMatchesSizeAndHash(t *testing.T) {
	t.Parallel()
	d := newLocalSDKDriver(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cid") != "root" {
			t.Errorf("wrong upload destination listed: %s", r.URL)
		}
		_, _ = io.WriteString(w, `{"state":true,"cid":"root","count":5,"data":[
			{"cid":"dir","pid":"root","n":"upload.txt"},
			{"fid":"other-name","cid":"root","n":"other","s":"5"},
			{"fid":"wrong-size","cid":"root","n":"upload.txt","s":"6"},
			{"fid":"wrong-hash","cid":"root","n":"upload.txt","s":"5","sha":"bad"},
			{"fid":"correct","cid":"root","n":"upload.txt","s":"5","sha":"aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d"}
		]}`)
	})
	entry, err := d.waitUploadedFile(t.Context(), "root", "upload.txt", drive.NewBytesReadOnlyFileSource([]byte("hello")))
	if err != nil || entry.ID != "correct" {
		t.Fatalf("uploaded entry = %+v err=%v, want content-matched file", entry, err)
	}
}
