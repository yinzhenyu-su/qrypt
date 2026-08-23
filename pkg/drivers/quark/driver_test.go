package quark

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	cryptpkg "github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestResolvePathRootUsesConfiguredRootID(t *testing.T) {
	d := &Driver{rootID: "root-fid"}
	got, err := d.ResolvePath(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "root-fid" {
		t.Fatalf("ResolvePath root = %q, want configured root id", got)
	}
}

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
						{"fid": "file-1", "file_name": "a.txt", "file": true, "file_size": 12, "created_at": 1699990000000, "updated_at": 1700000000000},
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
	if entry.CreatedAt.IsZero() || entry.UpdatedAt.IsZero() || !entry.ModTime.Equal(entry.UpdatedAt) {
		t.Fatalf("unexpected entry times: %+v", entry)
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

func TestDriverReadRetriesDownloadWithoutRefreshingURL(t *testing.T) {
	var apiCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file/download" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		apiCalls++
		writeJSON(t, w, map[string]any{
			"status": 200,
			"code":   0,
			"data": []map[string]any{
				{"download_url": "https://download.test/file.bin"},
			},
		})
	}))
	defer api.Close()

	var downloadCalls, actualCalls int
	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	driver.cl.downloadClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		downloadCalls++
		actualCalls++
		if actualCalls == 1 {
			return nil, &net.DNSError{Name: req.URL.Host, Err: "no such host"}
		}
		if got := req.Header.Get("User-Agent"); got != downloadChromeUA {
			t.Fatalf("download user-agent = %q, want Chrome UA", got)
		}
		if got := req.Header.Get("Referer"); got != "https://pan.quark.cn/" {
			t.Fatalf("download referer = %q, want trailing slash", got)
		}
		if got := req.Header.Get("Accept"); got != "*/*" {
			t.Fatalf("download accept = %q, want */*", got)
		}
		if got := req.Header.Get("Range"); got != "bytes=4-8" {
			t.Fatalf("range = %q, want bytes=4-8", got)
		}
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("hello")),
			Request:    req,
		}, nil
	})

	rc, err := driver.Read(context.Background(), drive.Entry{ID: "fid-1", Name: "file.bin", Size: 100}, 4, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("body = %q, want hello", data)
	}
	if apiCalls != 1 {
		t.Fatalf("download URL calls = %d, want 1", apiCalls)
	}
	if downloadCalls != 2 {
		t.Fatalf("download calls = %d, want 2", downloadCalls)
	}
	if _, ok := driver.getURL("fid-1"); !ok {
		t.Fatal("download URL cache was invalidated after transient network error")
	}
	metrics := driver.cl.metricEvents(time.Time{})
	if got := metricCount(metrics, "download_url"); got != 1 {
		t.Fatalf("download_url metric count = %d, want 1", got)
	}
	if got := metricCount(metrics, "download"); got != 2 {
		t.Fatalf("download metric count = %d, want 2", got)
	}
	var sawDownload bool
	for _, event := range metrics {
		if event.Operation != "download" || event.Attempt != 2 {
			continue
		}
		sawDownload = true
		if event.RemoteID != "fid-1" {
			t.Fatalf("download remote_id = %q, want fid-1", event.RemoteID)
		}
		if event.Offset != 4 || event.Requested != 5 {
			t.Fatalf("download offset/size = %d/%d, want 4/5", event.Offset, event.Requested)
		}
		if got := event.Request["range"]; got != "bytes=4-8" {
			t.Fatalf("download range = %v, want bytes=4-8", got)
		}
		if got := event.Request["url_host"]; got != "download.test" {
			t.Fatalf("download url_host = %v, want download.test", got)
		}
		if got := event.Request["url_cache_hit"]; got != false {
			t.Fatalf("download url_cache_hit = %v, want false", got)
		}
	}
	if !sawDownload {
		t.Fatal("missing second download metric")
	}
}

func TestDriverDownloadUsesCookieSnapshotAfterRotation(t *testing.T) {
	var apiCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file/download" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Cookie"); got != "__puus=old" {
			t.Fatalf("download URL request cookie = %q, want old snapshot", got)
		}
		apiCalls++
		w.Header().Add("Set-Cookie", "__puus=new; Path=/")
		writeJSON(t, w, map[string]any{
			"status": 200,
			"code":   0,
			"data": []map[string]any{
				{"download_url": "https://download.test/file.bin"},
			},
		})
	}))
	defer api.Close()

	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	driver := New("__puus=old", Options{BaseURL: api.URL, V2URL: api.URL})
	driver.InstallStateStore(store)
	var downloadCalls int
	driver.cl.downloadClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		downloadCalls++
		if got := req.Header.Get("Cookie"); got != "__puus=old" {
			t.Fatalf("CDN request cookie = %q, want URL snapshot", got)
		}
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("hello")),
			Request:    req,
		}, nil
	})

	for range 2 {
		rc, err := driver.Read(context.Background(), drive.Entry{ID: "fid-1", Name: "file.bin", Size: 100}, 0, 5)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "hello" {
			t.Fatalf("body = %q, want hello", data)
		}
	}

	if apiCalls != 1 {
		t.Fatalf("download URL calls = %d, want 1", apiCalls)
	}
	if downloadCalls != 2 {
		t.Fatalf("CDN download calls = %d, want 2", downloadCalls)
	}
	if got := driver.cl.cookieValue(); got != "__puus=new" {
		t.Fatalf("current cookie = %q, want rotated cookie", got)
	}
	var state cookieState
	if err := store.LoadJSON("quark_cookie.json", &state); err != nil {
		t.Fatal(err)
	}
	if state.Cookie != "__puus=new" {
		t.Fatalf("state cookie = %q, want rotated cookie", state.Cookie)
	}
}

