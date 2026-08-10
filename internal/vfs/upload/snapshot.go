package upload

import (
	"sort"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Snapshot state constants shared between the engine and VFS debug state.
const (
	SnapshotStatePreparing  = string(drive.UploadPhasePreparing)
	SnapshotStateUploading  = string(drive.UploadPhaseUploading)
	SnapshotStateCommitting = string(drive.UploadPhaseCommitting)
	SnapshotStateCompleted  = string(drive.UploadPhaseCompleted)
	SnapshotStateFailed     = "failed"
	SnapshotStateSuperseded = "superseded"
)

func snapshotHashNames(hashes drive.SourceHashes) []string {
	names := make([]string, 0, len(hashes))
	for algorithm := range hashes {
		names = append(names, string(algorithm))
	}
	sort.Strings(names)
	return names
}
