package vfs

import (
	"context"
	"testing"
	"time"
)

type fakeUploadWorkerRuntime struct {
	supported     bool
	latest        PendingUpload
	latestOK      bool
	quietDelay    time.Duration
	quietWindow   time.Duration
	acquire       bool
	removed       []string
	requeued      []PendingUpload
	requeueDelays []time.Duration
	executed      int
	released      int
}

func (r *fakeUploadWorkerRuntime) Receive(context.Context) (PendingUpload, bool) {
	return PendingUpload{}, false
}

func (r *fakeUploadWorkerRuntime) StopUploadTimers() {}
func (r *fakeUploadWorkerRuntime) StopDeleteTimers() {}

func (r *fakeUploadWorkerRuntime) SourceUploadSupported() bool {
	return r.supported
}

func (r *fakeUploadWorkerRuntime) LatestUpload(string) (PendingUpload, bool) {
	return r.latest, r.latestOK
}

func (r *fakeUploadWorkerRuntime) RemoveStagingIfUnreferenced(localPath string) {
	r.removed = append(r.removed, localPath)
}

func (r *fakeUploadWorkerRuntime) Requeue(pending PendingUpload) {
	r.requeued = append(r.requeued, pending)
}

func (r *fakeUploadWorkerRuntime) RequeueAfter(pending PendingUpload, delay time.Duration) {
	r.requeued = append(r.requeued, pending)
	r.requeueDelays = append(r.requeueDelays, delay)
}

func (r *fakeUploadWorkerRuntime) QuietDelay(PendingUpload) time.Duration {
	return r.quietDelay
}

func (r *fakeUploadWorkerRuntime) QuietWindow(PendingUpload) time.Duration {
	return r.quietWindow
}

func (r *fakeUploadWorkerRuntime) TryAcquire(PendingUpload, int) bool {
	return r.acquire
}

func (r *fakeUploadWorkerRuntime) Release(PendingUpload) {
	r.released++
}

func (r *fakeUploadWorkerRuntime) ExecuteUpload(context.Context, PendingUpload) error {
	r.executed++
	return nil
}

func (r *fakeUploadWorkerRuntime) SendUpload(PendingUpload) {}

func TestUploadPendingWithRuntimeCleansRemovedPending(t *testing.T) {
	pending := PendingUpload{Path: "/file.txt", FID: "old", LocalPath: "/tmp/old"}
	runtime := &fakeUploadWorkerRuntime{supported: true}
	if err := uploadPendingWithRuntime(context.Background(), pending, runtime, 1, time.Second); err != nil {
		t.Fatal(err)
	}
	if len(runtime.removed) != 1 || runtime.removed[0] != pending.LocalPath {
		t.Fatalf("removed = %+v", runtime.removed)
	}
	if runtime.executed != 0 {
		t.Fatalf("executed = %d", runtime.executed)
	}
}

func TestUploadPendingWithRuntimeRequeuesFrozenSupersededUpload(t *testing.T) {
	pending := PendingUpload{Path: "/file.txt", FID: "old", LocalPath: "/tmp/old", Frozen: true}
	latest := PendingUpload{Path: "/file.txt", FID: "new", LocalPath: "/tmp/new", Frozen: true}
	runtime := &fakeUploadWorkerRuntime{supported: true, latest: latest, latestOK: true}
	if err := uploadPendingWithRuntime(context.Background(), pending, runtime, 1, time.Second); err != nil {
		t.Fatal(err)
	}
	if len(runtime.removed) != 1 || runtime.removed[0] != pending.LocalPath {
		t.Fatalf("removed = %+v", runtime.removed)
	}
	if len(runtime.requeued) != 1 || runtime.requeued[0].FID != latest.FID {
		t.Fatalf("requeued = %+v", runtime.requeued)
	}
}

func TestUploadPendingWithRuntimeDelaysForQuietWindow(t *testing.T) {
	pending := PendingUpload{Path: "/file.txt", FID: "same", LocalPath: "/tmp/file"}
	runtime := &fakeUploadWorkerRuntime{supported: true, latest: pending, latestOK: true, quietDelay: 2 * time.Second}
	if err := uploadPendingWithRuntime(context.Background(), pending, runtime, 1, time.Second); err != nil {
		t.Fatal(err)
	}
	if len(runtime.requeueDelays) != 1 || runtime.requeueDelays[0] != 2*time.Second {
		t.Fatalf("requeue delays = %+v", runtime.requeueDelays)
	}
	if runtime.executed != 0 {
		t.Fatalf("executed = %d", runtime.executed)
	}
}

func TestUploadPendingWithRuntimeDelaysWhenAdmissionUnavailable(t *testing.T) {
	pending := PendingUpload{Path: "/file.txt", FID: "same", LocalPath: "/tmp/file"}
	runtime := &fakeUploadWorkerRuntime{supported: true, latest: pending, latestOK: true, quietWindow: 3 * time.Second}
	if err := uploadPendingWithRuntime(context.Background(), pending, runtime, 1, time.Second); err != nil {
		t.Fatal(err)
	}
	if len(runtime.requeueDelays) != 1 || runtime.requeueDelays[0] != 3*time.Second {
		t.Fatalf("requeue delays = %+v", runtime.requeueDelays)
	}
}

func TestUploadPendingWithRuntimeExecutesAndReleasesAdmission(t *testing.T) {
	pending := PendingUpload{Path: "/file.txt", FID: "same", LocalPath: "/tmp/file"}
	runtime := &fakeUploadWorkerRuntime{supported: true, latest: pending, latestOK: true, acquire: true}
	if err := uploadPendingWithRuntime(context.Background(), pending, runtime, 1, time.Second); err != nil {
		t.Fatal(err)
	}
	if runtime.executed != 1 || runtime.released != 1 {
		t.Fatalf("executed=%d released=%d", runtime.executed, runtime.released)
	}
}
