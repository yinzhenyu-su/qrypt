package p189

import (
	"bytes"
	"context"
	"crypto/md5"
	"io"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type countingMD5Source struct {
	data  []byte
	sum   [md5.Size]byte
	opens int
}

func newCountingMD5Source(data []byte) *countingMD5Source {
	copied := append([]byte(nil), data...)
	return &countingMD5Source{data: copied, sum: md5.Sum(copied)}
}

func (s *countingMD5Source) Size() int64 {
	return int64(len(s.data))
}

func (s *countingMD5Source) Open(context.Context) (drive.ReadOnlyFile, error) {
	s.opens++
	return countingReadOnlyFile{Reader: bytes.NewReader(s.data)}, nil
}

func (s *countingMD5Source) Hash(algorithm drive.HashAlgorithm) ([]byte, bool) {
	if algorithm != drive.HashMD5 {
		return nil, false
	}
	return s.sum[:], true
}

type countingReadOnlyFile struct {
	*bytes.Reader
}

func (countingReadOnlyFile) Close() error {
	return nil
}

func TestSourceMD5HexUsesSourceMetadata(t *testing.T) {
	source := newCountingMD5Source([]byte("data"))
	got, err := sourceMD5Hex(context.Background(), source, source.Size())
	if err != nil {
		t.Fatal(err)
	}
	if source.opens != 0 {
		t.Fatalf("source opened %d times, want 0", source.opens)
	}
	if want := "8D777F385D3DFEC8815D20F7496026DC"; got != want {
		t.Fatalf("md5 = %s, want %s", got, want)
	}
}

func TestInstallBandwidthLimiter(t *testing.T) {
	drv := &Driver{}
	handled := drv.InstallBandwidthLimiter(drive.NewBandwidthLimiter(drive.BandwidthLimits{
		DownloadBytesPerSecond: 1,
		UploadBytesPerSecond:   1,
	}))
	if handled != drive.BandwidthLimitDownload|drive.BandwidthLimitUpload {
		t.Fatalf("handled directions = %v, want download|upload", handled)
	}
	if drv.limiter == nil {
		t.Fatal("limiter was not installed")
	}
}

func TestResolvePathRootUsesConfiguredRootID(t *testing.T) {
	drv := &Driver{rootID: -11}
	got, err := drv.ResolvePath(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "-11" {
		t.Fatalf("root id = %q, want -11", got)
	}
}

func TestSourceSliceMD5HexReadsOnlySlice(t *testing.T) {
	source := newCountingMD5Source(bytes.Repeat([]byte("x"), sliceMD5Size+10))
	got, err := sourceSliceMD5Hex(context.Background(), source, source.Size())
	if err != nil {
		t.Fatal(err)
	}
	if source.opens != 1 {
		t.Fatalf("source opened %d times, want 1", source.opens)
	}
	wantSum := md5.Sum(bytes.Repeat([]byte("x"), sliceMD5Size))
	if want := stringUpperHex(wantSum[:]); got != want {
		t.Fatalf("slice md5 = %s, want %s", got, want)
	}
}

func TestSourceSliceMD5HexSmallFileUsesSourceMetadata(t *testing.T) {
	source := newCountingMD5Source([]byte("small"))
	got, err := sourceSliceMD5Hex(context.Background(), source, source.Size())
	if err != nil {
		t.Fatal(err)
	}
	if source.opens != 0 {
		t.Fatalf("source opened %d times, want 0", source.opens)
	}
	if want := "EB5C1399A871211C7E7ED732D15E3A8B"; got != want {
		t.Fatalf("slice md5 = %s, want %s", got, want)
	}
}

func TestSourceMD5HexFallbackStreamsSource(t *testing.T) {
	source := plainReadOnlySource{data: []byte("fallback")}
	got, err := sourceMD5Hex(context.Background(), source, source.Size())
	if err != nil {
		t.Fatal(err)
	}
	if want := "4CCB1142EBDD7CA505D88C28DF648283"; got != want {
		t.Fatalf("md5 = %s, want %s", got, want)
	}
}

var _ drive.ReadOnlyFile = countingReadOnlyFile{}
var _ io.Reader = countingReadOnlyFile{}

type plainReadOnlySource struct {
	data []byte
}

func (s plainReadOnlySource) Size() int64 {
	return int64(len(s.data))
}

func (s plainReadOnlySource) Open(context.Context) (drive.ReadOnlyFile, error) {
	return countingReadOnlyFile{Reader: bytes.NewReader(s.data)}, nil
}

func stringUpperHex(data []byte) string {
	const digits = "0123456789ABCDEF"
	out := make([]byte, len(data)*2)
	for i, b := range data {
		out[i*2] = digits[b>>4]
		out[i*2+1] = digits[b&0x0f]
	}
	return string(out)
}
