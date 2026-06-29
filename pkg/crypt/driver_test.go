package crypt

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type recordingRawDriver struct {
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
	drv := NewDriver(raw, cp)

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
	drv := NewDriver(raw, cp)

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
