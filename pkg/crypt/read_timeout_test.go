package crypt

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// ctxBlockingReadDriver returns a body that waits for the request context to
// end, simulaing a provider whose connection stalls after the request opens.
type ctxBlockingReadDriver struct {
	recordingRawDriver
	sawDeadline chan time.Time
}

func (d *ctxBlockingReadDriver) Capabilities() []drive.Capability { return nil }

func (d *ctxBlockingReadDriver) Read(ctx context.Context, _ drive.Entry, _, _ int64) (io.ReadCloser, error) {
	if d.sawDeadline != nil {
		if deadline, ok := ctx.Deadline(); ok {
			select {
			case d.sawDeadline <- deadline:
			default:
			}
		}
	}
	return &ctxBlockingBody{ctx: ctx}, nil
}

type ctxBlockingBody struct{ ctx context.Context }

func (b *ctxBlockingBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *ctxBlockingBody) Close() error { return nil }

func TestEncryptedReadForwardsTimeoutContextToRawDriver(t *testing.T) {
	t.Parallel()
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	raw := &ctxBlockingReadDriver{sawDeadline: make(chan time.Time, 1)}
	drv := NewDriver(raw, cp, DriverOptions{})
	entry := drive.Entry{ID: "remote-id", Name: "file.bin", Size: int64(BlockDataSize) * 2}
	drv.nonceCache.Store(entry.ID, [FileNonceSize]byte{})

	budget := 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	rc, err := drv.Read(ctx, entry, 0, 1024)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	defer rc.Close()

	select {
	case deadline := <-raw.sawDeadline:
		if deadline.IsZero() {
			t.Fatal("raw driver saw a zero deadline")
		}
	case <-time.After(time.Second):
		t.Fatal("raw driver did not receive a bounded context")
	}

	started := time.Now()
	_, err = io.ReadAll(rc)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read error = %v, want context.DeadlineExceeded from the raw body", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("stalled encrypted read returned after %s", elapsed)
	}
}

func TestCryptForwardsFetchTimeoutsFromRawProfile(t *testing.T) {
	t.Parallel()
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	raw := &profiledRawDriver{
		profile: func() drive.ReadPolicy {
			p := drive.DefaultReadPolicy()
			p.Bulk.Streams = 2
			p.Fetch.Idle = 90 * time.Second
			p.Fetch.Total = 9 * time.Minute
			return p
		}(),
	}
	drv := NewDriver(raw, cp, DriverOptions{})
	profile := drv.ReadPolicy()
	if profile.Fetch.Idle != 90*time.Second || profile.Fetch.Total != 9*time.Minute {
		t.Fatalf("profile = %+v, want the raw driver's fetch timeouts", profile)
	}
}

// A completed plaintext chunk can be published while the next encrypted block
// fails authentication. Unauthenticated bytes must never reach the callback.
func TestProgressivePrefetchKeepsAuthenticationBoundary(t *testing.T) {
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	plain := bytes.Repeat([]byte("verified"), drive.ReadChunkSize*3/8)
	var nonce [FileNonceSize]byte
	encrypted, err := io.ReadAll(NewEncryptingReader(bytes.NewReader(plain), cp, nonce, int64(len(plain))))
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the first encrypted block after one complete VFS plaintext chunk.
	encrypted[FileHeaderSize+(drive.ReadChunkSize/BlockDataSize)*BlockSize+8] ^= 1
	raw := &recordingRawDriver{data: encrypted}
	drv := NewDriver(raw, cp, DriverOptions{})
	drv.nonceCache.Store("file", nonce)
	budget := 1
	source, err := drive.NewReadSource(drv, &drive.ReadOverride{MaxInFlight: &budget})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	job := source.TryFetch(drive.Entry{ID: "file", Size: int64(len(plain))}, drive.RangePlan{Size: int64(len(plain))})
	if job == nil {
		t.Fatal("admission failed")
	}
	defer job.Close()
	var got []byte
	err = job.RunChunks(context.Background(), func(_ int64, b []byte) { got = append(got, b...) })
	if err == nil {
		t.Fatal("authentication failure accepted")
	}
	if !bytes.Equal(got, plain[:drive.ReadChunkSize]) {
		t.Fatalf("published invalid or missing bytes: %d", len(got))
	}
}
