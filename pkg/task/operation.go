package task

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidOperation    = errors.New("task: invalid operation")
	ErrIdempotencyConflict = errors.New("task: idempotency conflict")
)

type OperationKind string

const (
	OperationUpload   OperationKind = "upload"
	OperationDownload OperationKind = "download"
	OperationDelete   OperationKind = "delete"
	OperationCopy     OperationKind = "copy"
	OperationMove     OperationKind = "move"
	// OperationShare covers provider-side work on share links, including
	// copying share content into the acting account's drive.
	OperationShare OperationKind = "share"
)

type UploadPolicy string

const (
	UploadPolicyPreferDirect UploadPolicy = "prefer_direct"
	UploadPolicyStagingOnly  UploadPolicy = "staging_only"
)

type UploadSource string

const (
	UploadSourceApp   UploadSource = "app_source"
	UploadSourceLocal UploadSource = "local_file"
)

type OperationRequest struct {
	Operation      OperationKind            `json:"operation"`
	Scope          Scope                    `json:"scope,omitempty"`
	Items          []Item                   `json:"items,omitempty"`
	Options        Options                  `json:"options,omitempty"`
	Detail         map[string]any           `json:"detail,omitempty"`
	UploadPolicy   UploadPolicy             `json:"upload_policy,omitempty"`
	UploadSource   UploadSource             `json:"upload_source,omitempty"`
	Offline        *OfflineOperationRequest `json:"offline,omitempty"`
	Share          *ShareOperationRequest   `json:"share,omitempty"`
	IdempotencyKey string                   `json:"idempotency_key,omitempty"`
}

// OfflineSelectionMode names how an offline operation chooses parsed files.
type OfflineSelectionMode string

const (
	// OfflineSelectionAll imports every parsed file. It is the zero value.
	OfflineSelectionAll OfflineSelectionMode = "all"
	// OfflineSelectionLargest imports the Largest largest files.
	OfflineSelectionLargest OfflineSelectionMode = "largest"
	// OfflineSelectionInclude imports files matching Include and not Exclude.
	OfflineSelectionInclude OfflineSelectionMode = "include"
)

// OfflineSelection is the provider-neutral file selection of an offline
// operation.
type OfflineSelection struct {
	Mode    OfflineSelectionMode `json:"mode,omitempty"`
	Largest int                  `json:"largest,omitempty"`
	Include []string             `json:"include,omitempty"`
	Exclude []string             `json:"exclude,omitempty"`
}

// ShareOperationRequest carries the provider-neutral inputs of a share
// receive. A non-empty DownloadTo selects the fetch type; without it the task
// only copies share content into the acting account's drive.
type ShareOperationRequest struct {
	// Link is the provider share link (or its bare id). The Core resolves its
	// provider from the link itself.
	Link string `json:"link"`
	// Passcode protects the link when the provider requires one.
	Passcode string `json:"passcode,omitempty"`
	// Mount names the acting account when the link's provider has several
	// mounts. It must agree with To when both are given.
	Mount string `json:"mount,omitempty"`
	// To is the mount-qualified cloud destination (--to /MOUNT/DIR). Empty
	// means the acting mount's root.
	To         string           `json:"to,omitempty"`
	DownloadTo string           `json:"download_to,omitempty"`
	Selection  OfflineSelection `json:"selection,omitempty"`
	Mkdir      bool             `json:"mkdir,omitempty"`
	New        bool             `json:"new,omitempty"`
	KeepCloud  bool             `json:"keep_cloud,omitempty"`
	// Workers and ChunkSize tune the fetch transfer; they need DownloadTo.
	Workers   int   `json:"workers,omitempty"`
	ChunkSize int64 `json:"chunk_size,omitempty"`
}

// Validate rejects malformed share inputs before any provider call.
func (s ShareOperationRequest) Validate() error {
	if strings.TrimSpace(s.Link) == "" {
		return fmt.Errorf("%w: share link is required", ErrInvalidOperation)
	}
	if err := validateOfflineSelection(s.Selection); err != nil {
		return err
	}
	return validateTransferTuning(s.Workers, s.ChunkSize, s.DownloadTo)
}

