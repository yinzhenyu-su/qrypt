package cli

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func addCollectDiagnostics(out *[]debugAIDiagnostic, report debugAIReport) {
	if report.Health == nil || !report.Health.OK {
		*out = append(*out, debugAIDiagnostic{
			Severity: "error",
			Code:     "debug_socket_unhealthy",
			Message:  "debug socket health check failed or did not return ok",
		})
	}
	if report.State != nil {
		for _, mount := range report.State.Mounts {
			addRootIDDiagnostics(out, mount)
			if len(mount.Pending) > 0 {
				*out = append(*out, debugAIDiagnostic{
					Severity:  "warn",
					Code:      "pending_uploads",
					Component: "vfs",
					Mount:     mount.Name,
					Message:   "mount has pending uploads",
					Evidence:  map[string]any{"count": len(mount.Pending)},
				})
			}
			for _, upload := range mount.Uploads {
				if upload.LastError == "" {
					continue
				}
				*out = append(*out, debugAIDiagnostic{
					Severity:  "error",
					Code:      "upload_error",
					Component: "vfs",
					Mount:     mount.Name,
					Path:      prefixDebugMountPath(report.State.Kind, mount.Name, upload.Path),
					Message:   upload.LastError,
					Evidence:  map[string]any{"state": upload.State, "retry_count": upload.RetryCount},
				})
			}
		}
	}
	if report.MountHealth != nil {
		for _, h := range report.MountHealth.Mounts {
			if h.OK {
				continue
			}
			*out = append(*out, debugAIDiagnostic{
				Severity:  "error",
				Code:      "mount_health_failed",
				Component: "mount",
				Mount:     h.Mount,
				Message:   firstNonEmpty(h.Error, "mount health check failed"),
				Evidence:  map[string]any{"level": h.Level, "errors": h.Errors},
			})
		}
	}
	if report.Events != nil {
		for _, event := range report.Events.Events {
			sev := strings.ToLower(event.Level)
			if sev != "error" && sev != "warn" {
				sev = "warn"
			}
			*out = append(*out, debugAIDiagnostic{
				Severity:  sev,
				Code:      "recent_event",
				Component: debugEventComponent(event.Message),
				Message:   event.Message,
				Evidence:  map[string]any{"event_id": event.ID, "time": event.Time, "suppressed": event.Suppressed},
			})
		}
	}
	addStagingDiagnostics(out, report.Staging)
	addCacheDiagnostics(out, report.Cache)
}

func addRootIDDiagnostics(out *[]debugAIDiagnostic, mount vfs.DebugMountSnapshot) {
	if mount.RootID == "" || mount.Driver == nil || mount.Driver.Stats == nil {
		return
	}
	driverRootID, ok := debugStringStat(mount.Driver.Stats[drive.DebugStatRootID])
	if !ok || driverRootID == "" || driverRootID == mount.RootID {
		return
	}
	*out = append(*out, debugAIDiagnostic{
		Severity:  "error",
		Code:      "root_id_mismatch",
		Component: "vfs",
		Mount:     mount.Name,
		Message:   "VFS root id does not match the driver resolved root id",
		Evidence: map[string]any{
			"vfs_root_id":    mount.RootID,
			"driver_root_id": driverRootID,
			"driver":         mount.DriverName,
		},
	})
}

func debugStringStat(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case int:
		return strconv.Itoa(typed), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10), true
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case fmt.Stringer:
		return typed.String(), true
	default:
		return "", false
	}
}

