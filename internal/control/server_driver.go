package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func (s *Server) handleDriver(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snapshot := s.source.DebugSnapshot()
	var spaceByMount map[string]*DebugSpaceSummary
	if parseBoolQuery(r.URL.Query().Get("space")) {
		spaceByMount = s.driverSpaces(r.Context())
	}
	var drivers []DebugDriverSummary
	for _, mount := range snapshot.Mounts {
		if mount.Driver == nil {
			continue
		}
		drivers = append(drivers, DebugDriverSummary{
			Mount:        mount.Name,
			Capabilities: mount.Capabilities,
			Driver:       *mount.Driver,
			Space:        spaceByMount[mount.Name],
		})
	}
	sort.Slice(drivers, func(i, j int) bool {
		return drivers[i].Mount < drivers[j].Mount
	})
	writeJSON(w, DriversResponse{
		SchemaVersion: snapshot.SchemaVersion,
		GeneratedAt:   snapshot.GeneratedAt,
		Drivers:       drivers,
	})
}

func (s *Server) handleMountHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	checker, ok := s.source.(vfs.MountHealthChecker)
	if !ok {
		http.Error(w, "mount health not available", http.StatusNotImplemented)
		return
	}
	results, err := checker.MountHealth(r.Context(), r.URL.Query().Get("mount"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, MountHealthResponse{
		SchemaVersion: vfs.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Now(),
		Mounts:        results,
	})
}

func (s *Server) driverSpaces(ctx context.Context) map[string]*DebugSpaceSummary {
	provider, ok := s.source.(vfs.DriverProvider)
	if !ok {
		return nil
	}
	spaces := map[string]*DebugSpaceSummary{}
	for _, item := range provider.Drivers() {
		querier, ok := item.Driver.(drive.SpaceQuerier)
		if !ok {
			continue
		}
		space, err := querier.Space(ctx)
		summary := &DebugSpaceSummary{}
		if errors.Is(err, drive.ErrSpaceUnsupported) {
			summary.Unsupported = true
			summary.Reason = err.Error()
		} else if err != nil {
			summary.Error = err.Error()
		} else {
			summary.BytesTotal = space.Total
			summary.BytesFree = space.Free
			summary.Total = osutil.FormatBytes(space.Total)
			summary.Free = osutil.FormatBytes(space.Free)
		}
		spaces[item.Name] = summary
	}
	return spaces
}

func (s *Server) handleDriverTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	provider, ok := s.source.(vfs.DriverProvider)
	if !ok {
		http.Error(w, "driver test not available", http.StatusNotImplemented)
		return
	}
	drivers := provider.Drivers()
	mountFilter := r.URL.Query().Get("mount")
	testType := r.URL.Query().Get("test")

	switch testType {
	case "crud", "instantupload":
		var results []CRUDTestResult
		for _, nd := range drivers {
			if mountFilter != "" && nd.Name != mountFilter {
				continue
			}
			switch testType {
			case "crud":
				result := RunDriverCRUDTest(r.Context(), nd.Name, nd.Driver)
				results = append(results, *result)
			case "instantupload":
				result := RunDriverInstantUploadTest(r.Context(), nd.Name, nd.Driver)
				results = append(results, *result)
			}
		}
		writeJSON(w, results)

	case "xfer":
		srcMount := r.URL.Query().Get("source")
		dstMount := r.URL.Query().Get("dest")
		if srcMount == "" || dstMount == "" {
			http.Error(w, "xfer test requires source and dest query params", http.StatusBadRequest)
			return
		}
		if srcMount == dstMount {
			http.Error(w, "source and dest must be different mounts", http.StatusBadRequest)
			return
		}
		size := parseXferSize(r.URL.Query().Get("size"))

		useVFS := parseBoolQuery(r.URL.Query().Get("vfs"))
		if useVFS {
			filesys, ok := s.source.(vfs.FileSystem)
			if !ok {
				http.Error(w, "VFS xfer test not available: source does not implement FileSystem", http.StatusNotImplemented)
				return
			}
			result := RunVFSXferTest(r.Context(), filesys, srcMount, dstMount, size)
			writeJSON(w, result)
		} else {
			var srcDriver, dstDriver drive.Driver
			for _, nd := range drivers {
				if nd.Name == srcMount {
					srcDriver = nd.Driver
				}
				if nd.Name == dstMount {
					dstDriver = nd.Driver
				}
			}
			if srcDriver == nil {
				http.Error(w, fmt.Sprintf("source mount %q not found", srcMount), http.StatusNotFound)
				return
			}
			if dstDriver == nil {
				http.Error(w, fmt.Sprintf("dest mount %q not found", dstMount), http.StatusNotFound)
				return
			}
			result := RunDriverXferTest(r.Context(), srcMount, srcDriver, dstMount, dstDriver, size)
			writeJSON(w, result)
		}

	default:
		http.Error(w, fmt.Sprintf("unknown test type: %s", testType), http.StatusBadRequest)
		return
	}
}

// parseXferSize parses the size query param for xfer tests.
// Accepts plain bytes, or binary suffixes: k/K (*1024), m/M (*1048576), g/G (*1073741824).
func parseXferSize(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	var multiplier int64 = 1
	last := value[len(value)-1]
	switch {
	case last == 'k' || last == 'K':
		multiplier = 1 << 10
		value = value[:len(value)-1]
	case last == 'm' || last == 'M':
		multiplier = 1 << 20
		value = value[:len(value)-1]
	case last == 'g' || last == 'G':
		multiplier = 1 << 30
		value = value[:len(value)-1]
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n * multiplier
}
