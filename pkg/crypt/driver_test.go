package crypt

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type recordingRawDriver struct {
	drive.UnsupportedOperations
	data    []byte
	reads   []rawRead
	entries []drive.Entry
}

type rawRead struct {
	offset int64
	size   int64
}

func (d *recordingRawDriver) Init(context.Context) error { return nil }
func (d *recordingRawDriver) Drop(context.Context) error { return nil }
func (d *recordingRawDriver) List(context.Context, string) ([]drive.Entry, error) {
	return append([]drive.Entry(nil), d.entries...), nil
}

func (d *recordingRawDriver) Read(_ context.Context, _ drive.Entry, offset, size int64) (io.ReadCloser, error) {
	d.reads = append(d.reads, rawRead{offset: offset, size: size})
	if offset >= int64(len(d.data)) {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	end := int64(len(d.data))
	if size > 0 && offset+size < end {
		end = offset + size
	}
	return io.NopCloser(bytes.NewReader(d.data[offset:end])), nil
}

func (d *recordingRawDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}

func (d *recordingRawDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{Driver: "raw", Health: "ok"}, nil
}
func (d *recordingRawDriver) Capabilities() []drive.Capability { return nil }
func (d *recordingRawDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}

type writableRawDriver struct {
	recordingRawDriver
}

func (d *writableRawDriver) Mkdir(context.Context, string, string) (drive.Entry, error) {
	return drive.Entry{}, nil
}
func (d *writableRawDriver) Move(context.Context, drive.Entry, string) error { return nil }
func (d *writableRawDriver) Rename(context.Context, drive.Entry, string) error {
	return nil
}
func (d *writableRawDriver) Remove(context.Context, drive.Entry) error { return nil }
func (d *writableRawDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilityWriter}
}

type sourceWritableRawDriver struct {
	writableRawDriver
	putSourceName string
	putSourceSize int64
	putSourceData []byte
}

func (d *sourceWritableRawDriver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	d.putSourceName = name
	d.putSourceSize = source.Size()
	f, err := source.Open(ctx)
	if err != nil {
		return drive.Entry{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return drive.Entry{}, err
	}
	if len(data) > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, int64(len(data)-1)); err != nil {
			return drive.Entry{}, err
		}
		if last[0] != data[len(data)-1] {
			return drive.Entry{}, io.ErrUnexpectedEOF
		}
		if _, err := f.Seek(int64(FileHeaderSize), io.SeekStart); err != nil {
			return drive.Entry{}, err
		}
	}
	d.putSourceData = data
	return drive.Entry{ID: "uploaded", ParentID: parentID, Name: name, Size: source.Size()}, nil
}
func (d *sourceWritableRawDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilitySourceUploader, drive.CapabilityWriter}
}

type hashRequiringRawDriver struct {
	sourceWritableRawDriver
}

func (d *hashRequiringRawDriver) RequiredUploadHashes() []drive.HashAlgorithm {
	return []drive.HashAlgorithm{drive.HashMD5, drive.HashSHA1}
}

type bytesReadOnlyFileSource struct {
	data []byte
}

type bytesReadOnlyFile struct {
	*bytes.Reader
}

func newBytesReadOnlyFileSource(data []byte) bytesReadOnlyFileSource {
	return bytesReadOnlyFileSource{data: append([]byte(nil), data...)}
}

func (s bytesReadOnlyFileSource) Size() int64 {
	return int64(len(s.data))
}

func (s bytesReadOnlyFileSource) Open(context.Context) (drive.ReadOnlyFile, error) {
	return bytesReadOnlyFile{Reader: bytes.NewReader(s.data)}, nil
}

func (f bytesReadOnlyFile) Close() error {
	return nil
}

type countingSHA256Source struct {
	source drive.ReadOnlyFileSource
	opens  int
}

func newCountingSHA256Source(data []byte) *countingSHA256Source {
	return &countingSHA256Source{source: drive.NewBytesReadOnlyFileSource(data)}
}

func (s *countingSHA256Source) Size() int64 {
	return s.source.Size()
}

func (s *countingSHA256Source) Open(ctx context.Context) (drive.ReadOnlyFile, error) {
	s.opens++
	return s.source.Open(ctx)
}