func TestDriverParallelDownloadAssemblesRangeParts(t *testing.T) {
	const total = int64(24 * 1024 * 1024)
	data := make([]byte, total)
	for i := range data {
		data[i] = byte(i)
	}
	driver := New("k=v", Options{})
	var mu sync.Mutex
	var ranges []string
	active := 0
	maxActive := 0
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	driver.cl.downloadClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		value := req.Header.Get("Range")
		var start, end int64
		if _, err := fmt.Sscanf(value, "bytes=%d-%d", &start, &end); err != nil {
			return nil, fmt.Errorf("parse range %q: %w", value, err)
		}
		mu.Lock()
		ranges = append(ranges, value)
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		entered <- struct{}{}
		<-release
		body := append([]byte(nil), data[start:end+1]...)
		mu.Lock()
		active--
		mu.Unlock()
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     http.Header{"Content-Range": []string{fmt.Sprintf("bytes %d-%d/%d", start, end, total)}},
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    req,
		}, nil
	})

	type downloadResult struct {
		rc  io.ReadCloser
		err error
	}
	result := make(chan downloadResult, 1)
	go func() {
		rc, err := driver.downloadParallel(context.Background(), "fid-1", "https://download.test/file.bin", "k=v", false, 0, total)
		result <- downloadResult{rc: rc, err: err}
	}()
	for range 3 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("parallel range workers did not start")
		}
	}
	close(release)
	item := <-result
	rc, err := item.rc, item.err
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("assembled data differs: got=%d want=%d", len(got), len(data))
	}
	if len(ranges) != 3 {
		t.Fatalf("range request count = %d, want 3: %v", len(ranges), ranges)
	}
	wantRanges := []string{
		"bytes=0-8388607",
		"bytes=8388608-16777215",
		"bytes=16777216-25165823",
	}
	slices.Sort(ranges)
	slices.Sort(wantRanges)
	if !slices.Equal(ranges, wantRanges) {
		t.Fatalf("range requests = %v, want %v", ranges, wantRanges)
	}
	if maxActive < 2 {
		t.Fatalf("max concurrent range requests = %d, want at least 2", maxActive)
	}
}

func TestDriverDownloadURLCoalescesConcurrentMisses(t *testing.T) {
	var calls int
	var callsMu sync.Mutex
	requestEntered := make(chan struct{}, 2)
	release := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsMu.Lock()
		calls++
		callsMu.Unlock()
		requestEntered <- struct{}{}
		<-release
		writeJSON(t, w, map[string]any{
			"status": 200,
			"code":   0,
			"data": []map[string]any{
				{"download_url": "https://download.test/file.bin"},
			},
		})
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			result, err := driver.downloadURL(context.Background(), "fid-1")
			if err == nil && result.url != "https://download.test/file.bin" {
				err = fmt.Errorf("download URL = %q", result.url)
			}
			results <- err
		}()
	}
	close(start)
	select {
	case <-requestEntered:
	case <-time.After(time.Second):
		t.Fatal("download URL request did not start")
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	callsMu.Lock()
	defer callsMu.Unlock()
	if calls != 1 {
		t.Fatalf("download URL calls = %d, want 1", calls)
	}
}

func TestDriverInvalidateURLDoesNotDeleteNewerValue(t *testing.T) {
	driver := New("k=v", Options{})
	driver.setURL("fid-1", "https://download.test/fresh")
	driver.invalidateURL("fid-1", "https://download.test/stale")
	if url, ok := driver.getURL("fid-1"); !ok || url != "https://download.test/fresh" {
		t.Fatalf("cached URL = %q/%t, want fresh/true", url, ok)
	}
	driver.invalidateURL("fid-1", "https://download.test/fresh")
	if _, ok := driver.getURL("fid-1"); ok {
		t.Fatal("matching stale URL was not invalidated")
	}
}

func TestDriverReadRefreshesURLAfterForbidden(t *testing.T) {
	var apiCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file/download" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		apiCalls++
		writeJSON(t, w, map[string]any{
			"status": 200,
			"code":   0,
			"data": []map[string]any{
				{"download_url": fmt.Sprintf("https://download.test/file-%d.bin", apiCalls)},
			},
		})
	}))
	defer api.Close()

	var downloadCalls int
	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	driver.cl.downloadClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		downloadCalls++
		status := http.StatusPartialContent
		body := "fresh"
		if downloadCalls == 1 {
			status = http.StatusForbidden
			body = "expired"
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})

	rc, err := driver.Read(context.Background(), drive.Entry{ID: "fid-1", Name: "file.bin", Size: 100}, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fresh" {
		t.Fatalf("body = %q, want fresh", data)
	}
	if apiCalls != 2 {
		t.Fatalf("download URL calls = %d, want 2", apiCalls)
	}
	if downloadCalls != 2 {
		t.Fatalf("download calls = %d, want 2", downloadCalls)
	}
}

func TestDriverReadWarmsDownloadConnectionOnce(t *testing.T) {
	driver := New("k=v", Options{})
	driver.setURL("fid-1", "https://download.test/file.bin")
	var ranges []string
	driver.cl.downloadClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		ranges = append(ranges, req.Header.Get("Range"))
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("hello")),
			Request:    req,
		}, nil
	})

	for _, offset := range []int64{4, 9} {
		rc, err := driver.Read(context.Background(), drive.Entry{ID: "fid-1", Name: "file.bin", Size: 2 << 20}, offset, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if data, err := io.ReadAll(rc); err != nil || string(data) != "hello" {
			t.Fatalf("read at %d = %q/%v, want hello/nil", offset, data, err)
		}
		_ = rc.Close()
	}

	if got, want := ranges, []string{"bytes=0-0", "bytes=4-1048579", "bytes=9-1048584"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("download ranges = %v, want %v", got, want)
	}
	if got := metricCount(driver.cl.metricEvents(time.Time{}), "download_warmup"); got != 1 {
		t.Fatalf("download warmup metrics = %d, want 1", got)
	}
}