// OfflineOperationRequest carries the provider-neutral inputs of an offline
// download or fetch. It is only valid with OperationDownload; a non-empty
// DownloadTo selects the fetch type, otherwise the cloud-only type is used.
// An empty ParseTo means the provider's default staging directory.
type OfflineOperationRequest struct {
	SourceKind string `json:"source_kind,omitempty"`
	Source     string `json:"source"`
	Title      string `json:"title,omitempty"`
	// Mount selects the provider when ParseTo is empty; the parse directory
	// then defaults to the provider's magnet/system directory. When both are
	// given, Mount must agree with ParseTo's mount.
	Mount      string           `json:"mount,omitempty"`
	ParseTo    string           `json:"parse_to,omitempty"`
	DownloadTo string           `json:"download_to,omitempty"`
	Selection  OfflineSelection `json:"selection,omitempty"`
	Mkdir      bool             `json:"mkdir,omitempty"`
	New        bool             `json:"new,omitempty"`
	KeepCloud  bool             `json:"keep_cloud,omitempty"`
	// Workers and ChunkSize tune the fetch transfer: how many bounded Range
	// requests run at once and how many bytes each covers. Zero selects the
	// defaults. They need a download destination, so they are rejected for a
	// cloud-only task.
	Workers   int   `json:"workers,omitempty"`
	ChunkSize int64 `json:"chunk_size,omitempty"`
	// Flat forces a fetch to place every file by its own name instead of
	// mirroring the staged layout. It needs a download destination.
	Flat bool `json:"flat,omitempty"`
}

// Offline transfer tuning. A fetch copies staged content with bounded Range
// requests; the caller may choose how many are in flight and how large each one
// is. The bounds come from provider measurements: a bounded Range request costs
// roughly 0.3s of fixed latency, so chunks below 1MiB spend most of their time
// on overhead (8 concurrent 256KiB chunks reached only ~4MB/s while 8
// concurrent 1MiB chunks reached ~16MB/s), and chunks far above the default
// mostly increase what a failed request has to re-fetch. These bounds are
// shared with the CLI so the flags, the validation, and the documentation
// cannot drift apart.
const (
	DefaultOfflineTransferWorkers = 4
	MaxOfflineTransferWorkers     = 16
	DefaultOfflineTransferChunk   = 8 << 20
	MinOfflineTransferChunk       = 1 << 20
	MaxOfflineTransferChunk       = 64 << 20
)

// validateOfflineTransferTuning rejects transfer tuning that cannot apply or
// is outside the supported range, before a task or provider call exists.
func validateOfflineTransferTuning(o OfflineOperationRequest) error {
	return validateTransferTuning(o.Workers, o.ChunkSize, o.DownloadTo)
}

// validateTransferTuning rejects transfer tuning that cannot apply or is
// outside the supported range. It is shared by offline fetches and share
// fetches, which transfer through the same bounded-range core.
func validateTransferTuning(workers int, chunkSize int64, downloadTo string) error {
	if workers < 0 {
		return fmt.Errorf("%w: workers must not be negative", ErrInvalidOperation)
	}
	if workers > MaxOfflineTransferWorkers {
		return fmt.Errorf("%w: workers must be at most %d", ErrInvalidOperation, MaxOfflineTransferWorkers)
	}
	if chunkSize < 0 {
		return fmt.Errorf("%w: chunk size must not be negative", ErrInvalidOperation)
	}
	if chunkSize > MaxOfflineTransferChunk {
		return fmt.Errorf("%w: chunk size must be at most %s", ErrInvalidOperation, OfflineByteSize(MaxOfflineTransferChunk))
	}
	if chunkSize > 0 && chunkSize < MinOfflineTransferChunk {
		return fmt.Errorf("%w: chunk size must be at least %s", ErrInvalidOperation, OfflineByteSize(MinOfflineTransferChunk))
	}
	if (workers > 0 || chunkSize > 0) && strings.TrimSpace(downloadTo) == "" {
		return fmt.Errorf("%w: transfer tuning requires a download destination", ErrInvalidOperation)
	}
	return nil
}

// OfflineByteSize renders a byte count as MiB when it is a whole number of
// them, so validation errors and flag help share the documented limits.
func OfflineByteSize(bytes int64) string {
	if bytes%(1<<20) == 0 {
		return fmt.Sprintf("%dMiB", bytes>>20)
	}
	return fmt.Sprintf("%d bytes", bytes)
}

// Validate rejects malformed offline inputs before any provider task or
// destination resolution happens.
func (o OfflineOperationRequest) Validate() error {
	if strings.TrimSpace(o.Source) == "" {
		return fmt.Errorf("%w: offline source is required", ErrInvalidOperation)
	}
	if o.ParseTo != "" && o.DownloadTo != "" &&
		strings.TrimRight(o.ParseTo, "/") == strings.TrimRight(o.DownloadTo, "/") {
		return fmt.Errorf("%w: parse destination and download destination must differ", ErrInvalidOperation)
	}
	if err := validateOfflineSelection(o.Selection); err != nil {
		return err
	}
	return validateOfflineTransferTuning(o)
}

