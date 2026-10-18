package core

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

// TestOfflineOperationMapsToTaskTypes pins the operation-level mapping: an
// offline request with a final destination selects the fetch type, and one
// without selects the cloud-only type, both under the download operation.
func TestOfflineOperationMapsToTaskTypes(t *testing.T) {
	t.Parallel()

	cloudOnly, err := taskRequestForOperation(task.OperationRequest{
		Operation: task.OperationDownload,
		Offline:   &task.OfflineOperationRequest{Source: "magnet:?xt=urn:btih:abc"},
	})
	if err != nil {
		t.Fatalf("cloud-only mapping: %v", err)
	}
	if cloudOnly.Type != task.TypeOfflineDownload {
		t.Fatalf("cloud-only type = %q, want %q", cloudOnly.Type, task.TypeOfflineDownload)
	}
	if cloudOnly.Offline == nil {
		t.Fatal("cloud-only request dropped the offline inputs")
	}

	fetch, err := taskRequestForOperation(task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:abc",
			DownloadTo: "/local/dir",
		},
	})
	if err != nil {
		t.Fatalf("fetch mapping: %v", err)
	}
	if fetch.Type != task.TypeOfflineFetch {
		t.Fatalf("fetch type = %q, want %q", fetch.Type, task.TypeOfflineFetch)
	}
	if fetch.Offline == nil || fetch.Offline.DownloadTo != "/local/dir" {
		t.Fatalf("fetch request dropped the offline inputs: %+v", fetch.Offline)
	}

	// A plain download operation keeps its existing type.
	plain, err := taskRequestForOperation(task.OperationRequest{
		Operation: task.OperationDownload,
		Items:     []task.Item{{SourcePath: "/a"}},
	})
	if err != nil {
		t.Fatalf("plain mapping: %v", err)
	}
	if plain.Type != task.TypeDownloadStreamBatch {
		t.Fatalf("plain download type = %q, want %q", plain.Type, task.TypeDownloadStreamBatch)
	}
}