func TestDriverDebugSnapshot(t *testing.T) {
	driver := New("k=v", Options{RootID: "root", RootPath: "/Docs"})
	snapshot, err := driver.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Driver != "quark" {
		t.Fatalf("driver = %q, want quark", snapshot.Driver)
	}
	if snapshot.Stats[drive.DebugStatRootID] != "root" {
		t.Fatalf("unexpected stats: %+v", snapshot.Stats)
	}
	if snapshot.Stats[drive.DebugStatRootPath] != "/Docs" {
		t.Fatalf("unexpected stats: %+v", snapshot.Stats)
	}
	if snapshot.Extra[drive.DebugExtraCredentialSource] == nil {
		t.Fatalf("expected credential_source extra, got %+v", snapshot.Extra)
	}
}

func TestCookieUpdatePersistsState(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	driver := New("k=v", Options{})
	driver.InstallStateStore(store)
	driver.cl.updateCookie("__puus", "new")
	var state cookieState
	if err := store.LoadJSON("quark_cookie.json", &state); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state.Cookie, "__puus=new") {
		t.Fatalf("cookie state = %q, want __puus=new", state.Cookie)
	}
	if driver.cookieSource != "response" {
		t.Fatalf("cookieSource = %q, want response", driver.cookieSource)
	}
}

func TestLoadCookieStateOverridesConfigCookie(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("quark_cookie.json", cookieState{
		Cookie:    "stored=1; __puus=stored",
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	driver := New("config=1", Options{})
	driver.InstallStateStore(store)
	driver.loadCookieState()
	if got := driver.cl.cookieValue(); got != "stored=1; __puus=stored" {
		t.Fatalf("cookie = %q, want stored cookie", got)
	}
	if driver.cookieSource != "state" {
		t.Fatalf("cookieSource = %q, want state", driver.cookieSource)
	}
}

func TestOSSClientHasNoWholeRequestTimeout(t *testing.T) {
	client := newOSSClient()
	if client.Timeout != 0 {
		t.Fatalf("oss client timeout = %s, want no whole-request timeout", client.Timeout)
	}
}

func TestOSSRetryDelayBacksOffForNetworkOutages(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: -1, want: 30 * time.Second},
		{attempt: 0, want: 30 * time.Second},
		{attempt: 1, want: time.Minute},
		{attempt: 2, want: 2 * time.Minute},
		{attempt: 3, want: 2 * time.Minute},
	}
	for _, tt := range tests {
		if got := ossRetryDelay(tt.attempt); got != tt.want {
			t.Fatalf("ossRetryDelay(%d) = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}

func TestQuarkUploadConflictIsRetryable(t *testing.T) {
	if nonRetryableUploadStatus(http.StatusConflict) {
		t.Fatal("409 conflict should not be marked non-retryable; OSS upload sessions can be stale")
	}
	if !nonRetryableUploadStatus(http.StatusBadRequest) {
		t.Fatal("400 bad request should remain non-retryable")
	}
}

func TestResumedUploadConflictDeletesSessionAndRetriesFromScratch(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	driver := New("k=v", Options{})
	driver.InstallStateStore(store)
	session := quarkUploadSession{
		Key:       "session-key",
		ParentID:  "parent",
		Name:      "data.bin",
		Size:      1,
		TaskID:    "task-old",
		UploadID:  "upload-old",
		ObjKey:    "obj-old",
		UploadURL: "upload.example",
		AuthInfo:  "auth",
		PartSize:  4 * 1024 * 1024,
		Etags:     map[int]string{},
	}
	driver.saveUploadSession(session)

	err := driver.resumedUploadSessionError(true, session.Key, uploadStatusError{op: "upload part 1", status: http.StatusConflict})
	if err == nil {
		t.Fatal("expected retryable invalid session error")
	}
	if drive.IsNonRetryable(err) {
		t.Fatalf("invalid resumed session error should be retryable, got %v", err)
	}
	if _, ok := driver.loadUploadSession(session.Key); ok {
		t.Fatal("stale resumed upload session was not deleted")
	}
}

func TestUploadSessionPruneOnInstallStateStore(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	now := time.Now()
	old := quarkUploadSession{
		Key:       "old",
		Name:      "old.bin",
		UploadID:  "upload-old",
		PartSize:  4 * 1024 * 1024,
		Etags:     map[int]string{1: "etag-old"},
		UpdatedAt: now.Add(-quarkUploadSessionMaxAge - time.Minute),
	}
	empty := quarkUploadSession{
		Key:       "empty",
		Name:      "empty.bin",
		UploadID:  "upload-empty",
		PartSize:  4 * 1024 * 1024,
		UpdatedAt: now,
	}
	fresh := quarkUploadSession{
		Key:       "fresh",
		Name:      "fresh.bin",
		UploadID:  "upload-fresh",
		PartSize:  4 * 1024 * 1024,
		Etags:     map[int]string{1: "etag-fresh"},
		UpdatedAt: now,
	}
	if err := store.SaveJSON(quarkUploadSessionStateFile, uploadSessionState{
		Version: 1,
		Sessions: map[string]quarkUploadSession{
			old.Key:   old,
			empty.Key: empty,
			fresh.Key: fresh,
		},
	}); err != nil {
		t.Fatal(err)
	}

	driver := New("k=v", Options{})
	driver.InstallStateStore(store)

	state := uploadSessionState{}
	if err := store.LoadJSON(quarkUploadSessionStateFile, &state); err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Sessions[old.Key]; ok {
		t.Fatal("expired upload session was not pruned")
	}
	if _, ok := state.Sessions[empty.Key]; ok {
		t.Fatal("empty upload session was not pruned")
	}
	if _, ok := state.Sessions[fresh.Key]; !ok {
		t.Fatal("fresh upload session was pruned")
	}
}

func TestUploadSessionPruneCapsOldestEntries(t *testing.T) {
	driver := New("k=v", Options{})
	now := time.Now()
	state := uploadSessionState{Version: 1, Sessions: map[string]quarkUploadSession{}}
	for i := 0; i < quarkUploadSessionMaxEntries+2; i++ {
		key := fmt.Sprintf("session-%04d", i)
		state.Sessions[key] = quarkUploadSession{
			Key:       key,
			Name:      key + ".bin",
			UploadID:  key,
			PartSize:  4 * 1024 * 1024,
			Etags:     map[int]string{1: "etag"},
			UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}
	}

	pruned, changed := driver.prunedUploadSessions(state, now)
	if !changed {
		t.Fatal("expected cap pruning to report changed")
	}
	if got := len(pruned.Sessions); got != quarkUploadSessionMaxEntries {
		t.Fatalf("session count = %d, want %d", got, quarkUploadSessionMaxEntries)
	}
	if _, ok := pruned.Sessions["session-0000"]; ok {
		t.Fatal("oldest upload session was not pruned")
	}
	if _, ok := pruned.Sessions[fmt.Sprintf("session-%04d", quarkUploadSessionMaxEntries+1)]; !ok {
		t.Fatal("newest upload session was pruned")
	}
}

func TestDriverPutInstantUploadFinishes(t *testing.T) {
	var finishCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id": "task-1",
					"obj_key": "obj-1",
					"fid":     "pre-fid",
					"finish":  true,
				},
			})
		case "/file/upload/finish":
			finishCalled = true
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-fid"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	driver := New("k=v", Options{BaseURL: server.URL, V2URL: server.URL})
	localPath := filepath.Join(t.TempDir(), "same.bin")
	if err := os.WriteFile(localPath, []byte("unused"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := driver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "same.bin",
		Source:   drive.NewLocalReadOnlyFileSource(localPath, 6),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !finishCalled {
		t.Fatal("finish was not called")
	}
	if entry.ID != "final-fid" || entry.ParentID != "parent" || entry.Name != "same.bin" || entry.Size != 6 {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.ModTime.IsZero() {
		t.Fatal("instant upload entry modtime is zero")
	}
}

func TestDriverPutMultipartUpload(t *testing.T) {
	var partsMu sync.Mutex
	parts := map[string][]byte{}
	var completed bool
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/obj-1" {
			t.Fatalf("unexpected oss path: %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			partsMu.Lock()
			parts[r.URL.Query().Get("partNumber")] = data
			partsMu.Unlock()
			w.Header().Set("Etag", "etag-"+r.URL.Query().Get("partNumber"))
			w.WriteHeader(http.StatusOK)
		case http.MethodPost:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"etag-1", "etag-2", "etag-3"} {
				if !bytes.Contains(data, []byte(want)) {
					t.Fatalf("complete body missing %s: %s", want, data)
				}
			}
			completed = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected oss method: %s", r.Method)
		}
	}))
	defer oss.Close()

	var authMu sync.Mutex
	var authCalls int
	var hashCalled bool
	var finishCalled bool
	content := []byte("abcdefgh")
	contentMD5 := md5.Sum(content)
	contentSHA1 := sha1.Sum(content)
	wantMD5 := fmt.Sprintf("%X", contentMD5[:])
	wantSHA1 := fmt.Sprintf("%X", contentSHA1[:])
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id":    "task-1",
					"upload_id":  "upload-1",
					"obj_key":    "obj-1",
					"upload_url": strings.TrimPrefix(oss.URL, "https://"),
					"fid":        "pre-fid",
					"bucket":     "bucket",
					"callback":   json.RawMessage(`{}`),
					"auth_info":  "auth-info",
				},
				"metadata": map[string]any{"part_size": 3},
			})
		case "/file/upload/auth":
			authMu.Lock()
			authCalls++
			authMu.Unlock()
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"auth_key": "auth-key"},
			})
		case "/file/update/hash":
			hashCalled = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["md5"] != wantMD5 || body["sha1"] != wantSHA1 {
				t.Fatalf("unexpected hash body: %+v, want md5=%s sha1=%s", body, wantMD5, wantSHA1)
			}
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"finish": false},
			})
		case "/file/upload/finish":
			finishCalled = true
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-fid"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	routeOSSToTestServer(driver.cl.ossClient, oss)

	tmp := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := driver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "data.bin",
		Source:   drive.NewLocalReadOnlyFileSource(tmp, int64(len(content))),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "final-fid" || entry.ParentID != "parent" || entry.Name != "data.bin" || entry.Size != 8 {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.ModTime.IsZero() {
		t.Fatal("multipart upload entry modtime is zero")
	}
	if got := len(parts); got != 3 {
		t.Fatalf("part count = %d, want 3", got)
	}
	if got := string(parts["1"]) + string(parts["2"]) + string(parts["3"]); got != "abcdefgh" {
		t.Fatalf("uploaded data = %q, want abcdefgh", got)
	}
	authMu.Lock()
	gotAuthCalls := authCalls
	authMu.Unlock()
	if gotAuthCalls != 4 {
		t.Fatalf("auth calls = %d, want 4", gotAuthCalls)
	}
	if !hashCalled {
		t.Fatal("hash update was not called")
	}
	if !completed {
		t.Fatal("oss complete was not called")
	}
	if !finishCalled {
		t.Fatal("finish was not called")
	}
}