// validateOfflineSelection checks the provider-neutral selection shape shared by
// offline sources and share entries.
func validateOfflineSelection(selection OfflineSelection) error {
	if selection.Largest < 0 {
		return fmt.Errorf("%w: largest count must not be negative", ErrInvalidOperation)
	}
	switch selection.Mode {
	case "", OfflineSelectionAll:
		if selection.Largest != 0 || len(selection.Include) != 0 {
			return fmt.Errorf("%w: selection mode %q takes no largest count or include patterns", ErrInvalidOperation, selection.Mode)
		}
	case OfflineSelectionLargest:
		if selection.Largest < 1 {
			return fmt.Errorf("%w: largest selection requires a positive count", ErrInvalidOperation)
		}
		if len(selection.Include) != 0 {
			return fmt.Errorf("%w: largest selection cannot also include patterns", ErrInvalidOperation)
		}
	case OfflineSelectionInclude:
		if len(selection.Include) == 0 {
			return fmt.Errorf("%w: include selection requires at least one pattern", ErrInvalidOperation)
		}
		if selection.Largest != 0 {
			return fmt.Errorf("%w: include selection cannot also take a largest count", ErrInvalidOperation)
		}
	default:
		return fmt.Errorf("%w: unsupported offline selection mode %q", ErrInvalidOperation, selection.Mode)
	}
	return nil
}

func (r OperationRequest) Validate() error {
	if !validOperationKind(r.Operation) {
		return fmt.Errorf("%w: operation is required", ErrInvalidOperation)
	}
	if r.Offline != nil {
		if r.Operation != OperationDownload {
			return fmt.Errorf("%w: offline inputs require the download operation", ErrInvalidOperation)
		}
		if len(r.Items) != 0 {
			return fmt.Errorf("%w: offline requests must not carry items", ErrInvalidOperation)
		}
		if err := r.Offline.Validate(); err != nil {
			return err
		}
	} else if r.Share != nil {
		want := OperationShare
		if strings.TrimSpace(r.Share.DownloadTo) != "" {
			want = OperationDownload
		}
		if r.Operation != want {
			return fmt.Errorf("%w: share inputs use the %q operation", ErrInvalidOperation, want)
		}
		if len(r.Items) != 0 {
			return fmt.Errorf("%w: share requests must not carry items", ErrInvalidOperation)
		}
		if err := r.Share.Validate(); err != nil {
			return err
		}
	} else if len(r.Items) == 0 {
		return fmt.Errorf("%w: at least one item is required", ErrInvalidOperation)
	}
	if r.Operation == OperationUpload {
		if r.UploadSource == "" {
			r.UploadSource = UploadSourceApp
		}
		if r.UploadSource != UploadSourceApp && r.UploadSource != UploadSourceLocal {
			return fmt.Errorf("%w: unsupported upload source %q", ErrInvalidOperation, r.UploadSource)
		}
		if r.UploadPolicy == "" {
			r.UploadPolicy = UploadPolicyPreferDirect
		}
		if r.UploadPolicy != UploadPolicyPreferDirect && r.UploadPolicy != UploadPolicyStagingOnly {
			return fmt.Errorf("%w: unsupported upload policy %q", ErrInvalidOperation, r.UploadPolicy)
		}
	} else if r.UploadPolicy != "" || r.UploadSource != "" {
		return fmt.Errorf("%w: upload options are only valid for upload operations", ErrInvalidOperation)
	}
	if strings.TrimSpace(r.IdempotencyKey) != r.IdempotencyKey {
		return fmt.Errorf("%w: idempotency key must not have surrounding whitespace", ErrInvalidOperation)
	}
	return nil
}

func validOperationKind(kind OperationKind) bool {
	switch kind {
	case OperationUpload, OperationDownload, OperationDelete, OperationCopy, OperationMove, OperationShare:
		return true
	default:
		return false
	}
}

// OperationForType reports the operation category declared for a task type, or
// "" when the type is not declared. Creation paths reject an undeclared type
// instead of persisting a task without operation identity.
func OperationForType(typ Type) OperationKind {
	descriptor, _ := Describe(typ)
	return descriptor.Operation
}
