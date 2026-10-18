package drive

import (
	"context"
	"errors"
	"time"
)

// ErrOfflineInspectPending reports that a provider's asynchronous parse has not
// finished yet. An inspector returns it (possibly wrapping it) so callers can
// retry a bounded number of times before reporting a typed timeout.
var ErrOfflineInspectPending = errors.New("offline: provider parse pending")

// OfflinePreview is the read-only result of inspecting a source without
// creating a provider task. Providers that declare CapabilityOfflineInspect
// return it from OfflineInspector.
type OfflinePreview struct {
	// Source is the normalized source identity qrypt submitted or inspected.
	Source string `json:"source"`
	// SourceKind is the normalized kind of Source.
	SourceKind OfflineSourceKind `json:"source_kind,omitempty"`
	// Title is the provider-reported resource title when it exposes one.
	Title string `json:"title,omitempty"`
	// TotalSize is the sum of the parsed file sizes when the provider reports
	// them; zero means unknown.
	TotalSize int64 `json:"total_size,omitempty"`
	// Files are the parsed files, in provider order. The selection is applied
	// by the caller, not the provider, so the preview stays provider-neutral.
	Files []OfflineFile `json:"files,omitempty"`
	// InstantSave reports whether the provider predicts the resource is
	// already cached server-side (an instant import).
	InstantSave bool `json:"instant_save,omitempty"`
}

// OfflineInspector is an optional provider capability: resolve a source into a
// file list without creating a provider task. It is declared with
// CapabilityOfflineInspect; providers without it must not be inspected.
type OfflineInspector interface {
	InspectOfflineDownload(ctx context.Context, req OfflineDownloadRequest) (OfflinePreview, error)
}

// OfflineFetchRequest asks a provider for a download URL for the files a
// staged offline task produced. It is the provider half of the fetch transfer
// phase: the caller owns staging and destination writing.
type OfflineFetchRequest struct {
	// ID is the provider task identifier returned by CreateOfflineDownload.
	ID string `json:"id"`
	// Destination is the mount-relative staging directory the task parsed
	// into; providers may need it to resolve the produced entries.
	Destination string `json:"destination,omitempty"`
	// Files are the staged files to download. Providers that download by
	// directory may ignore it, but a per-file provider uses RemoteID.
	Files []OfflineFile `json:"files,omitempty"`
	// Accelerated asks for an accelerated session when the provider supports
	// it. The caller sets it only when the provider declared
	// CapabilityAcceleratedFetch; a provider may still ignore it.
	Accelerated bool `json:"accelerated,omitempty"`
	// Offset is the byte offset the caller already has, so a provider that can
	// issue ranged URLs may do so. Zero means from the start.
	Offset int64 `json:"offset,omitempty"`
}

// OfflineDownloadTarget is a provider-signed download URL for staged content.
// ExpiresAt may be zero when the provider does not disclose an expiry; the
// caller then re-requests on transfer failure rather than pre-emptively.
type OfflineDownloadTarget struct {
	URL       string            `json:"url"`
	ExpiresAt time.Time         `json:"expires_at,omitempty"`
	Size      int64             `json:"size,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// OfflineFetcher is an optional provider capability: hand out a download URL
// for content a provider-side task staged. It is declared with
// CapabilityOfflineDownload (a provider that can stage but never fetch would
// fail the transfer phase), and accelerated sessions additionally need
// CapabilityAcceleratedFetch.
type OfflineFetcher interface {
	OfflineDownloadURL(ctx context.Context, req OfflineFetchRequest) (OfflineDownloadTarget, error)
}

// OfflineLayout is the persisted identity a provider needs to resolve the same
// staged output tree after a restart. TaskID is the provider task identifier,
// RootID is the entry the task produced, ParentID is the directory that
// contains it, and Available reports that the staged result set was already
// complete when the layout was persisted.
type OfflineLayout struct {
	TaskID    string `json:"task_id,omitempty"`
	RootID    string `json:"root_id,omitempty"`
	ParentID  string `json:"parent_id,omitempty"`
	Available bool   `json:"available,omitempty"`
}

// OfflineLayoutRestorer is an optional provider capability: restore a task's
// persisted output identity into a fresh driver instance so a recovered fetch
// can resolve staged relative paths and discover omitted files. Providers
// without it keep their layout in process memory and may lose it on restart.
type OfflineLayoutRestorer interface {
	RestoreOfflineLayout(layout OfflineLayout)
}

// OfflineRetrier is an optional provider capability: re-queue a provider task
// in place after it failed or was canceled, keeping the same provider task
// identifier. A provider without it is retried by re-creating the task from
// persisted inputs.
type OfflineRetrier interface {
	RetryOfflineDownload(ctx context.Context, id string) error
}

// OfflineSourceKindsFor returns the source kinds a driver accepts, or nil when
// the driver does not report them. It is OfflineSourceKinds under a name that
// reads better at call sites that already hold a normalized kind.
func OfflineSourceKindsFor(d Driver) []OfflineSourceKind { return OfflineSourceKinds(d) }

// SupportsOfflineSourceKind reports whether d accepts kind. A driver that does
// not report its kinds is treated as accepting every kind; the provider's own
// error at creation time is authoritative.
func SupportsOfflineSourceKind(d Driver, kind OfflineSourceKind) bool {
	kinds := OfflineSourceKinds(d)
	if len(kinds) == 0 {
		return true
	}
	for _, accepted := range kinds {
		if accepted == kind {
			return true
		}
	}
	return false
}