func TestDriverPutMultipartUploadResumesPersistedParts(t *testing.T) {
	var partsMu sync.Mutex
	partUploads := map[string]int{}
	failPart2 := true
	var completed bool
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/obj-resume" {
			t.Fatalf("unexpected oss path: %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodPut:
			partNumber := r.URL.Query().Get("partNumber")
			_, _ = io.ReadAll(r.Body)
			partsMu.Lock()
			partUploads[partNumber]++
			partsMu.Unlock()
			partsMu.Lock()
			shouldFail := failPart2 && partNumber == "2"
			partsMu.Unlock()
			if partNumber == "2" {
				if shouldFail {
					<-r.Context().Done()
					return
				}
			}
			w.Header().Set("Etag", "etag-"+partNumber)
			w.WriteHeader(http.StatusOK)
		case http.MethodPost:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"etag-1", "etag-2", "etag-3"} {
				if !bytes.Contains(data, []byte(want)) {
					t.Fatalf("complete body missing %s: %s", want, data)
				}
			}
			completed = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected oss method: %s", r.Method)
		}
	}))
	defer oss.Close()

	var apiMu sync.Mutex
	var preCalls int
	var hashCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			apiMu.Lock()
			preCalls++
			apiMu.Unlock()
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id":    "task-resume",
					"upload_id":  "upload-resume",
					"obj_key":    "obj-resume",
					"upload_url": strings.TrimPrefix(oss.URL, "https://"),
					"fid":        "pre-fid",
					"bucket":     "bucket",
					"callback":   json.RawMessage(`{}`),
					"auth_info":  "auth-info",
				},
				"metadata": map[string]any{"part_size": 3},
			})
		case "/file/upload/auth":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"auth_key": "auth-key"},
			})
		case "/file/update/hash":
			apiMu.Lock()
			hashCalls++
			apiMu.Unlock()
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"finish": false},
			})
		case "/file/upload/finish":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-fid"},
			})
		default:
			t.Fatalf("unexpected api path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	content := []byte("abcdefghi")
	tmp := filepath.Join(t.TempDir(), "resume.bin")
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		t.Fatal(err)
	}
	md5Sum := md5.Sum(content)
	sha1Sum := sha1.Sum(content)
	source := drive.NewLocalReadOnlyFileSourceWithHashes(tmp, int64(len(content)), drive.SourceHashes{
		drive.HashMD5:  md5Sum[:],
		drive.HashSHA1: sha1Sum[:],
	})
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))

	first := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	first.InstallStateStore(store)
	routeOSSToTestServer(first.cl.ossClient, oss)
	firstCtx, cancelFirst := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancelFirst()
	if _, err := first.PutSource(firstCtx, drive.UploadRequest{ParentID: "parent", Name: "resume.bin", Source: source}); err == nil {
		t.Fatal("first upload unexpectedly succeeded")
	}

	partsMu.Lock()
	failPart2 = false
	if partUploads["1"] != 1 {
		t.Fatalf("part 1 uploads after first attempt = %d, want 1", partUploads["1"])
	}
	partsMu.Unlock()

	second := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	second.InstallStateStore(store)
	routeOSSToTestServer(second.cl.ossClient, oss)
	entry, err := second.PutSource(context.Background(), drive.UploadRequest{ParentID: "parent", Name: "resume.bin", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "final-fid" || entry.Size != int64(len(content)) {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if !completed {
		t.Fatal("multipart upload was not completed")
	}
	partsMu.Lock()
	defer partsMu.Unlock()
	if partUploads["1"] != 1 {
		t.Fatalf("part 1 was reuploaded: count=%d", partUploads["1"])
	}
	if partUploads["2"] < 2 || partUploads["3"] != 1 {
		t.Fatalf("unexpected resumed upload counts: %+v", partUploads)
	}
	apiMu.Lock()
	defer apiMu.Unlock()
	if preCalls != 1 || hashCalls != 1 {
		t.Fatalf("pre/hash calls = %d/%d, want 1/1", preCalls, hashCalls)
	}
}

func TestDriverPutSourceUnderCryptHashesEncryptedSource(t *testing.T) {
	var uploadedCipher []byte
	var completed bool
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			uploadedCipher = append(uploadedCipher[:0], data...)
			w.Header().Set("Etag", "etag-1")
			w.WriteHeader(http.StatusOK)
		case http.MethodPost:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(data, []byte("etag-1")) {
				t.Fatalf("complete body missing etag-1: %s", data)
			}
			completed = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected oss method: %s", r.Method)
		}
	}))
	defer oss.Close()

	plain := []byte("plain payload")
	plainMD5 := md5.Sum(plain)
	plainSHA1 := sha1.Sum(plain)
	plainMD5Hex := fmt.Sprintf("%X", plainMD5[:])
	plainSHA1Hex := fmt.Sprintf("%X", plainSHA1[:])
	var hashCalled bool
	var hashBody map[string]any
	var finishCalled bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id":    "task-crypt",
					"upload_id":  "upload-crypt",
					"obj_key":    "obj-crypt",
					"upload_url": strings.TrimPrefix(oss.URL, "https://"),
					"fid":        "pre-fid",
					"bucket":     "bucket",
					"callback":   json.RawMessage(`{}`),
					"auth_info":  "auth-info",
				},
				"metadata": map[string]any{"part_size": 64},
			})
		case "/file/upload/auth":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"auth_key": "auth-key"},
			})
		case "/file/update/hash":
			hashCalled = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["md5"] == plainMD5Hex || body["sha1"] == plainSHA1Hex {
				t.Fatalf("hash body used plaintext hash: %+v", body)
			}
			hashBody = body
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"finish": false},
			})
		case "/file/upload/finish":
			finishCalled = true
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-fid"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	routeOSSToTestServer(driver.cl.ossClient, oss)
	cp, err := cryptpkg.NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	cryptDriver := cryptpkg.NewDriver(driver, cp, cryptpkg.DriverOptions{})
	tmp := filepath.Join(t.TempDir(), "plain.bin")
	if err := os.WriteFile(tmp, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := cryptDriver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "secret.bin",
		Source:   drive.NewLocalReadOnlyFileSource(tmp, int64(len(plain))),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "final-fid" || entry.ParentID != "parent" || entry.Name != "secret.bin" || entry.Size != int64(len(plain)) {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if bytes.Contains(uploadedCipher, plain) {
		t.Fatal("uploaded body contains plaintext")
	}
	cipherMD5 := md5.Sum(uploadedCipher)
	cipherSHA1 := sha1.Sum(uploadedCipher)
	wantMD5 := fmt.Sprintf("%X", cipherMD5[:])
	wantSHA1 := fmt.Sprintf("%X", cipherSHA1[:])
	if hashBody["md5"] != wantMD5 || hashBody["sha1"] != wantSHA1 {
		t.Fatalf("unexpected encrypted hash body: %+v, want md5=%s sha1=%s", hashBody, wantMD5, wantSHA1)
	}
	if !hashCalled {
		t.Fatal("hash update was not called")
	}
	if !completed {
		t.Fatal("oss complete was not called")
	}
	if !finishCalled {
		t.Fatal("finish was not called")
	}
}

func TestDriverPutSourceUnderContentDedupCryptInstantUploadsByEncryptedHash(t *testing.T) {
	ossCalled := false
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ossCalled = true
		t.Fatalf("oss should not be called for hash-finished upload: %s %s", r.Method, r.URL)
	}))
	defer oss.Close()

	plain := []byte("same plaintext should instant upload when encrypted content hash already exists")
	cp, err := cryptpkg.NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	encrypted := encryptForTest(t, cp, plain)
	encryptedMD5 := md5.Sum(encrypted)
	encryptedSHA1 := sha1.Sum(encrypted)
	wantMD5 := fmt.Sprintf("%X", encryptedMD5[:])
	wantSHA1 := fmt.Sprintf("%X", encryptedSHA1[:])
	var hashCalled bool
	var finishCalled bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id":    "task-dedup",
					"upload_id":  "upload-dedup",
					"obj_key":    "obj-dedup",
					"upload_url": strings.TrimPrefix(oss.URL, "https://"),
					"fid":        "pre-fid",
					"bucket":     "bucket",
					"callback":   json.RawMessage(`{}`),
					"auth_info":  "auth-info",
				},
				"metadata": map[string]any{"part_size": 64},
			})
		case "/file/update/hash":
			hashCalled = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["md5"] != wantMD5 || body["sha1"] != wantSHA1 {
				t.Fatalf("unexpected encrypted hash body: %+v, want md5=%s sha1=%s", body, wantMD5, wantSHA1)
			}
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"finish": true, "fid": "existing-fid"},
			})
		case "/file/upload/finish":
			finishCalled = true
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-existing-fid"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	routeOSSToTestServer(driver.cl.ossClient, oss)
	cryptDriver := cryptpkg.NewDriver(driver, cp, cryptpkg.DriverOptions{ContentDedup: true})
	entry, err := cryptDriver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "secret.bin",
		Source:   drive.NewBytesReadOnlyFileSource(plain),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "final-existing-fid" || entry.ParentID != "parent" || entry.Name != "secret.bin" || entry.Size != int64(len(plain)) {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if !hashCalled {
		t.Fatal("hash update was not called")
	}
	if !finishCalled {
		t.Fatal("finish was not called")
	}
	if ossCalled {
		t.Fatal("oss was called")
	}
}

