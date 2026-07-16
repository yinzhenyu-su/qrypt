package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/yinzhenyu/qrypt/internal/driver/util"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// ─── in-memory mock S3 server ─────────────────────────────────────────────

type s3Object struct {
	key     string
	data    []byte
	modTime time.Time
}

type mockMultipartUpload struct {
	key   string
	parts map[int32][]byte
	etags map[int32]string
}

type mockS3 struct {
	mu              sync.RWMutex
	objects         map[string]*s3Object // key → object
	uploads         map[string]*mockMultipartUpload
	nextUploadID    int
	failUploadPart  map[int32]int
	uploadPartCalls map[int32]int
}

func newMockS3() *mockS3 {
	return &mockS3{
		objects:         map[string]*s3Object{},
		uploads:         map[string]*mockMultipartUpload{},
		failUploadPart:  map[int32]int{},
		uploadPartCalls: map[int32]int{},
	}
}

func (m *mockS3) put(key string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = &s3Object{key: key, data: data, modTime: time.Now()}
}

func (m *mockS3) get(key string) (*s3Object, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	obj, ok := m.objects[key]
	return obj, ok
}

func (m *mockS3) del(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
}

func (m *mockS3) list(prefix, delimiter string) ([]s3Object, []string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var contents []s3Object
	prefixes := map[string]bool{}
	for key, obj := range m.objects {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		rel := strings.TrimPrefix(key, prefix)
		if delimiter != "" {
			if idx := strings.Index(rel, delimiter); idx >= 0 {
				prefixes[prefix+rel[:idx+1]] = true
				continue
			}
		}
		if rel != "" {
			contents = append(contents, *obj)
		}
	}
	var sortedPrefixes []string
	for p := range prefixes {
		sortedPrefixes = append(sortedPrefixes, p)
	}
	return contents, sortedPrefixes
}

func (m *mockS3) failPartOnce(partNumber int32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failUploadPart[partNumber]++
}

func (m *mockS3) partCalls(partNumber int32) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.uploadPartCalls[partNumber]
}

// S3 XML response types
type listBucketResult struct {
	XMLName     xml.Name      `xml:"ListBucketResult"`
	XMLNS       string        `xml:"xmlns,attr"`
	Name        string        `xml:"Name"`
	Prefix      string        `xml:"Prefix"`
	Marker      string        `xml:"Marker,omitempty"`
	MaxKeys     int           `xml:"MaxKeys"`
	IsTruncated bool          `xml:"IsTruncated"`
	Contents    []s3ObjXML    `xml:"Contents,omitempty"`
	Prefixes    []s3PrefixXML `xml:"CommonPrefixes,omitempty"`
}

type listBucketResultV2 struct {
	XMLName     xml.Name      `xml:"ListBucketResult"`
	XMLNS       string        `xml:"xmlns,attr"`
	Name        string        `xml:"Name"`
	Prefix      string        `xml:"Prefix"`
	KeyCount    int           `xml:"KeyCount"`
	MaxKeys     int           `xml:"MaxKeys"`
	IsTruncated bool          `xml:"IsTruncated"`
	Contents    []s3ObjXML    `xml:"Contents,omitempty"`
	Prefixes    []s3PrefixXML `xml:"CommonPrefixes,omitempty"`
}

