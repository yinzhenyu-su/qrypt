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
	Operation    OperationKind  `json:"operation"`
	Scope        Scope          `json:"scope,omitempty"`
	Items        []Item         `json:"items,omitempty"`
	Options      Options        `json:"options,omitempty"`
	Detail       map[string]any `json:"detail,omitempty"`
	UploadPolicy UploadPolicy   `json:"upload_policy,omitempty"`
	UploadSource UploadSource   `json:"upload_source,omitempty"`
	Idempotency  string         `json:"idempotency_key,omitempty"`
}

func (r OperationRequest) Validate() error {
	if !validOperationKind(r.Operation) || len(r.Items) == 0 {
		return fmt.Errorf("%w: operation and at least one item are required", ErrInvalidOperation)
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
	if strings.TrimSpace(r.Idempotency) != r.Idempotency {
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

func operationKindForType(typ Type) OperationKind {
	switch typ {
	case TypeUploadRemote, TypeUploadBatch, TypeUploadStreamBatch, TypeUploadStreamDirect:
		return OperationUpload
	case TypeDownload, TypeDownloadStreamBatch:
		return OperationDownload
	case TypeDeleteRemote, TypeDeleteBatch:
		return OperationDelete
	case TypeCopy:
		return OperationCopy
	case TypeMoveRemote:
		return OperationMove
	default:
		return ""
	}
}