func encryptForTest(t *testing.T, cp cryptpkg.Cipher, plain []byte) []byte {
	t.Helper()
	plainSHA256 := sha256.Sum256(plain)
	nonce, err := cp.ContentDedupNonce(plainSHA256, int64(len(plain)))
	if err != nil {
		t.Fatal(err)
	}
	encrypted := make([]byte, 0, cp.EncryptedSize(int64(len(plain))))
	encrypted = append(encrypted, []byte(cryptpkg.FileMagic)...)
	encrypted = append(encrypted, nonce[:]...)
	for blockIndex, offset := uint64(0), 0; offset < len(plain); blockIndex, offset = blockIndex+1, offset+cryptpkg.BlockDataSize {
		end := offset + cryptpkg.BlockDataSize
		if end > len(plain) {
			end = len(plain)
		}
		block, err := cp.EncryptBlock(plain[offset:end], blockIndex, nonce)
		if err != nil {
			t.Fatal(err)
		}
		encrypted = append(encrypted, block...)
	}
	return encrypted
}

func TestDriverPutRespectsServerPartSize(t *testing.T) {
	var partsMu sync.Mutex
	var partSizes []int
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			partsMu.Lock()
			partSizes = append(partSizes, len(data))
			partsMu.Unlock()
			w.Header().Set("Etag", "etag-"+r.URL.Query().Get("partNumber"))
			w.WriteHeader(http.StatusOK)
		case http.MethodPost:
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected oss method: %s", r.Method)
		}
	}))
	defer oss.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id":    "task-1",
					"upload_id":  "upload-1",
					"obj_key":    "obj-1",
					"upload_url": strings.TrimPrefix(oss.URL, "https://"),
					"fid":        "pre-fid",
					"bucket":     "bucket",
					"callback":   json.RawMessage(`{}`),
					"auth_info":  "auth-info",
				},
				"metadata": map[string]any{"part_size": 4 * 1024 * 1024},
			})
		case "/file/upload/auth":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"auth_key": "auth-key"},
			})
		case "/file/update/hash":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"finish": false},
			})
		case "/file/upload/finish":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-fid"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	routeOSSToTestServer(driver.cl.ossClient, oss)

	localPath := filepath.Join(t.TempDir(), "data.bin")
	if err := os.WriteFile(localPath, []byte(strings.Repeat("a", 12*1024*1024)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "data.bin",
		Source:   drive.NewLocalReadOnlyFileSource(localPath, 12*1024*1024),
	}); err != nil {
		t.Fatal(err)
	}
	partsMu.Lock()
	defer partsMu.Unlock()
	if len(partSizes) != 3 {
		t.Fatalf("part count = %d, want 3 (server 4MB part size); sizes=%v", len(partSizes), partSizes)
	}
	for i, got := range partSizes {
		if got != 4*1024*1024 {
			t.Fatalf("part %d size = %d, want %d", i+1, got, 4*1024*1024)
		}
	}
}

