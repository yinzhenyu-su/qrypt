package task

import (
	"errors"
	"testing"
)

// TestOperationRequestValidateOffline covers the offline addition to the
// generic download operation: offline inputs are accepted only with the
// download operation, must carry a source, must not carry items, and must not
// carry a malformed or conflicting selection.
func TestOperationRequestValidateOffline(t *testing.T) {
	tests := []struct {
		name    string
		request OperationRequest
		wantErr error
	}{
		{
			name: "valid cloud-only offline",
			request: OperationRequest{
				Operation: OperationDownload,
				Offline:   &OfflineOperationRequest{Source: "magnet:?xt=urn:btih:abc"},
			},
		},
		{
			name: "valid offline fetch",
			request: OperationRequest{
				Operation: OperationDownload,
				Offline: &OfflineOperationRequest{
					Source:     "magnet:?xt=urn:btih:abc",
					ParseTo:    "/quark-test/downloads",
					DownloadTo: "/local/dir",
				},
			},
		},
		{
			name: "valid largest selection",
			request: OperationRequest{
				Operation: OperationDownload,
				Offline: &OfflineOperationRequest{
					Source:    "magnet:?xt=urn:btih:abc",
					Selection: OfflineSelection{Mode: OfflineSelectionLargest, Largest: 2},
				},
			},
		},
		{
			name: "valid include selection",
			request: OperationRequest{
				Operation: OperationDownload,
				Offline: &OfflineOperationRequest{
					Source:    "magnet:?xt=urn:btih:abc",
					Selection: OfflineSelection{Mode: OfflineSelectionInclude, Include: []string{"*.mkv"}, Exclude: []string{"*sample*"}},
				},
			},
		},
		{
			name: "empty source",
			request: OperationRequest{
				Operation: OperationDownload,
				Offline:   &OfflineOperationRequest{},
			},
			wantErr: ErrInvalidOperation,
		},
		{
			name: "offline inputs on a non-download operation",
			request: OperationRequest{
				Operation: OperationUpload,
				Offline:   &OfflineOperationRequest{Source: "magnet:?xt=urn:btih:abc"},
			},
			wantErr: ErrInvalidOperation,
		},
		{
			name: "offline request carries items",
			request: OperationRequest{
				Operation: OperationDownload,
				Items:     []Item{{Path: "/file.txt"}},
				Offline:   &OfflineOperationRequest{Source: "magnet:?xt=urn:btih:abc"},
			},
			wantErr: ErrInvalidOperation,
		},
		{
			name: "identical destinations",
			request: OperationRequest{
				Operation: OperationDownload,
				Offline: &OfflineOperationRequest{
					Source:     "magnet:?xt=urn:btih:abc",
					ParseTo:    "/quark-test/downloads",
					DownloadTo: "/quark-test/downloads/",
				},
			},
			wantErr: ErrInvalidOperation,
		},
		{
			name: "unknown selection mode",
			request: OperationRequest{
				Operation: OperationDownload,
				Offline: &OfflineOperationRequest{
					Source:    "magnet:?xt=urn:btih:abc",
					Selection: OfflineSelection{Mode: OfflineSelectionMode("weird")},
				},
			},
			wantErr: ErrInvalidOperation,
		},
		{
			name: "largest without a count",
			request: OperationRequest{
				Operation: OperationDownload,
				Offline: &OfflineOperationRequest{
					Source:    "magnet:?xt=urn:btih:abc",
					Selection: OfflineSelection{Mode: OfflineSelectionLargest},
				},
			},
			wantErr: ErrInvalidOperation,
		},
		{
			name: "include without patterns",
			request: OperationRequest{
				Operation: OperationDownload,
				Offline: &OfflineOperationRequest{
					Source:    "magnet:?xt=urn:btih:abc",
					Selection: OfflineSelection{Mode: OfflineSelectionInclude},
				},
			},
			wantErr: ErrInvalidOperation,
		},
		{
			name: "all with include patterns",
			request: OperationRequest{
				Operation: OperationDownload,
				Offline: &OfflineOperationRequest{
					Source:    "magnet:?xt=urn:btih:abc",
					Selection: OfflineSelection{Mode: OfflineSelectionAll, Include: []string{"*.mkv"}},
				},
			},
			wantErr: ErrInvalidOperation,
		},
		{
			name: "negative largest count",
			request: OperationRequest{
				Operation: OperationDownload,
				Offline: &OfflineOperationRequest{
					Source:    "magnet:?xt=urn:btih:abc",
					Selection: OfflineSelection{Mode: OfflineSelectionAll, Largest: -1},
				},
			},
			wantErr: ErrInvalidOperation,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.request.Validate()
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestOfflineTransferTuningValidation pins the transfer tuning contract: the
// bounds the CLI advertises, and that tuning without a download destination is
// rejected instead of silently ignored.
func TestOfflineTransferTuningValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		req  OfflineOperationRequest
	}{
		{name: "negative workers", req: OfflineOperationRequest{Source: "magnet:?xt=urn:btih:a", DownloadTo: "/tmp/out", Workers: -1}},
		{name: "too many workers", req: OfflineOperationRequest{Source: "magnet:?xt=urn:btih:a", DownloadTo: "/tmp/out", Workers: MaxOfflineTransferWorkers + 1}},
		{name: "negative chunk", req: OfflineOperationRequest{Source: "magnet:?xt=urn:btih:a", DownloadTo: "/tmp/out", ChunkSize: -1}},
		{name: "chunk below minimum", req: OfflineOperationRequest{Source: "magnet:?xt=urn:btih:a", DownloadTo: "/tmp/out", ChunkSize: MinOfflineTransferChunk - 1}},
		{name: "chunk above maximum", req: OfflineOperationRequest{Source: "magnet:?xt=urn:btih:a", DownloadTo: "/tmp/out", ChunkSize: MaxOfflineTransferChunk + 1}},
		{name: "tuning without destination", req: OfflineOperationRequest{Source: "magnet:?xt=urn:btih:a", Workers: 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.req.Validate(); err == nil {
				t.Fatalf("Validate() accepted %+v", tc.req)
			}
		})
	}

	valid := []OfflineOperationRequest{
		{Source: "magnet:?xt=urn:btih:a", DownloadTo: "/tmp/out"},
		{Source: "magnet:?xt=urn:btih:a", DownloadTo: "/tmp/out", Workers: 1},
		{Source: "magnet:?xt=urn:btih:a", DownloadTo: "/tmp/out", Workers: MaxOfflineTransferWorkers, ChunkSize: MinOfflineTransferChunk},
		{Source: "magnet:?xt=urn:btih:a", DownloadTo: "/tmp/out", ChunkSize: MaxOfflineTransferChunk},
	}
	for _, req := range valid {
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() rejected %+v: %v", req, err)
		}
	}
}
