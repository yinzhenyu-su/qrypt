package core

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// Client-owned value types and client-facing methods. pkg/core is
// pkg/mobile's application boundary: mobile consumes filesystem value types
// through these contracts and never through pkg/vfs. The structs mirror the
// VFS definitions field for field (same names, types and JSON tags), and the
// *Client methods adapt VFS values into them internally with the conversion
// helpers below. Because the conversion is a direct struct conversion, any
// drift in the VFS field names or types breaks compilation here. Go ignores
// struct tags when converting, so tag/schema drift is not caught by
// compilation; the JSON round-trip tests in client_test.go are what pin the
// mobile wire format.
//
// The legacy Core.ListPage / Core.Capabilities / Core.Mounts methods keep
// their original VFS-typed signatures for source compatibility; the *Client
// variants below are the boundary for client-facing code.

// ListPageResult is a deterministic page of a directory listing. Entries
// are sorted by name (then id) so a name cursor stays stable while the
// directory changes between requests.
type ListPageResult struct {
	Entries    []drive.Entry `json:"entries,omitempty"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// CapabilityInfo reports which operations are allowed on a path.
type CapabilityInfo struct {
	Mount          string             `json:"mount,omitempty"`
	Path           string             `json:"path"`
	Root           bool               `json:"root"`
	MountRoot      bool               `json:"mount_root"`
	Capabilities   []drive.Capability `json:"capabilities,omitempty"`
	CanRead        bool               `json:"can_read"`
	CanList        bool               `json:"can_list"`
	CanUpload      bool               `json:"can_upload"`
	CanMkdir       bool               `json:"can_mkdir"`
	CanRename      bool               `json:"can_rename"`
	CanMove        bool               `json:"can_move"`
	CanRemove      bool               `json:"can_remove"`
	CanSpace       bool               `json:"can_space"`
	CanUploadChild bool               `json:"can_upload_child"`
	CanMkdirChild  bool               `json:"can_mkdir_child"`
	CanRenameChild bool               `json:"can_rename_child"`
	CanMoveChild   bool               `json:"can_move_child"`
	CanRemoveChild bool               `json:"can_remove_child"`
}

// MountInfo describes one mounted filesystem domain. State is "failed" when
// a configured mount could not be initialized and was excluded from the
// namespace; Error carries the initialization failure for failed mounts.
type MountInfo struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Encrypted bool   `json:"encrypted"`
	State     string `json:"state,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ReadPriority controls how read slots are allocated under contention in the
// filesystem read scheduler. The values mirror the scheduler's own priority
// classes (pkg/vfs/read): PriorityLow for prefetch and anticipatory reads,
// PriorityNormal for default user-initiated reads, PriorityHigh for
// UI-critical reads. Core maps a ReadPriority onto a read call in
// ReadAtIntoWithPriority; clients never paint scheduling values onto a
// context themselves.
type ReadPriority int

const (
	PriorityLow    ReadPriority = iota // prefetch, anticipatory reads
	PriorityNormal                     // default user-initiated reads
	PriorityHigh                       // UI-critical reads (thumbnails, visible content)
)

// ListPageClient is the client-facing paging API. It behaves exactly like
// ListPage but returns the core-owned ListPageResult contract, so callers
// never depend on pkg/vfs.
func (c *Core) ListPageClient(ctx context.Context, path, cursor string, limit int) (ListPageResult, error) {
	result, err := c.ListPage(ctx, path, cursor, limit)
	if err != nil {
		return ListPageResult{}, err
	}
	return listPageResultFromVFS(result), nil
}

// CapabilitiesClient is the client-facing capability API. It behaves exactly
// like Capabilities but returns the core-owned CapabilityInfo contract.
func (c *Core) CapabilitiesClient(ctx context.Context, path string) (CapabilityInfo, error) {
	info, err := c.Capabilities(ctx, path)
	if err != nil {
		return CapabilityInfo{}, err
	}
	return capabilityInfoFromVFS(info), nil
}

// MountsClient is the client-facing mount introspection API. It behaves
// exactly like Mounts but returns the core-owned MountInfo contract.
func (c *Core) MountsClient() ([]MountInfo, error) {
	mounts, err := c.Mounts()
	if err != nil {
		return nil, err
	}
	out := make([]MountInfo, len(mounts))
	for i, info := range mounts {
		out[i] = mountInfoFromVFS(info)
	}
	return out, nil
}

// Conversion helpers. These adapt VFS implementation values into the
// client-owned contracts at the boundary; the underlying layouts are
// identical, so the conversion is a direct struct conversion. The direct
// conversion catches field/type layout drift at compile time, but Go
// ignores struct tags for conversion — the JSON encoding of a converted
// value is not preserved by construction. The JSON tests catch any
// tag/schema drift between the client contract and the VFS type.

func listPageResultFromVFS(result vfs.ListPageResult) ListPageResult {
	return ListPageResult(result)
}

func capabilityInfoFromVFS(info vfs.CapabilityInfo) CapabilityInfo {
	return CapabilityInfo(info)
}

func mountInfoFromVFS(info vfs.MountInfo) MountInfo {
	return MountInfo(info)
}

// priorityToVFS maps a client ReadPriority to the scheduler priority used
// by the VFS read domain.
func priorityToVFS(priority ReadPriority) vfs.ReadPriority {
	return vfs.ReadPriority(priority)
}