func TestDriverPutSourceStreamsMultipartWithoutPartSizedReads(t *testing.T) {
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Etag", "etag-"+r.URL.Query().Get("partNumber"))
		w.WriteHeader(http.StatusOK)
	}))
	defer oss.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id":    "task-1",
					"upload_id":  "upload-1",
					"obj_key":    "obj-1",
					"upload_url": strings.TrimPrefix(oss.URL, "https://"),
					"fid":        "pre-fid",
					"bucket":     "bucket",
					"callback":   json.RawMessage(`{}`),
					"auth_info":  "auth-info",
				},
				"metadata": map[string]any{"part_size": 4 * 1024 * 1024},
			})
		case "/file/upload/auth":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"auth_key": "auth-key"},
			})
		case "/file/update/hash":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"finish": false},
			})
		case "/file/upload/finish":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-fid"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	routeOSSToTestServer(driver.cl.ossClient, oss)
	source := newTrackingReadOnlyFileSource(bytes.Repeat([]byte("a"), 4*1024*1024+17))
	if _, err := driver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "data.bin",
		Source:   source,
	}); err != nil {
		t.Fatal(err)
	}
	if got := source.maxRead(); got >= 1024*1024 {
		t.Fatalf("max source read buffer = %d, want streaming reads below 1MiB", got)
	}
}

