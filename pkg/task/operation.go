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
	if o.Workers < 0 {
		return fmt.Errorf("%w: workers must not be negative", ErrInvalidOperation)
	}
	if o.Workers > MaxOfflineTransferWorkers {
		return fmt.Errorf("%w: workers must be at most %d", ErrInvalidOperation, MaxOfflineTransferWorkers)
	}
	if o.ChunkSize < 0 {
		return fmt.Errorf("%w: chunk size must not be negative", ErrInvalidOperation)
	}
	if o.ChunkSize > MaxOfflineTransferChunk {
		return fmt.Errorf("%w: chunk size must be at most %s", ErrInvalidOperation, OfflineByteSize(MaxOfflineTransferChunk))
	}
	if o.ChunkSize > 0 && o.ChunkSize < MinOfflineTransferChunk {
		return fmt.Errorf("%w: chunk size must be at least %s", ErrInvalidOperation, OfflineByteSize(MinOfflineTransferChunk))
	}
	if (o.Workers > 0 || o.ChunkSize > 0) && strings.TrimSpace(o.DownloadTo) == "" {
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
	if o.Selection.Largest < 0 {
		return fmt.Errorf("%w: largest count must not be negative", ErrInvalidOperation)
	}
	switch o.Selection.Mode {
	case "", OfflineSelectionAll:
		if o.Selection.Largest != 0 || len(o.Selection.Include) != 0 {
			return fmt.Errorf("%w: selection mode %q takes no largest count or include patterns", ErrInvalidOperation, o.Selection.Mode)
		}
	case OfflineSelectionLargest:
		if o.Selection.Largest < 1 {
			return fmt.Errorf("%w: largest selection requires a positive count", ErrInvalidOperation)
		}
		if len(o.Selection.Include) != 0 {
			return fmt.Errorf("%w: largest selection cannot also include patterns", ErrInvalidOperation)
		}
	case OfflineSelectionInclude:
		if len(o.Selection.Include) == 0 {
			return fmt.Errorf("%w: include selection requires at least one pattern", ErrInvalidOperation)
		}
		if o.Selection.Largest != 0 {
			return fmt.Errorf("%w: include selection cannot also take a largest count", ErrInvalidOperation)
		}
	default:
		return fmt.Errorf("%w: unsupported offline selection mode %q", ErrInvalidOperation, o.Selection.Mode)
	}
	return validateOfflineTransferTuning(o)
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
	case OperationUpload, OperationDownload, OperationDelete, OperationCopy, OperationMove:
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