func (s *countingSHA256Source) Hash(algorithm drive.HashAlgorithm) ([]byte, bool) {
	return drive.SourceHash(s.source, algorithm)
}

func TestDriverCapabilitiesFollowRawRuntimeCapabilities(t *testing.T) {
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	readOnly := NewDriver(&recordingRawDriver{}, cp, DriverOptions{})
	if drive.HasCapability(readOnly, drive.CapabilityWriter) {
		t.Fatal("crypt wrapper over read-only raw should not report writer capability")
	}
	if drive.HasCapability(readOnly, drive.CapabilitySourceUploader) {
		t.Fatal("crypt wrapper over read-only raw should not report source uploader capability")
	}
	if !drive.HasCapability(readOnly, drive.CapabilityRemoteNameResolver) {
		t.Fatal("crypt wrapper should report remote-name resolution")
	}
	if !drive.HasCapability(readOnly, drive.CapabilityForeignEntries) {
		t.Fatal("crypt wrapper should report foreign-entry listing")
	}

	writable := NewDriver(&writableRawDriver{}, cp, DriverOptions{})
	if !drive.HasCapability(writable, drive.CapabilityWriter) {
		t.Fatal("crypt wrapper over writable raw should report writer capability")
	}
	if drive.HasCapability(writable, drive.CapabilitySourceUploader) {
		t.Fatal("crypt wrapper over writable-only raw should not report source uploader capability")
	}
	sourceWritable := NewDriver(&sourceWritableRawDriver{}, cp, DriverOptions{})
	if !drive.HasCapability(sourceWritable, drive.CapabilitySourceUploader) {
		t.Fatal("crypt wrapper over source uploader raw should report source uploader capability")
	}
}

func TestDriverDebugSnapshotReportsContentDedup(t *testing.T) {
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	drv := NewDriver(&recordingRawDriver{}, cp, DriverOptions{ContentDedup: true})
	snapshot, err := drv.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Extra["crypt"] != true {
		t.Fatalf("crypt extra = %+v, want crypt=true", snapshot.Extra)
	}
	if snapshot.Extra["content_dedup"] != true {
		t.Fatalf("content_dedup extra = %+v, want content_dedup=true", snapshot.Extra)
	}
}

func TestDriverReadUsesEncryptedRange(t *testing.T) {
	ctx := context.Background()
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, BlockDataSize*3)
	for i := range plain {
		plain[i] = byte(i % 251)
	}
	var nonce [FileNonceSize]byte
	copy(nonce[:], []byte("range-read-test-nonce-01"))
	encrypted, err := io.ReadAll(NewEncryptingReader(bytes.NewReader(plain), cp, nonce, int64(len(plain))))
	if err != nil {
		t.Fatal(err)
	}
	raw := &recordingRawDriver{data: encrypted}
	drv := NewDriver(raw, cp, DriverOptions{})

	offset := int64(BlockDataSize + 123)
	size := int64(2000)
	rc, err := drv.Read(ctx, drive.Entry{ID: "file", Size: int64(len(plain))}, offset, size)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	if !bytes.Equal(got, plain[offset:offset+size]) {
		t.Fatal("range read returned wrong plaintext")
	}
	if len(raw.reads) != 2 {
		t.Fatalf("raw reads = %+v, want header read and range read", raw.reads)
	}
	if raw.reads[0] != (rawRead{offset: 0, size: int64(FileHeaderSize)}) {
		t.Fatalf("header raw read = %+v", raw.reads[0])
	}
	wantOffset := int64(FileHeaderSize + BlockSize)
	if raw.reads[1].offset != wantOffset {
		t.Fatalf("range raw offset = %d, want %d", raw.reads[1].offset, wantOffset)
	}
	if raw.reads[1].size != BlockSize {
		t.Fatalf("range raw size = %d, want %d", raw.reads[1].size, BlockSize)
	}
}