func TestDriverUploadPartUsesNativeBandwidthLimiter(t *testing.T) {
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("unexpected oss method: %s", r.Method)
		}
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Etag", "etag-1")
		w.WriteHeader(http.StatusOK)
	}))
	defer oss.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file/upload/auth" {
			t.Fatalf("unexpected api path: %s", r.URL.Path)
		}
		writeJSON(t, w, map[string]any{
			"status": 200,
			"code":   0,
			"data":   map[string]any{"auth_key": "auth-key"},
		})
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	routeOSSToTestServer(driver.cl.ossClient, oss)
	driver.InstallBandwidthLimiter(drive.NewBandwidthLimiter(drive.BandwidthLimits{UploadBytesPerSecond: 1}))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	pre := &upPreResp{}
	pre.Data.TaskID = "task-1"
	pre.Data.UploadID = "upload-1"
	pre.Data.ObjKey = "obj"
	pre.Data.UploadURL = strings.TrimPrefix(oss.URL, "https://")
	pre.Data.Bucket = "bucket"
	pre.Data.AuthInfo = "auth-info"

	_, err := driver.uploadPart(ctx, pre, 1, int64(len("slow")), func() (io.Reader, error) {
		return strings.NewReader("slow"), nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("uploadPart error = %v, want context deadline exceeded", err)
	}
}

func TestOssURLWithBucketPrefixesHost(t *testing.T) {
	pre := &upPreResp{}
	pre.Data.Bucket = "ul-sz"
	pre.Data.UploadURL = "pds.quark.cn"
	pre.Data.ObjKey = "path/to/file.bin"
	got := ossURL(pre)
	want := "https://ul-sz.pds.quark.cn/path/to/file.bin"
	if got != want {
		t.Fatalf("ossURL = %q, want %q", got, want)
	}
}

func TestOssURLWithoutBucket(t *testing.T) {
	pre := &upPreResp{}
	pre.Data.Bucket = ""
	pre.Data.UploadURL = "endpoint.quark.cn"
	pre.Data.ObjKey = "obj"
	got := ossURL(pre)
	want := "https://endpoint.quark.cn/obj"
	if got != want {
		t.Fatalf("ossURL = %q, want %q", got, want)
	}
}

type trackingReadOnlyFileSource struct {
	data []byte
	mu   sync.Mutex
	max  int
}

func newTrackingReadOnlyFileSource(data []byte) *trackingReadOnlyFileSource {
	return &trackingReadOnlyFileSource{data: append([]byte(nil), data...)}
}

func (s *trackingReadOnlyFileSource) Size() int64 {
	return int64(len(s.data))
}

func (s *trackingReadOnlyFileSource) Open(ctx context.Context) (drive.ReadOnlyFile, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return &trackingReadOnlyFile{source: s, reader: bytes.NewReader(s.data)}, nil
}

func (s *trackingReadOnlyFileSource) Hash(algorithm drive.HashAlgorithm) ([]byte, bool) {
	switch algorithm {
	case drive.HashMD5:
		sum := md5.Sum(s.data)
		return sum[:], true
	case drive.HashSHA1:
		sum := sha1.Sum(s.data)
		return sum[:], true
	default:
		return nil, false
	}
}

func (s *trackingReadOnlyFileSource) recordRead(size int) {
	s.mu.Lock()
	if size > s.max {
		s.max = size
	}
	s.mu.Unlock()
}

func (s *trackingReadOnlyFileSource) maxRead() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.max
}

type trackingReadOnlyFile struct {
	source *trackingReadOnlyFileSource
	reader *bytes.Reader
}

func (f *trackingReadOnlyFile) Read(p []byte) (int, error) {
	f.source.recordRead(len(p))
	return f.reader.Read(p)
}

func (f *trackingReadOnlyFile) ReadAt(p []byte, off int64) (int, error) {
	f.source.recordRead(len(p))
	return f.reader.ReadAt(p, off)
}

func (f *trackingReadOnlyFile) Seek(offset int64, whence int) (int64, error) {
	return f.reader.Seek(offset, whence)
}

func (f *trackingReadOnlyFile) Close() error {
	return nil
}

func TestOssURLStripsProtocol(t *testing.T) {
	pre := &upPreResp{}
	pre.Data.Bucket = "ul-sz"
	pre.Data.UploadURL = "http://pds.quark.cn"
	pre.Data.ObjKey = "obj"
	got := ossURL(pre)
	want := "https://ul-sz.pds.quark.cn/obj"
	if got != want {
		t.Fatalf("ossURL = %q, want %q", got, want)
	}
}

func TestOssURLStripsPathFromUploadURL(t *testing.T) {
	pre := &upPreResp{}
	pre.Data.Bucket = "ul-sz"
	pre.Data.UploadURL = "https://pds.quark.cn/some/path"
	pre.Data.ObjKey = "obj"
	got := ossURL(pre)
	want := "https://ul-sz.pds.quark.cn/obj"
	if got != want {
		t.Fatalf("ossURL = %q, want %q", got, want)
	}
}

func TestOssURLWithRealWorldExample(t *testing.T) {
	pre := &upPreResp{}
	pre.Data.Bucket = "ul-sz"
	pre.Data.UploadURL = "http://pds.quark.cn"
	pre.Data.ObjKey = "j0uOasD0/5936150331/e410edbb116847b08f047d6474aa52396a3cefab/6a3cefab53760f59fd5b42be8a069dd70859b8b9"
	got := ossURL(pre)
	want := "https://ul-sz.pds.quark.cn/j0uOasD0/5936150331/e410edbb116847b08f047d6474aa52396a3cefab/6a3cefab53760f59fd5b42be8a069dd70859b8b9"
	if got != want {
		t.Fatalf("ossURL = %q, want %q", got, want)
	}
}

