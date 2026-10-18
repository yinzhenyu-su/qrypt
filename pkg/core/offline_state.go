package core

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

// offlinePhase is the durable phase of an offline task. It survives restart so
// an interrupted fetch resumes at the phase it stopped in.
type offlinePhase string

const (
	offlinePhaseStaging  offlinePhase = "staging"
	offlinePhaseTransfer offlinePhase = "transfer"
	offlinePhaseCleanup  offlinePhase = "cleanup"
	offlinePhaseComplete offlinePhase = "complete"
)

func (p offlinePhase) display() task.Phase {
	switch p {
	case offlinePhaseStaging:
		return task.PhaseServerStage
	case offlinePhaseTransfer:
		return task.PhaseDownload
	case offlinePhaseCleanup:
		return task.PhaseDelete
	case offlinePhaseComplete:
		return task.PhaseComplete
	default:
		return task.PhasePending
	}
}

type offlineDetailSelection struct {
	Mode    string   `json:"mode,omitempty"`
	Largest int      `json:"largest,omitempty"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// offlineObservation is the latest provider observation folded into the task
// detail. Progress counters also land in Task.Progress; this copy keeps the
// provider's own wording and error classification.
type offlineObservation struct {
	State               string    `json:"state,omitempty"`
	Percent             float64   `json:"percent,omitempty"`
	FilesDone           int64     `json:"files_done,omitempty"`
	FilesTotal          int64     `json:"files_total,omitempty"`
	BytesDone           int64     `json:"bytes_done,omitempty"`
	BytesTotal          int64     `json:"bytes_total,omitempty"`
	DestinationVerified bool      `json:"destination_verified,omitempty"`
	ErrorCode           string    `json:"error_code,omitempty"`
	Retryable           bool      `json:"retryable,omitempty"`
	ProviderError       string    `json:"provider_error,omitempty"`
	UpdatedAt           time.Time `json:"updated_at,omitempty"`
}

type offlineResultFile struct {
	Index    int    `json:"index,omitempty"`
	Name     string `json:"name,omitempty"`
	Size     int64  `json:"size,omitempty"`
	RemoteID string `json:"remote_id,omitempty"`
}

// offlineDetail is the durable, provider-neutral metadata of an offline task.
// It lives under Task.Detail[task.OfflineDetailKey]; provider identifiers and
// observations stay here so the common task schema gains no provider fields.
type offlineDetail struct {
	Source         string                 `json:"source"`
	SourceKind     string                 `json:"source_kind,omitempty"`
	SourceIdentity string                 `json:"source_identity,omitempty"`
	Title          string                 `json:"title,omitempty"`
	ParseMount     string                 `json:"parse_mount,omitempty"`
	ParseDir       string                 `json:"parse_dir,omitempty"`
	ParseRemoteID  string                 `json:"parse_remote_id,omitempty"`
	Selection      offlineDetailSelection `json:"selection,omitempty"`
	Mkdir          bool                   `json:"mkdir,omitempty"`

	DownloadTo    string `json:"download_to,omitempty"`
	DownloadMount string `json:"download_mount,omitempty"`
	DownloadDir   string `json:"download_dir,omitempty"`
	KeepCloud     bool   `json:"keep_cloud,omitempty"`
	Accelerated   bool   `json:"accelerated,omitempty"`
	// TransferWorkers and TransferChunk are the persisted transfer tuning, so
	// a recovered or retried fetch keeps the caller's choice. Zero means the
	// Core defaults.
	TransferWorkers int   `json:"transfer_workers,omitempty"`
	TransferChunk   int64 `json:"transfer_chunk,omitempty"`

	ProviderID          string              `json:"provider_id,omitempty"`
	PreviousProviderIDs []string            `json:"previous_provider_ids,omitempty"`
	Phase               offlinePhase        `json:"phase,omitempty"`
	Observation         *offlineObservation `json:"observation,omitempty"`
	ResultFiles         []offlineResultFile `json:"result_files,omitempty"`
	StagedFIDs          []string            `json:"staged_fids,omitempty"`
	Cleanup             string              `json:"cleanup,omitempty"`
	TransferDone        bool                `json:"transfer_done,omitempty"`
	CleanupDone         bool                `json:"cleanup_done,omitempty"`
}

func offlineDetailSelectionFromTask(selection task.OfflineSelection) offlineDetailSelection {
	out := offlineDetailSelection{
		Mode:    string(selection.Mode),
		Largest: selection.Largest,
		Include: append([]string(nil), selection.Include...),
		Exclude: append([]string(nil), selection.Exclude...),
	}
	if out.Mode == "" {
		out.Mode = string(task.OfflineSelectionAll)
	}
	return out
}

func (s offlineDetailSelection) toDrive() drive.OfflineSelection {
	return drive.OfflineSelection{
		Mode:    drive.OfflineSelectionMode(s.Mode),
		Largest: s.Largest,
		Include: append([]string(nil), s.Include...),
		Exclude: append([]string(nil), s.Exclude...),
	}
}

// decodeOfflineDetail reads the durable offline metadata from a task detail
// map. It accepts both the in-process struct form and the JSON map form a
// journal replay produces.
func decodeOfflineDetail(detail map[string]any) (offlineDetail, bool) {
	if detail == nil {
		return offlineDetail{}, false
	}
	value, ok := detail[task.OfflineDetailKey]
	if !ok || value == nil {
		return offlineDetail{}, false
	}
	switch typed := value.(type) {
	case offlineDetail:
		return typed, true
	case *offlineDetail:
		if typed == nil {
			return offlineDetail{}, false
		}
		return *typed, true
	}
	data, err := json.Marshal(value)
	if err != nil {
		return offlineDetail{}, false
	}
	var decoded offlineDetail
	if err := json.Unmarshal(data, &decoded); err != nil {
		return offlineDetail{}, false
	}
	return decoded, true
}

// apply writes the offline metadata into a task's detail map under the stable
// key and keeps the display phase in sync with the durable phase.
func (d offlineDetail) apply(taskItem *task.Task) {
	if taskItem.Detail == nil {
		taskItem.Detail = map[string]any{}
	}
	taskItem.Detail[task.OfflineDetailKey] = d
	taskItem.Detail["phase"] = d.Phase.display()
}

func (d *offlineDetail) setObservation(observation offlineObservation) {
	observation.UpdatedAt = time.Now().UTC()
	d.Observation = &observation
}

func offlineResultFilesFromDrive(files []drive.OfflineFile) []offlineResultFile {
	if len(files) == 0 {
		return nil
	}
	out := make([]offlineResultFile, 0, len(files))
	for _, file := range files {
		out = append(out, offlineResultFile{
			Index:    file.Index,
			Name:     file.Name,
			Size:     file.Size,
			RemoteID: file.RemoteID,
		})
	}
	return out
}

func offlineFilesFromDetail(files []offlineResultFile) []drive.OfflineFile {
	if len(files) == 0 {
		return nil
	}
	out := make([]drive.OfflineFile, 0, len(files))
	for _, file := range files {
		out = append(out, drive.OfflineFile{
			Index:    file.Index,
			Name:     file.Name,
			Size:     file.Size,
			RemoteID: file.RemoteID,
		})
	}
	return out
}

// mergeOfflineResultFiles folds a provider's latest file list into the retained
// one. A provider that omits identities or sizes on a later observation must not
// erase what an earlier observation established.
func mergeOfflineResultFiles(existing, incoming []offlineResultFile) []offlineResultFile {
	if len(incoming) == 0 {
		return existing
	}
	if len(existing) == 0 {
		return incoming
	}
	byName := make(map[string]offlineResultFile, len(existing))
	byIndex := make(map[int]offlineResultFile, len(existing))
	for _, file := range existing {
		if file.Name != "" {
			byName[file.Name] = file
		}
		byIndex[file.Index] = file
	}
	out := make([]offlineResultFile, 0, len(incoming))
	for _, file := range incoming {
		previous, ok := byName[file.Name]
		if !ok && file.Name == "" {
			previous, ok = byIndex[file.Index]
		}
		if ok {
			if file.Size == 0 {
				file.Size = previous.Size
			}
			if file.RemoteID == "" {
				file.RemoteID = previous.RemoteID
			}
			if file.Name == "" {
				file.Name = previous.Name
			}
			if file.Index == 0 && previous.Index != 0 {
				file.Index = previous.Index
			}
		}
		out = append(out, file)
	}
	return out
}

func offlineResultItems(detail offlineDetail) []task.ItemTracking {
	if len(detail.ResultFiles) == 0 {
		return nil
	}
	items := make([]task.ItemTracking, 0, len(detail.ResultFiles))
	for i, file := range detail.ResultFiles {
		item := task.ItemTracking{
			ItemID:           offlineResultItemID(i, file),
			Path:             offlineJoinPath(detail.ParseDir, file.Name),
			DestPath:         offlineJoinPath(detail.ParseDir, file.Name),
			Mount:            detail.ParseMount,
			State:            task.ItemStateSucceeded,
			Phase:            task.PhaseComplete,
			RemoteID:         file.RemoteID,
			SourceBytesDone:  file.Size,
			SourceBytesTotal: file.Size,
		}
		items = append(items, item)
	}
	return items
}

func offlineResultItemID(index int, file offlineResultFile) string {
	if file.RemoteID != "" {
		return file.RemoteID
	}
	if file.Name != "" {
		return file.Name
	}
	return "file-" + strconv.Itoa(index+1)
}

func offlineJoinPath(dir, name string) string {
	if name == "" {
		return dir
	}
	if dir == "" || dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}