func TestDriverForeignEntriesReportsUndecryptableNames(t *testing.T) {
	ctx := context.Background()
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	raw := &recordingRawDriver{entries: []drive.Entry{
		{ID: "encrypted", ParentID: "root", Name: cp.EncryptSegment("secret.txt"), Size: 42},
		{ID: "plain", ParentID: "root", Name: "plain.txt", Size: 7},
	}}
	drv := NewDriver(raw, cp, DriverOptions{})

	foreign, err := drv.ForeignEntries(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	if len(foreign) != 1 {
		t.Fatalf("foreign entries = %+v, want one", foreign)
	}
	if foreign[0].ID != "plain" || foreign[0].RemoteName != "plain.txt" || foreign[0].Reason != "filename_decrypt_failed" {
		t.Fatalf("unexpected foreign entry: %+v", foreign[0])
	}
}

func TestEncryptedReadOnlyFileSourceMatchesEncryptingReader(t *testing.T) {
	ctx := context.Background()
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, BlockDataSize*2+123)
	for i := range plain {
		plain[i] = byte(i % 251)
	}
	var nonce [FileNonceSize]byte
	copy(nonce[:], []byte("source-encrypt-nonce-01"))
	source := newEncryptedReadOnlyFileSource(newBytesReadOnlyFileSource(plain), cp, nonce, nil)
	want, err := io.ReadAll(NewEncryptingReader(bytes.NewReader(plain), cp, nonce, int64(len(plain))))
	if err != nil {
		t.Fatal(err)
	}
	if source.Size() != int64(len(want)) {
		t.Fatalf("source size = %d, want %d", source.Size(), len(want))
	}

	f, err := source.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	all, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(all, want) {
		t.Fatal("sequential encrypted source output differs from EncryptingReader")
	}
	if _, err := f.Seek(int64(FileHeaderSize+17), io.SeekStart); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 300)
	n, err := f.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:n], want[FileHeaderSize+17:FileHeaderSize+17+n]) {
		t.Fatal("seek/read returned wrong encrypted bytes")
	}
	readAt := make([]byte, BlockSize+100)
	off := int64(FileHeaderSize + BlockSize - 50)
	n, err = f.ReadAt(readAt, off)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readAt[:n], want[off:off+int64(n)]) {
		t.Fatal("ReadAt across encrypted block boundary returned wrong bytes")
	}
}