func addInspectDiagnostics(out *[]debugAIDiagnostic, inspect *debugAIInspect) {
	if inspect == nil {
		return
	}
	if inspect.Resolve == nil || len(inspect.Resolve.Resolves) == 0 {
		*out = append(*out, debugAIDiagnostic{
			Severity:  "error",
			Code:      "path_resolve_failed",
			Component: "vfs",
			Path:      inspect.Path,
			Message:   "path could not be resolved",
		})
	}
	if inspect.Consistency != nil {
		report := inspect.Consistency.Report
		if report.Status != "" && report.Status != "ok" && report.Status != "uploaded_pending_cleanup" && report.Status != "namespace_root" {
			*out = append(*out, debugAIDiagnostic{
				Severity:  "warn",
				Code:      "path_consistency_issue",
				Component: "vfs",
				Path:      report.Path,
				Message:   firstNonEmpty(report.Issue, "path consistency check is not ok"),
				Evidence:  map[string]any{"status": report.Status, "pending": report.Pending, "remote_found": report.RemoteFound, "size_matches": report.SizeMatches},
			})
		}
	}
	if inspect.Uploads != nil {
		for _, upload := range inspect.Uploads.Uploads {
			if upload.LastError == "" {
				continue
			}
			*out = append(*out, debugAIDiagnostic{
				Severity:  "error",
				Code:      "path_upload_error",
				Component: "vfs",
				Path:      upload.Path,
				Message:   upload.LastError,
				Evidence:  map[string]any{"state": upload.State, "retry_count": upload.RetryCount},
			})
		}
	}
	if inspect.Reads != nil {
		for _, read := range inspect.Reads.Reads {
			if read.Error == "" {
				continue
			}
			*out = append(*out, debugAIDiagnostic{
				Severity: "error", Code: "path_read_error", Component: "vfs",
				Path: read.Path, Mount: read.Mount, Message: read.Error,
				Evidence: map[string]any{"op_id": read.OpID, "error_category": read.ErrorCategory, "bytes": read.Bytes},
			})
		}
	}
	addStagingDiagnostics(out, inspect.Staging)
	addCacheDiagnostics(out, inspect.Cache)
}

func addStagingDiagnostics(out *[]debugAIDiagnostic, staging *control.StagingResponse) {
	if staging == nil {
		return
	}
	for _, mount := range staging.Mounts {
		for _, file := range mount.Files {
			hasIssue := file.Issue != "" || file.Pending && !file.Exists || file.Pending && file.Exists && !file.SizeMatches
			if !hasIssue {
				continue
			}
			code := "staging_issue"
			if file.Pending && !file.Exists {
				code = "staging_missing"
			} else if file.Exists && !file.SizeMatches {
				code = "staging_size_mismatch"
			}
			*out = append(*out, debugAIDiagnostic{
				Severity:  "warn",
				Code:      code,
				Component: "staging",
				Mount:     mount.Mount,
				Path:      file.Path,
				Message:   firstNonEmpty(file.Issue, "staging file state is inconsistent"),
				Evidence:  map[string]any{"local_path": file.LocalPath, "pending_size": file.PendingSize, "staging_size": file.StagingSize, "upload_in_progress": file.UploadInProgress},
			})
		}
		for _, file := range mount.Orphans {
			*out = append(*out, debugAIDiagnostic{
				Severity:  "warn",
				Code:      "staging_orphan",
				Component: "staging",
				Mount:     mount.Mount,
				Path:      file.Path,
				Message:   firstNonEmpty(file.Issue, "staging file is not referenced by pending upload state"),
				Evidence:  map[string]any{"local_path": file.LocalPath, "staging_size": file.StagingSize},
			})
		}
	}
}

func addCacheDiagnostics(out *[]debugAIDiagnostic, cache *control.CacheResponse) {
	if cache == nil {
		return
	}
	for _, mount := range cache.Mounts {
		if mount.Cache.LastGetError != "" {
			*out = append(*out, debugAIDiagnostic{
				Severity:  "warn",
				Code:      "read_cache_get_error",
				Component: "cache",
				Mount:     mount.Mount,
				Path:      cache.Path,
				Message:   mount.Cache.LastGetError,
				Evidence:  map[string]any{"at": mount.Cache.LastGetErrorAt},
			})
		}
		if mount.Cache.LastPutError != "" {
			*out = append(*out, debugAIDiagnostic{
				Severity:  "warn",
				Code:      "read_cache_put_error",
				Component: "cache",
				Mount:     mount.Mount,
				Path:      cache.Path,
				Message:   mount.Cache.LastPutError,
				Evidence:  map[string]any{"at": mount.Cache.LastPutErrorAt},
			})
		}
	}
}

func cleanDebugPath(path string) string {
	if path == "" {
		return ""
	}
	cleaned := filepath.Clean("/" + strings.TrimPrefix(path, "/"))
	if cleaned == "." {
		return "/"
	}
	return cleaned
}

func prefixDebugMountPath(kind, mountName, path string) string {
	if kind != "namespace" || mountName == "" {
		return cleanDebugPath(path)
	}
	return cleanDebugPath("/" + strings.Trim(mountName, "/") + "/" + strings.TrimPrefix(path, "/"))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func debugEventComponent(message string) string {
	if !strings.HasPrefix(message, "[") {
		return ""
	}
	end := strings.Index(message, "]")
	if end <= 1 {
		return ""
	}
	return strings.ToLower(message[1:end])
}
