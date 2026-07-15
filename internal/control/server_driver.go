package control

import (
	"context"
	"encoding/json"
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

type DriverTestRequest struct {
	Test           string `json:"test"`
	Mount          string `json:"mount,omitempty"`
	Source         string `json:"source,omitempty"`
	Dest           string `json:"dest,omitempty"`
	Size           string `json:"size,omitempty"`
	VFS            bool   `json:"vfs,omitempty"`
	Samples        int    `json:"samples,omitempty"`
	SampleInterval string `json:"sample_interval,omitempty"`
}

type DriverProbeRequest = DriverTestRequest

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
		if mount.Identity.Driver == nil {
			continue
		}
		drivers = append(drivers, DebugDriverSummary{
			Mount:        mount.Identity.Name,
			Capabilities: mount.Identity.Capabilities,
			Driver:       *mount.Identity.Driver,
			Metrics:      mount.DriverMetricEvents(),
			Space:        spaceByMount[mount.Identity.Name],
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
		space, err := item.Driver.Space(ctx)
		summary := &DebugSpaceSummary{}
		if errors.Is(err, drive.ErrSpaceUnsupported) {
			summary.Unsupported = true
			summary.Reason = err.Error()
			summary.ErrorCategory = drive.ErrorCategory(err)
		} else if err != nil {
			summary.Error = err.Error()
			summary.ErrorCategory = drive.ErrorCategory(err)
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
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	provider, ok := s.source.(vfs.DriverProvider)
	if !ok {
		http.Error(w, "driver test not available", http.StatusNotImplemented)
		return
	}
	var req DriverTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	drivers := provider.Drivers()
	testType := strings.ToLower(strings.TrimSpace(req.Test))

	switch testType {
	case "auth":
		var results []AuthTestResult
		matched := false
		for _, nd := range drivers {
			if req.Mount != "" && nd.Name != req.Mount {
				continue
			}
			matched = true
			result := RunDriverAuthTest(r.Context(), nd.Name, nd.Driver)
			results = append(results, *result)
		}
		if req.Mount != "" && !matched {
			http.Error(w, fmt.Sprintf("mount %q not found", req.Mount), http.StatusNotFound)
			return
		}
		writeJSON(w, results)

	case "crud", "instantupload":
		var results []CRUDTestResult
		matched := false
		for _, nd := range drivers {
			if req.Mount != "" && nd.Name != req.Mount {
				continue
			}
			matched = true
			switch testType {
			case "crud":
				result := RunDriverCRUDTest(r.Context(), nd.Name, nd.Driver)
				results = append(results, *result)
			case "instantupload":
				result := RunDriverInstantUploadTest(r.Context(), nd.Name, nd.Driver)
				results = append(results, *result)
			}
		}
		if req.Mount != "" && !matched {
			http.Error(w, fmt.Sprintf("mount %q not found", req.Mount), http.StatusNotFound)
			return
		}
		writeJSON(w, results)

	case "xfer":
		srcMount := req.Source
		dstMount := req.Dest
		if srcMount == "" || dstMount == "" {
			http.Error(w, "xfer test requires source and dest", http.StatusBadRequest)
			return
		}
		if srcMount == dstMount {
			http.Error(w, "source and dest must be different mounts", http.StatusBadRequest)
			return
		}
		size := parseXferSize(req.Size)

		if req.VFS {
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

	case "fs":
		if req.Mount == "" {
			http.Error(w, "fs test requires --mount", http.StatusBadRequest)
			return
		}
		filesys, ok := s.source.(vfs.FileSystem)
		if !ok {
			http.Error(w, "VFS fs test not available: source does not implement FileSystem", http.StatusNotImplemented)
			return
		}
		matched := false
		for _, nd := range drivers {
			if nd.Name == req.Mount {
				matched = true
				break
			}
		}
		if !matched {
			http.Error(w, fmt.Sprintf("mount %q not found", req.Mount), http.StatusNotFound)
			return
		}
		result := RunVFSSmokeTest(r.Context(), filesys, req.Mount, parseXferSize(req.Size))
		writeJSON(w, result)

	default:
		http.Error(w, fmt.Sprintf("unknown driver test: %s", req.Test), http.StatusBadRequest)
		return
	}
}

func (s *Server) handleBench(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	provider, ok := s.source.(vfs.DriverProvider)
	if !ok {
		http.Error(w, "benchmark not available", http.StatusNotImplemented)
		return
	}
	var req DriverTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	testType := strings.ToLower(strings.TrimSpace(req.Test))
	samples := req.Samples
	if samples <= 0 {
		samples = 1
	}
	interval, err := parseBenchmarkSampleInterval(req.SampleInterval)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	switch testType {
	case "crud":
		if req.Source != "" || req.Dest != "" || req.Size != "" || req.VFS {
			http.Error(w, "crud benchmark only supports mount", http.StatusBadRequest)
			return
		}
		s.writeCRUDBenchmark(w, r, provider, req, samples, interval)
	case "fs":
		if req.Mount == "" {
			http.Error(w, "fs benchmark requires mount", http.StatusBadRequest)
			return
		}
		if req.Source != "" || req.Dest != "" || req.VFS {
			http.Error(w, "fs benchmark only supports mount and size", http.StatusBadRequest)
			return
		}
		filesys, ok := s.source.(vfs.FileSystem)
		if !ok {
			http.Error(w, "VFS fs benchmark not available: source does not implement FileSystem", http.StatusNotImplemented)
			return
		}
		s.writeFSBenchmark(w, r, provider, filesys, req, samples, interval)
	case "xfer":
		if req.Mount != "" {
			http.Error(w, "xfer benchmark uses source and dest, not mount", http.StatusBadRequest)
			return
		}
		if req.Source == "" || req.Dest == "" {
			http.Error(w, "xfer benchmark requires source and dest", http.StatusBadRequest)
			return
		}
		if req.Source == req.Dest {
			http.Error(w, "source and dest must be different mounts", http.StatusBadRequest)
			return
		}
		s.writeXferBenchmark(w, r, provider, req, samples, interval)
	default:
		http.Error(w, fmt.Sprintf("unknown benchmark: %s", req.Test), http.StatusBadRequest)
	}
}

func (s *Server) writeCRUDBenchmark(w http.ResponseWriter, r *http.Request, provider vfs.DriverProvider, req DriverTestRequest, samples int, interval time.Duration) {
	var reports []BenchmarkReport
	matched := false
	for _, nd := range provider.Drivers() {
		if req.Mount != "" && nd.Name != req.Mount {
			continue
		}
		matched = true
		probe := RunBenchmarkNetworkProbe(r.Context(), nd.Driver)
		results := make([]CRUDTestResult, 0, samples)
		for i := 0; i < samples; i++ {
			if i > 0 && interval > 0 {
				timer := time.NewTimer(interval)
				select {
				case <-r.Context().Done():
					timer.Stop()
					http.Error(w, r.Context().Err().Error(), http.StatusRequestTimeout)
					return
				case <-timer.C:
				}
			}
			result := RunDriverCRUDTest(r.Context(), nd.Name, nd.Driver)
			results = append(results, *result)
		}
		reports = append(reports, NewCRUDBenchmarkReportSamplesWithEnvironment(results, BenchmarkEnvironment{NetworkProbe: &probe}))
	}
	if req.Mount != "" && !matched {
		http.Error(w, fmt.Sprintf("mount %q not found", req.Mount), http.StatusNotFound)
		return
	}
	writeJSON(w, reports)
}

func (s *Server) writeFSBenchmark(w http.ResponseWriter, r *http.Request, provider vfs.DriverProvider, filesys vfs.FileSystem, req DriverTestRequest, samples int, interval time.Duration) {
	var driver drive.Driver
	for _, nd := range provider.Drivers() {
		if nd.Name == req.Mount {
			driver = nd.Driver
			break
		}
	}
	if driver == nil {
		http.Error(w, fmt.Sprintf("mount %q not found", req.Mount), http.StatusNotFound)
		return
	}
	probe := RunBenchmarkNetworkProbe(r.Context(), driver)
	results := make([]FSTestResult, 0, samples)
	for i := 0; i < samples; i++ {
		if i > 0 && interval > 0 {
			timer := time.NewTimer(interval)
			select {
			case <-r.Context().Done():
				timer.Stop()
				http.Error(w, r.Context().Err().Error(), http.StatusRequestTimeout)
				return
			case <-timer.C:
			}
		}
		result := RunVFSSmokeTest(r.Context(), filesys, req.Mount, parseXferSize(req.Size))
		results = append(results, *result)
	}
	writeJSON(w, []BenchmarkReport{NewFSBenchmarkReportSamplesWithEnvironment(results, BenchmarkEnvironment{NetworkProbe: &probe})})
}

func (s *Server) writeXferBenchmark(w http.ResponseWriter, r *http.Request, provider vfs.DriverProvider, req DriverTestRequest, samples int, interval time.Duration) {
	var srcDriver, dstDriver drive.Driver
	for _, nd := range provider.Drivers() {
		if nd.Name == req.Source {
			srcDriver = nd.Driver
		}
		if nd.Name == req.Dest {
			dstDriver = nd.Driver
		}
	}
	if srcDriver == nil {
		http.Error(w, fmt.Sprintf("source mount %q not found", req.Source), http.StatusNotFound)
		return
	}
	if dstDriver == nil {
		http.Error(w, fmt.Sprintf("dest mount %q not found", req.Dest), http.StatusNotFound)
		return
	}

	probe := mergeBenchmarkNetworkProbes(
		RunBenchmarkNetworkProbe(r.Context(), srcDriver),
		RunBenchmarkNetworkProbe(r.Context(), dstDriver),
	)
	results := make([]XferTestResult, 0, samples)
	for i := 0; i < samples; i++ {
		if i > 0 && interval > 0 {
			timer := time.NewTimer(interval)
			select {
			case <-r.Context().Done():
				timer.Stop()
				http.Error(w, r.Context().Err().Error(), http.StatusRequestTimeout)
				return
			case <-timer.C:
			}
		}
		if req.VFS {
			filesys, ok := s.source.(vfs.FileSystem)
			if !ok {
				http.Error(w, "VFS xfer benchmark not available: source does not implement FileSystem", http.StatusNotImplemented)
				return
			}
			results = append(results, *RunVFSXferTest(r.Context(), filesys, req.Source, req.Dest, parseXferSize(req.Size)))
		} else {
			results = append(results, *RunDriverXferTest(r.Context(), req.Source, srcDriver, req.Dest, dstDriver, parseXferSize(req.Size)))
		}
	}
	writeJSON(w, []BenchmarkReport{NewXferBenchmarkReportSamplesWithEnvironment(results, BenchmarkEnvironment{NetworkProbe: &probe})})
}

func mergeBenchmarkNetworkProbes(src, dst BenchmarkNetworkProbe) BenchmarkNetworkProbe {
	probe := BenchmarkNetworkProbe{
		Status:          "ok",
		Started:         src.Started,
		Finished:        dst.Finished,
		Steps:           append(append([]BenchmarkProbeStep(nil), src.Steps...), dst.Steps...),
		EventCount:      src.EventCount + dst.EventCount,
		RetryCount:      src.RetryCount + dst.RetryCount,
		ErrorCount:      src.ErrorCount + dst.ErrorCount,
		EventOperations: map[string]int{},
		Events:          append(append([]drive.MetricEvent(nil), src.Events...), dst.Events...),
	}
	if probe.Started.IsZero() || (!dst.Started.IsZero() && dst.Started.Before(probe.Started)) {
		probe.Started = dst.Started
	}
	if src.Finished.After(probe.Finished) {
		probe.Finished = src.Finished
	}
	if probe.Finished.After(probe.Started) {
		duration := probe.Finished.Sub(probe.Started)
		probe.Duration = duration.String()
		probe.DurationMS = durationMillis(duration)
	}
	for operation, count := range src.EventOperations {
		probe.EventOperations[operation] += count
	}
	for operation, count := range dst.EventOperations {
		probe.EventOperations[operation] += count
	}
	if len(probe.EventOperations) == 0 {
		probe.EventOperations = nil
	}
	probe.APILatency = benchmarkStats(probeStepDurations(probe.Steps))
	switch {
	case src.Status == "degraded" || dst.Status == "degraded":
		probe.Status = "degraded"
	case src.Status == "unstable" || dst.Status == "unstable":
		probe.Status = "unstable"
	case src.Status == "" || dst.Status == "":
		probe.Status = ""
	default:
		probe.Status = "ok"
	}
	return probe
}

func parseBenchmarkSampleInterval(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid sample_interval %q", value)
	}
	if duration < 0 {
		return 0, fmt.Errorf("sample_interval must not be negative")
	}
	return duration, nil
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