func TestDriverPutSourcePassesEncryptedSourceToRawSourceUploader(t *testing.T) {
	ctx := context.Background()
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	raw := &sourceWritableRawDriver{}
	drv := NewDriver(raw, cp, DriverOptions{})
	plain := []byte("plain source payload")

	entry, err := drv.PutSource(ctx, drive.UploadRequest{
		ParentID: "parent",
		Name:     "secret.txt",
		Source:   newBytesReadOnlyFileSource(plain),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "uploaded" || entry.ParentID != "parent" || entry.Name != "secret.txt" || entry.Size != int64(len(plain)) {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if raw.putSourceName == "secret.txt" {
		t.Fatal("raw source uploader received plaintext name")
	}
	if raw.putSourceName != cp.EncryptSegment("secret.txt") {
		t.Fatalf("raw source uploader name = %q, want encrypted name", raw.putSourceName)
	}
	if raw.putSourceSize != cp.EncryptedSize(int64(len(plain))) {
		t.Fatalf("raw source size = %d, want %d", raw.putSourceSize, cp.EncryptedSize(int64(len(plain))))
	}
	if bytes.Contains(raw.putSourceData, plain) {
		t.Fatal("raw source uploader received plaintext bytes")
	}
	if len(raw.putSourceData) < FileHeaderSize || string(raw.putSourceData[:FileMagicSize]) != FileMagic {
		t.Fatal("raw source uploader received invalid encrypted file header")
	}
	var nonce [FileNonceSize]byte
	copy(nonce[:], raw.putSourceData[FileMagicSize:FileHeaderSize])
	decrypted, err := io.ReadAll(NewDecryptingReader(bytes.NewReader(raw.putSourceData[FileHeaderSize:]), cp, nonce))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted, plain) {
		t.Fatal("raw encrypted source does not decrypt to original plaintext")
	}
}

func TestDriverPutSourceContentDedupProducesStableEncryptedSource(t *testing.T) {
	ctx := context.Background()
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("same plaintext should encrypt deterministically with content_dedup")

	rawA := &sourceWritableRawDriver{}
	drvA := NewDriver(rawA, cp, DriverOptions{ContentDedup: true})
	if _, err := drvA.PutSource(ctx, drive.UploadRequest{
		ParentID: "parent",
		Name:     "same-a.txt",
		Source:   drive.NewBytesReadOnlyFileSource(plain),
	}); err != nil {
		t.Fatal(err)
	}
	rawB := &sourceWritableRawDriver{}
	drvB := NewDriver(rawB, cp, DriverOptions{ContentDedup: true})
	if _, err := drvB.PutSource(ctx, drive.UploadRequest{
		ParentID: "parent",
		Name:     "same-b.txt",
		Source:   drive.NewBytesReadOnlyFileSource(plain),
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rawA.putSourceData, rawB.putSourceData) {
		t.Fatal("content_dedup encrypted bytes differ for identical plaintext")
	}
}

func TestDriverPutSourceContentDedupDoesNotOpenSourceForHash(t *testing.T) {
	ctx := context.Background()
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	raw := &sourceWritableRawDriver{}
	drv := NewDriver(raw, cp, DriverOptions{ContentDedup: true})
	source := newCountingSHA256Source([]byte("metadata hash avoids an extra source read"))

	if _, err := drv.PutSource(ctx, drive.UploadRequest{
		ParentID: "parent",
		Name:     "dedup.txt",
		Source:   source,
	}); err != nil {
		t.Fatal(err)
	}
	if source.opens != 1 {
		t.Fatalf("source opens = %d, want exactly one raw uploader read", source.opens)
	}
}

func TestDriverPutSourceContentDedupCachesEncryptedUploadHashes(t *testing.T) {
	ctx := context.Background()
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	raw := &hashRequiringRawDriver{}
	drv := NewDriver(raw, cp, DriverOptions{ContentDedup: true})
	plain := []byte("same content should reuse encrypted upload hashes")
	first := newCountingSHA256Source(plain)
	if _, err := drv.PutSource(ctx, drive.UploadRequest{
		ParentID: "parent",
		Name:     "first.txt",
		Source:   first,
	}); err != nil {
		t.Fatal(err)
	}
	if first.opens != 2 {
		t.Fatalf("first source opens = %d, want 2 (prehash + raw upload)", first.opens)
	}
	second := newCountingSHA256Source(plain)
	if _, err := drv.PutSource(ctx, drive.UploadRequest{
		ParentID: "parent",
		Name:     "second.txt",
		Source:   second,
	}); err != nil {
		t.Fatal(err)
	}
	if second.opens != 1 {
		t.Fatalf("second source opens = %d, want 1 (cached prehash + raw upload)", second.opens)
	}
}

func TestDriverPutSourceDefaultUsesRandomNonce(t *testing.T) {
	ctx := context.Background()
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("same plaintext should use random nonce by default")

	rawA := &sourceWritableRawDriver{}
	drvA := NewDriver(rawA, cp, DriverOptions{})
	if _, err := drvA.PutSource(ctx, drive.UploadRequest{
		ParentID: "parent",
		Name:     "same-a.txt",
		Source:   drive.NewBytesReadOnlyFileSource(plain),
	}); err != nil {
		t.Fatal(err)
	}
	rawB := &sourceWritableRawDriver{}
	drvB := NewDriver(rawB, cp, DriverOptions{})
	if _, err := drvB.PutSource(ctx, drive.UploadRequest{
		ParentID: "parent",
		Name:     "same-b.txt",
		Source:   drive.NewBytesReadOnlyFileSource(plain),
	}); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(rawA.putSourceData, rawB.putSourceData) {
		t.Fatal("default encrypted bytes should differ for identical plaintext")
	}
}

func TestDriverPutSourceContentDedupRequiresSHA256Metadata(t *testing.T) {
	ctx := context.Background()
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	raw := &sourceWritableRawDriver{}
	drv := NewDriver(raw, cp, DriverOptions{ContentDedup: true})

	_, err = drv.PutSource(ctx, drive.UploadRequest{
		ParentID: "parent",
		Name:     "missing-hash.txt",
		Source:   newBytesReadOnlyFileSource([]byte("plain")),
	})
	if err == nil {
		t.Fatal("expected content_dedup to require SHA-256 metadata")
	}
	if raw.putSourceData != nil {
		t.Fatal("raw uploader should not be called when SHA-256 metadata is missing")
	}
}