type s3ObjXML struct {
	Key          string `xml:"Key"`
	Size         int64  `xml:"Size"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	StorageClass string `xml:"StorageClass"`
}

type s3PrefixXML struct {
	Prefix string `xml:"Prefix"`
}

type copyObjectResult struct {
	XMLName      xml.Name `xml:"CopyObjectResult"`
	XMLNS        string   `xml:"xmlns,attr"`
	LastModified string   `xml:"LastModified"`
	ETag         string   `xml:"ETag"`
}

type createMultipartUploadResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	XMLNS    string   `xml:"xmlns,attr"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

type completeMultipartUploadRequest struct {
	XMLName xml.Name              `xml:"CompleteMultipartUpload"`
	Parts   []completePartRequest `xml:"Part"`
}

type completePartRequest struct {
	PartNumber int32  `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

type completeMultipartUploadResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	XMLNS    string   `xml:"xmlns,attr"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
	Location string   `xml:"Location"`
}

type deleteResponse struct {
	XMLName xml.Name      `xml:"DeleteResult"`
	Deleted []deletedObj  `xml:"Deleted,omitempty"`
	Errors  []deleteError `xml:"Error,omitempty"`
}

type deletedObj struct {
	Key string `xml:"Key"`
}

type deleteError struct {
	Key     string `xml:"Key"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

type deleteRequest struct {
	XMLName xml.Name    `xml:"Delete"`
	Objects []deleteObj `xml:"Object"`
	Quiet   bool        `xml:"Quiet"`
}

type deleteObj struct {
	Key string `xml:"Key"`
}

func s3Time(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func newTestS3Client(t *testing.T, serverURL string) *s3.Client {
	t.Helper()
	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(serverURL)
		o.UsePathStyle = true
	})
}

func (m *mockS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Path-style: /{bucket}/{key...}
	pathParts := strings.SplitN(strings.Trim(r.URL.Path, "/"), "/", 2)
	bucket := pathParts[0]
	objKey := ""
	if len(pathParts) > 1 {
		objKey = pathParts[1]
	}

	switch r.Method {
	case http.MethodHead:
		// HeadBucket or HeadObject
		if objKey == "" {
			w.WriteHeader(http.StatusOK) // HeadBucket
		} else {
			// HeadObject — return 200 if exists
			if _, ok := m.get(objKey); ok {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		}

	case http.MethodGet:
		// ListObjects or GetObject
		if r.URL.Query().Has("prefix") || r.URL.Query().Has("list-type") {
			// ListObjects
			prefix := r.URL.Query().Get("prefix")
			delimiter := r.URL.Query().Get("delimiter")
			contents, prefixes := m.list(prefix, delimiter)

			isV2 := r.URL.Query().Get("list-type") == "2"
			if isV2 {
				resp := listBucketResultV2{
					XMLNS:       "http://s3.amazonaws.com/doc/2006-03-01/",
					Name:        bucket,
					Prefix:      prefix,
					KeyCount:    len(contents) + len(prefixes),
					MaxKeys:     1000,
					IsTruncated: false,
				}
				for _, obj := range contents {
					resp.Contents = append(resp.Contents, s3ObjXML{
						Key:          obj.key,
						Size:         int64(len(obj.data)),
						LastModified: s3Time(obj.modTime),
						ETag:         `"test-etag"`,
						StorageClass: "STANDARD",
					})
				}
				for _, p := range prefixes {
					resp.Prefixes = append(resp.Prefixes, s3PrefixXML{Prefix: p})
				}
				w.Header().Set("Content-Type", "application/xml")
				xml.NewEncoder(w).Encode(resp)
				return
			}

			resp := listBucketResult{
				XMLNS:       "http://s3.amazonaws.com/doc/2006-03-01/",
				Name:        bucket,
				Prefix:      prefix,
				Marker:      r.URL.Query().Get("marker"),
				MaxKeys:     1000,
				IsTruncated: false,
			}
			for _, obj := range contents {
				resp.Contents = append(resp.Contents, s3ObjXML{
					Key:          obj.key,
					Size:         int64(len(obj.data)),
					LastModified: s3Time(obj.modTime),
					ETag:         `"test-etag"`,
					StorageClass: "STANDARD",
				})
			}
			for _, p := range prefixes {
				resp.Prefixes = append(resp.Prefixes, s3PrefixXML{Prefix: p})
			}
			w.Header().Set("Content-Type", "application/xml")
			xml.NewEncoder(w).Encode(resp)
			return
		}

		// GetObject
		obj, ok := m.get(objKey)
		if !ok {
			http.Error(w, "NoSuchKey", http.StatusNotFound)
			return
		}
		data := obj.data
		rangeHeader := r.Header.Get("Range")
		if rangeHeader != "" {
			var start, end int64
			if n, _ := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); n == 2 {
				if int(end) >= len(data) {
					end = int64(len(data) - 1)
				}
				data = data[start : end+1]
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(obj.data)))
				w.WriteHeader(http.StatusPartialContent)
			} else if n, _ := fmt.Sscanf(rangeHeader, "bytes=%d-", &start); n == 1 {
				data = data[start:]
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, int64(len(obj.data))-1, len(obj.data)))
				w.WriteHeader(http.StatusPartialContent)
			} else {
				w.WriteHeader(http.StatusOK)
			}
		} else {
			w.WriteHeader(http.StatusOK)
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(data)

	case http.MethodPut:
		if uploadID := r.URL.Query().Get("uploadId"); uploadID != "" {
			partNumber64, err := strconv.ParseInt(r.URL.Query().Get("partNumber"), 10, 32)
			if err != nil || partNumber64 <= 0 {
				http.Error(w, "InvalidPart", http.StatusBadRequest)
				return
			}
			partNumber := int32(partNumber64)
			data, _ := io.ReadAll(r.Body)

			m.mu.Lock()
			m.uploadPartCalls[partNumber]++
			if m.failUploadPart[partNumber] > 0 {
				m.failUploadPart[partNumber]--
				m.mu.Unlock()
				http.Error(w, "InvalidPart", http.StatusBadRequest)
				return
			}
			upload, ok := m.uploads[uploadID]
			if !ok {
				m.mu.Unlock()
				http.Error(w, "NoSuchUpload", http.StatusNotFound)
				return
			}
			etag := fmt.Sprintf(`"etag-%s-%d"`, uploadID, partNumber)
			upload.parts[partNumber] = data
			upload.etags[partNumber] = etag
			m.mu.Unlock()

			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusOK)
			return
		}

		// PutObject or CopyObject
		copySource := r.Header.Get("X-Amz-Copy-Source")
		if copySource != "" {
			// CopyObject
			decodedSrc, _ := url.PathUnescape(copySource)
			srcParts := strings.SplitN(decodedSrc, "/", 2)
			srcKey := ""
			if len(srcParts) > 1 {
				srcKey = srcParts[1]
			}
			srcObj, ok := m.get(srcKey)
			if !ok {
				http.Error(w, "NoSuchKey", http.StatusNotFound)
				return
			}
			m.put(objKey, srcObj.data)
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusOK)
			xml.NewEncoder(w).Encode(copyObjectResult{
				XMLNS:        "http://s3.amazonaws.com/doc/2006-03-01/",
				LastModified: s3Time(time.Now()),
				ETag:         `"test-etag"`,
			})
			return
		}

		// Plain PutObject
		data, _ := io.ReadAll(r.Body)
		m.put(objKey, data)
		w.WriteHeader(http.StatusOK)

	case http.MethodDelete:
		if uploadID := r.URL.Query().Get("uploadId"); uploadID != "" {
			m.mu.Lock()
			delete(m.uploads, uploadID)
			m.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// DeleteObject
		m.del(objKey)
		w.WriteHeader(http.StatusNoContent)

	case http.MethodPost:
		if r.URL.Query().Has("uploads") {
			m.mu.Lock()
			m.nextUploadID++
			uploadID := fmt.Sprintf("upload-%d", m.nextUploadID)
			m.uploads[uploadID] = &mockMultipartUpload{key: objKey, parts: map[int32][]byte{}, etags: map[int32]string{}}
			m.mu.Unlock()

			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusOK)
			xml.NewEncoder(w).Encode(createMultipartUploadResult{
				XMLNS:    "http://s3.amazonaws.com/doc/2006-03-01/",
				Bucket:   bucket,
				Key:      objKey,
				UploadID: uploadID,
			})
			return
		}

		if uploadID := r.URL.Query().Get("uploadId"); uploadID != "" {
			var req completeMultipartUploadRequest
			if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "MalformedXML", http.StatusBadRequest)
				return
			}

			m.mu.Lock()
			upload, ok := m.uploads[uploadID]
			if !ok {
				m.mu.Unlock()
				http.Error(w, "NoSuchUpload", http.StatusNotFound)
				return
			}
			var data []byte
			for _, part := range req.Parts {
				partData, ok := upload.parts[part.PartNumber]
				if !ok {
					m.mu.Unlock()
					http.Error(w, "InvalidPart", http.StatusBadRequest)
					return
				}
				data = append(data, partData...)
			}
			m.objects[objKey] = &s3Object{key: objKey, data: data, modTime: time.Now()}
			delete(m.uploads, uploadID)
			m.mu.Unlock()

			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusOK)
			xml.NewEncoder(w).Encode(completeMultipartUploadResult{
				XMLNS:    "http://s3.amazonaws.com/doc/2006-03-01/",
				Bucket:   bucket,
				Key:      objKey,
				ETag:     `"complete-etag"`,
				Location: "/" + bucket + "/" + objKey,
			})
			return
		}

		// DeleteObjects (batch delete)
		if r.URL.Query().Get("delete") == "" {
			http.Error(w, "MethodNotAllowed", http.StatusMethodNotAllowed)
			return
		}
		var req deleteRequest
		if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "MalformedXML", http.StatusBadRequest)
			return
		}
		var resp deleteResponse
		for _, obj := range req.Objects {
			m.del(obj.Key)
			resp.Deleted = append(resp.Deleted, deletedObj{Key: obj.Key})
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		xml.NewEncoder(w).Encode(resp)

	default:
		http.Error(w, "MethodNotAllowed", http.StatusMethodNotAllowed)
	}
}

// ─── driver+client setup helper ───────────────────────────────────────────

func setupTest(t *testing.T) (*Driver, *mockS3, string) {
	t.Helper()
	mock := newMockS3()
	srv := httptest.NewServer(mock)
	t.Cleanup(srv.Close)

	d := New(Options{
		Bucket:          "test-bucket",
		Endpoint:        srv.URL,
		Region:          "us-east-1",
		AccessKeyID:     "test",
		SecretAccessKey: "test",
		Placeholder:     ".qrypt",
	})
	d.client = newTestS3Client(t, srv.URL)
	return d, mock, srv.URL
}

// ─── factory tests ────────────────────────────────────────────────────────

func TestFactoryMissingBucket(t *testing.T) {
	_, err := drive.New("s3", drive.Params{"endpoint": "https://example.com"})
	if err == nil || !strings.Contains(err.Error(), "bucket") {
		t.Fatalf("expected bucket error, got %v", err)
	}
}

func TestFactoryMissingEndpoint(t *testing.T) {
	_, err := drive.New("s3", drive.Params{"bucket": "b"})
	if err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("expected endpoint error, got %v", err)
	}
}

func TestFactoryCreatesDriver(t *testing.T) {
	raw, err := drive.New("s3", drive.Params{
		"bucket":   "my-bucket",
		"endpoint": "https://s3.us-east-1.amazonaws.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := raw.(*Driver)
	if !ok {
		t.Fatalf("type = %T, want *s3.Driver", raw)
	}
	if d.bucket != "my-bucket" {
		t.Fatalf("bucket = %q, want my-bucket", d.bucket)
	}
}

// ─── path utility tests ───────────────────────────────────────────────────

func TestToS3Key(t *testing.T) {
	tests := []struct {
		name     string
		rootPath string
		id       string
		want     string
	}{
		{"root", "", "/", ""},
		{"root with prefix", "data", "/", "data"},
		{"file", "", "docs/note.txt", "docs/note.txt"},
		{"file with prefix", "data", "docs/note.txt", "data/docs/note.txt"},
		{"dir", "", "photos/", "photos"},
		{"dir with prefix", "data", "photos/", "data/photos"},
		{"zero ID", "", "0", ""},
		{"zero ID with prefix", "data", "0", "data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := New(Options{RootPath: tt.rootPath})
			got := d.toS3Key(tt.id)
			if got != tt.want {
				t.Fatalf("toS3Key(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestRelPath(t *testing.T) {
	tests := []struct {
		name     string
		rootPath string
		s3Key    string
		want     string
	}{
		{"no prefix", "", "docs/file.txt", "docs/file.txt"},
		{"with prefix", "data", "data/docs/file.txt", "docs/file.txt"},
		{"prefix itself", "data", "data", ""},
		{"prefix itself slash", "data", "data/", ""},
		{"nested with prefix", "root", "root/a/b/c", "a/b/c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := New(Options{RootPath: tt.rootPath})
			got := d.relPath(tt.s3Key)
			if got != tt.want {
				t.Fatalf("relPath(%q) = %q, want %q", tt.s3Key, got, tt.want)
			}
		})
	}
}

func TestResolvePathUsesVirtualRootUnderRootPath(t *testing.T) {
	d := New(Options{RootPath: "/A/B/C"})
	root, err := d.ResolvePath(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if root != "0" {
		t.Fatalf("ResolvePath root = %q, want virtual root id 0", root)
	}
	nested, err := d.ResolvePath(context.Background(), "/x/y.txt")
	if err != nil {
		t.Fatal(err)
	}
	if nested != "x/y.txt" {
		t.Fatalf("ResolvePath nested = %q, want root-relative id", nested)
	}
	if key := d.toS3Key(nested); key != "A/B/C/x/y.txt" {
		t.Fatalf("toS3Key nested = %q, want root_path-prefixed key", key)
	}
}

func TestJoinPath(t *testing.T) {
	tests := []struct {
		name     string
		parentID string
		child    string
		want     string
	}{
		{"root parent", "0", "docs", "docs"},
		{"root parent alt", "/", "docs", "docs"},
		{"nested", "docs", "note.txt", "docs/note.txt"},
		{"deep nested", "a/b", "c", "a/b/c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := New(Options{})
			got := d.joinPath(tt.parentID, tt.child)
			if got != tt.want {
				t.Fatalf("joinPath(%q, %q) = %q, want %q", tt.parentID, tt.child, got, tt.want)
			}
		})
	}
}

// ─── Init test ────────────────────────────────────────────────────────────

func TestInitValidatesBucket(t *testing.T) {
	d, _, _ := setupTest(t)
	if err := d.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// ─── CRUD tests ───────────────────────────────────────────────────────────

func TestS3CRUD(t *testing.T) {
	ctx := context.Background()
	d, mock, _ := setupTest(t)

	// Mkdir
	docs, err := d.Mkdir(ctx, "0", "docs")
	if err != nil {
		t.Fatal(err)
	}
	if !docs.IsDir {
		t.Fatal("expected directory")
	}
	if docs.ID != "docs/" {
		t.Fatalf("dir ID = %q, want docs/", docs.ID)
	}

	// Put
	data := "hello world"
	entry, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: docs.ID,
		Name:     "note.txt",
		Source:   drive.NewBytesReadOnlyFileSource([]byte(data)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Size != int64(len(data)) {
		t.Fatalf("size = %d, want %d", entry.Size, len(data))
	}
	// Verify object in mock
	obj, ok := mock.get("docs/note.txt")
	if !ok {
		t.Fatal("expected docs/note.txt in mock")
	}
	if string(obj.data) != data {
		t.Fatalf("data = %q, want %q", string(obj.data), data)
	}

	// List docs
	entries, err := d.List(ctx, docs.ID)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	if !slices.Contains(names, "note.txt") {
		t.Fatalf("docs entries = %v, want note.txt", names)
	}

	// Read
	rc, err := d.Read(ctx, entry, 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	gotData, _ := io.ReadAll(rc)
	rc.Close()
	if string(gotData) != data {
		t.Fatalf("read = %q, want %q", string(gotData), data)
	}

	// Read range
	rc, err = d.Read(ctx, entry, 6, 5)
	if err != nil {
		t.Fatal(err)
	}
	gotData, _ = io.ReadAll(rc)
	rc.Close()
	if string(gotData) != "world" {
		t.Fatalf("range read = %q, want world", string(gotData))
	}

	// Read directory should fail
	if _, err := d.Read(ctx, docs, 0, 0); err == nil {
		t.Fatal("expected error reading a directory")
	}

	if err := d.Rename(ctx, entry, "renamed.txt"); err != nil {
		t.Fatal(err)
	}
	if _, ok := mock.get("docs/renamed.txt"); !ok {
		t.Fatal("expected renamed.txt after rename")
	}
	if _, ok := mock.get("docs/note.txt"); ok {
		t.Fatal("note.txt should be gone after rename")
	}

	renamedEntry := drive.Entry{ID: "docs/renamed.txt", Name: "renamed.txt"}
	if err := d.Move(ctx, renamedEntry, "0"); err != nil {
		t.Fatal(err)
	}

	if err := d.Remove(ctx, drive.Entry{ID: "renamed.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := mock.get("renamed.txt"); ok {
		t.Fatal("renamed.txt should be deleted")
	}

	if err := d.Remove(ctx, drive.Entry{ID: "docs/.qrypt"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove(ctx, docs); err != nil {
		t.Fatal(err)
	}
}

func TestS3ReadNotFound(t *testing.T) {
	ctx := context.Background()
	d, _, _ := setupTest(t)
	_, err := d.Read(ctx, drive.Entry{ID: "nonexistent"}, 0, 0)
	if err == nil {
		t.Fatal("expected error for nonexistent key")
	}
}

// ─── PutSource test ───────────────────────────────────────────────────────

func TestPutSource(t *testing.T) {
	ctx := context.Background()
	d, mock, _ := setupTest(t)
	d.InstallBandwidthLimiter(drive.NewBandwidthLimiter(drive.BandwidthLimits{UploadBytesPerSecond: 1 << 30}))

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "upload.txt")
	content := []byte("file upload content")
	if err := os.WriteFile(localPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	progress := &recordingUploadProgress{}
	entry, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: "0",
		Name:     "uploaded.txt",
		Source:   drive.NewLocalReadOnlyFileSource(localPath, int64(len(content))),
		Progress: progress,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Size != int64(len(content)) {
		t.Fatalf("size = %d, want %d", entry.Size, len(content))
	}
	obj, ok := mock.get("uploaded.txt")
	if !ok {
		t.Fatal("expected uploaded.txt in mock")
	}
	if !bytes.Equal(obj.data, content) {
		t.Fatalf("data mismatch")
	}
	if progress.bytes < int64(len(content)) {
		t.Fatalf("uploaded bytes = %d, want at least %d", progress.bytes, len(content))
	}
	if !slices.Contains(progress.phases, drive.UploadPhaseUploading) {
		t.Fatalf("upload phases = %v, want uploading", progress.phases)
	}
}

func TestUploadPartRanges(t *testing.T) {
	got := s3UploadPartRanges(35, 16)
	want := []s3UploadPartRange{
		{Number: 1, Offset: 0, Size: 16},
		{Number: 2, Offset: 16, Size: 16},
		{Number: 3, Offset: 32, Size: 3},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ranges = %+v, want %+v", got, want)
	}
}

func TestPutSourceMultipart(t *testing.T) {
	ctx := context.Background()
	d, mock, _ := setupTest(t)
	d.InstallStateStore(drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver")))

	content := bytes.Repeat([]byte("a"), s3MultipartPartSize+3)
	progress := &recordingUploadProgress{}
	entry, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: "0",
		Name:     "large.bin",
		Source:   drive.NewBytesReadOnlyFileSource(content),
		Progress: progress,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Size != int64(len(content)) {
		t.Fatalf("size = %d, want %d", entry.Size, len(content))
	}
	obj, ok := mock.get("large.bin")
	if !ok {
		t.Fatal("expected large.bin in mock")
	}
	if !bytes.Equal(obj.data, content) {
		t.Fatal("multipart data mismatch")
	}
	if mock.partCalls(1) != 1 || mock.partCalls(2) != 1 {
		t.Fatalf("part calls = part1:%d part2:%d, want 1 each", mock.partCalls(1), mock.partCalls(2))
	}
	if progress.bytes < int64(len(content)) {
		t.Fatalf("uploaded bytes = %d, want at least %d", progress.bytes, len(content))
	}
}

func TestPutSourceMultipartResumesCompletedPart(t *testing.T) {
	ctx := context.Background()
	d, mock, _ := setupTest(t)
	d.InstallStateStore(drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver")))
	mock.failPartOnce(2)

	content := bytes.Repeat([]byte("r"), s3MultipartPartSize+7)
	req := drive.UploadRequest{
		ParentID: "0",
		Name:     "resume.bin",
		Source:   drive.NewBytesReadOnlyFileSource(content),
		Progress: &recordingUploadProgress{},
	}
	if _, err := d.PutSource(ctx, req); err == nil {
		t.Fatal("expected first upload to fail")
	}

	sessionKey := util.UploadSessionKey(d.bucket, "resume.bin", int64(len(content)))
	session, ok := d.loadUploadSession(sessionKey)
	if !ok {
		t.Fatal("expected saved upload session after first part")
	}
	if len(session.Parts) != 1 || session.Parts[0].Number != 1 {
		t.Fatalf("saved parts = %+v, want only part 1", session.Parts)
	}

	req.Progress = &recordingUploadProgress{}
	if _, err := d.PutSource(ctx, req); err != nil {
		t.Fatal(err)
	}
	obj, ok := mock.get("resume.bin")
	if !ok {
		t.Fatal("expected resume.bin in mock")
	}
	if !bytes.Equal(obj.data, content) {
		t.Fatal("resumed multipart data mismatch")
	}
	if mock.partCalls(1) != 1 {
		t.Fatalf("part 1 calls = %d, want 1 because resume should skip it", mock.partCalls(1))
	}
	if mock.partCalls(2) != 2 {
		t.Fatalf("part 2 calls = %d, want 2 because first attempt failed once", mock.partCalls(2))
	}
	if _, ok := d.loadUploadSession(sessionKey); ok {
		t.Fatal("upload session should be deleted after complete")
	}
}

type recordingUploadProgress struct {
	phases []drive.UploadPhase
	bytes  int64
}

func (p *recordingUploadProgress) Phase(phase drive.UploadPhase) {
	p.phases = append(p.phases, phase)
}

func (p *recordingUploadProgress) Uploaded(n int64) {
	p.bytes += n
}

// ─── DebugSnapshot test (keep existing) ───────────────────────────────────

func TestDebugSnapshot(t *testing.T) {
	d := New(Options{
		Bucket:   "my-bucket",
		Endpoint: "https://s3.us-east-1.amazonaws.com",
		Region:   "us-east-1",
		RootPath: "/data",
	})
	snapshot, err := d.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Driver != "s3" {
		t.Fatalf("driver = %q, want s3", snapshot.Driver)
	}
	if snapshot.Health != "ok" {
		t.Fatalf("health = %q, want ok", snapshot.Health)
	}
	if snapshot.Stats[drive.DebugStatRootPath] != "data" {
		t.Fatalf("unexpected stats: %+v", snapshot.Stats)
	}
	if snapshot.Stats["bucket"] != "my-bucket" {
		t.Fatalf("unexpected stats: %+v", snapshot.Stats)
	}
	if snapshot.Extra[drive.DebugExtraCredentialSource] != "config" {
		t.Fatalf("credential_source = %v, want config", snapshot.Extra[drive.DebugExtraCredentialSource])
	}
}