func routeOSSToTestServer(c *http.Client, server *httptest.Server) {
	targetAddr := strings.TrimPrefix(server.URL, "https://")
	baseTransport := c.Transport.(*http.Transport)
	baseTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	baseDial := baseTransport.DialContext
	baseTransport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if strings.HasSuffix(addr, targetAddr) {
			addr = targetAddr
		}
		return baseDial(ctx, network, addr)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func metricCount(events []drive.MetricEvent, operation string) int {
	var count int
	for _, event := range events {
		if event.Operation == operation {
			count++
		}
	}
	return count
}

func TestValidateWithFallbackFallsBackToConfigCookie(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("quark_cookie.json", cookieState{Cookie: "stored=1"}); err != nil {
		t.Fatal(err)
	}
	driver := New("config=1", Options{})
	driver.InstallStateStore(store)
	driver.loadCookieState()
	if driver.cookie != "stored=1" {
		t.Fatalf("precondition: cookie = %q, want stored state cookie", driver.cookie)
	}

	want := errors.New("bad cookie")
	calls := 0
	err := driver.validateWithFallback(context.Background(), "config=1", func() error {
		calls++
		if calls == 1 {
			return want // state cookie fails
		}
		return nil // config cookie succeeds
	})
	if err != nil {
		t.Fatalf("validateWithFallback error = %v, want nil after config fallback", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (state + config retry)", calls)
	}
	if driver.cookie != "config=1" {
		t.Fatalf("cookie = %q, want config cookie after fallback", driver.cookie)
	}
	var state cookieState
	if err := store.LoadJSON("quark_cookie.json", &state); err != nil {
		t.Fatal(err)
	}
	if state.Cookie != "config=1" {
		t.Fatalf("persisted cookie = %q, want working config cookie", state.Cookie)
	}
}

func TestValidateWithFallbackBothFailReturnsError(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("quark_cookie.json", cookieState{Cookie: "stored=1"}); err != nil {
		t.Fatal(err)
	}
	driver := New("config=1", Options{})
	driver.InstallStateStore(store)
	driver.loadCookieState()

	want := errors.New("bad cookie")
	calls := 0
	err := driver.validateWithFallback(context.Background(), "config=1", func() error {
		calls++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("validateWithFallback error = %v, want %v", err, want)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (state and config both fail)", calls)
	}
}

func TestValidateWithFallbackNoFallbackWithoutConfigCookie(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("quark_cookie.json", cookieState{Cookie: "stored=1"}); err != nil {
		t.Fatal(err)
	}
	driver := New("", Options{})
	driver.InstallStateStore(store)
	driver.loadCookieState()

	want := errors.New("bad cookie")
	calls := 0
	err := driver.validateWithFallback(context.Background(), "", func() error {
		calls++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("validateWithFallback error = %v, want %v", err, want)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no config cookie to retry with)", calls)
	}
}

func TestDriverSpaceParsesEnvelope(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/member" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		writeJSON(t, w, map[string]any{
			"status": 200,
			"code":   0,
			"data": map[string]any{
				"total_capacity": int64(10995116277760), // 10 TiB
				"use_capacity":   int64(1234567890),
			},
		})
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	space, err := driver.Space(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if space.Total != 10995116277760 {
		t.Fatalf("total = %d, want 10995116277760 (capacity lives under data envelope)", space.Total)
	}
	if want := int64(10995116277760 - 1234567890); space.Free != want {
		t.Fatalf("free = %d, want %d", space.Free, want)
	}
}

func TestDriverSpaceReportsAPIError(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"status":  400,
			"code":    23001,
			"message": "not found",
			"data":    map[string]any{},
		})
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	if _, err := driver.Space(context.Background()); err == nil {
		t.Fatal("Space must surface the API error envelope")
	}
}

func TestDriverServerSideCopy(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/copy":
			calls = append(calls, "copy")
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("copy body: %v", err)
			}
			if body["action_type"] != float64(1) || body["to_pdir_fid"] != "dst-dir" {
				t.Fatalf("copy body = %#v", body)
			}
			filelist, ok := body["filelist"].([]any)
			if !ok || len(filelist) != 1 || filelist[0] != "src-fid" {
				t.Fatalf("copy filelist = %#v", body["filelist"])
			}
			writeJSON(t, w, map[string]any{"status": 200, "code": 0, "data": map[string]any{"task_id": ""}})
		case "/file/sort":
			calls = append(calls, "list")
			writeJSON(t, w, map[string]any{
				"status":   200,
				"code":     0,
				"metadata": map[string]any{"total": 1},
				"data": map[string]any{
					"list": []map[string]any{
						{"fid": "copied-fid", "file_name": "dst.txt", "file": false, "size": 4},
					},
				},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d := New("k=v", Options{BaseURL: server.URL, V2URL: server.URL})
	dst, err := d.Copy(context.Background(), drive.Entry{ID: "src-fid", Name: "src.txt", Size: 4}, "dst-dir", "dst.txt")
	if err != nil {
		t.Fatal(err)
	}
	if dst.ID != "copied-fid" || dst.Name != "dst.txt" {
		t.Fatalf("copy entry = %+v, want located id", dst)
	}
	if len(calls) != 2 || calls[0] != "copy" || calls[1] != "list" {
		t.Fatalf("calls = %v, want [copy list]", calls)
	}
}

func TestDriverServerSideCopyRejectsDirectory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	d := New("k=v", Options{BaseURL: server.URL, V2URL: server.URL})
	if _, err := d.Copy(context.Background(), drive.Entry{ID: "d", IsDir: true}, "0", "x"); err == nil {
		t.Fatal("directory copy should fail")
	}
}
