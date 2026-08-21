package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/contracttest"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/faultinject"
)

func (s *Server) handleDriver(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snapshot := s.debugSnapshot(r)
	var spaceByMount map[string]*DebugSpaceSummary
	if parseBoolQuery(r.URL.Query().Get("space")) {
		spaceByMount = s.driverSpaces(r.Context(), debugMountQuery(r))
	}
	since, sinceOK, err := parseSinceQuery(r.URL.Query().Get("since"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	limit := parseLimitQuery(r.URL.Query().Get("limit"), 200, 2000)
	var drivers []DebugDriverSummary
	for _, mount := range snapshot.Mounts {
		if mount.Identity.Driver == nil {
			continue
		}
		metrics := filterDriverMetricEvents(mount.DriverMetricEvents(), r.URL.Query(), since, sinceOK, limit)
		drivers = append(drivers, DebugDriverSummary{
			Mount:        mount.Identity.Name,
			Capabilities: mount.Identity.Capabilities,
			Driver:       *mount.Identity.Driver,
			Metrics:      metrics,
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

func filterDriverMetricEvents(events []drive.MetricEvent, q url.Values, since time.Time, sinceOK bool, limit int) []drive.MetricEvent {
	if len(events) == 0 {
		return nil
	}
	filterPath := cleanVirtual(q.Get("path"))
	hasPath := q.Get("path") != ""
	out := make([]drive.MetricEvent, 0, len(events))
	for _, event := range events {
		if hasPath && cleanVirtual(event.Path) != filterPath {
			continue
		}
		if sinceOK && eventTime(event).Before(since) {
			continue
		}
		if !metricEventMatchesQuery(event, q) {
			continue
		}
		out = append(out, event)
	}
	sort.Slice(out, func(i, j int) bool { return eventTime(out[i]).Before(eventTime(out[j])) })
	return limitMetricEvents(out, limit)
}

func (s *Server) handleMountHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	checker, ok := s.source.(diagnostics.MountHealthChecker)
	if !ok {
		http.Error(w, "mount health not available", http.StatusNotImplemented)
		return
	}
	mounts := debugMountQuery(r)
	var results []diagnostics.MountHealth
	var err error
	if len(mounts) == 0 {
		results, err = checker.MountHealth(r.Context(), "")
	} else {
		for _, mount := range mounts {
			items, itemErr := checker.MountHealth(r.Context(), mount)
			if itemErr != nil {
				err = itemErr
				break
			}
			results = append(results, items...)
		}
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, MountHealthResponse{
		SchemaVersion: diagnostics.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Now(),
		Mounts:        results,
	})
}

func (s *Server) driverSpaces(ctx context.Context, mountNames []string) map[string]*DebugSpaceSummary {
	provider, ok := s.source.(diagnostics.DriverProvider)
	if !ok {
		return nil
	}
	spaces := map[string]*DebugSpaceSummary{}
	for _, item := range provider.Drivers() {
		if !debugMountAllowed(item.Name, mountNames) {
			continue
		}
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
			summary.Total = util.FormatBytes(space.Total)
			summary.Free = util.FormatBytes(space.Free)
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
	provider, ok := s.source.(diagnostics.DriverProvider)
	if !ok {
		http.Error(w, "driver test not available", http.StatusNotImplemented)
		return
	}
	var req contracttest.DriverTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	drivers := provider.Drivers()
	testType := strings.ToLower(strings.TrimSpace(req.Test))

	switch testType {
	case "auth", "contract", "crud", "instantupload", "multipart", "fs", "resume", "batchupload", "batchmove":
		spec, ok := contracttest.Specs()[testType]
		if !ok {
			http.Error(w, fmt.Sprintf("unknown driver test: %s", testType), http.StatusBadRequest)
			return
		}
		// VFS-layer specs need the filesystem plus an explicit mount.
		var env contracttest.TestEnv
		if spec.RequiresVFS {
			filesys, ok := s.source.(vfs.FileSystem)
			if !ok {
				http.Error(w, fmt.Sprintf("%s test not available: source does not implement FileSystem", spec.Name), http.StatusNotImplemented)
				return
			}
			if req.Mount == "" {
				http.Error(w, fmt.Sprintf("%s test requires --mount", spec.Name), http.StatusBadRequest)
				return
			}
			if spec.Name == "resume" {
				if _, ok := s.source.(faultinject.DebugUploadCancelInjector); !ok {
					http.Error(w, "VFS resume test not available: source does not support debug upload cancel injection", http.StatusNotImplemented)
					return
				}
			}
			env.FileSys = filesys
		}
		env.Ctx = r.Context()
		env.Req = req
		var results []contracttest.TestRun
		matched := false
		for _, nd := range drivers {
			if req.Mount != "" && nd.Name != req.Mount {
				continue
			}
			matched = true
			if !nd.TestEnabled {
				if req.Mount != "" {
					http.Error(w, debugTestDisabledError(nd.Name), http.StatusForbidden)
					return
				}
				continue
			}
			// Capability matrix: a spec only runs on mounts that provide its
			// prerequisites. Explicit mounts get a coded failure run, bulk
			// runs skip the mount with a reason instead of a failing step.
			if missing := contracttest.MissingCapabilities(nd.Driver, spec.Requires); len(missing) > 0 {
				reason := fmt.Sprintf("driver lacks capability %v required by %s test", missing, spec.Name)
				if req.Mount != "" {
					results = append(results, contracttest.TestRun{
						Spec: spec.Name, Mount: nd.Name,
						Pass: false, Error: reason, ErrorCategory: drive.ErrorCategoryUnsupported,
					})
				} else {
					results = append(results, contracttest.TestRun{
						Spec: spec.Name, Mount: nd.Name,
						Skipped: true, SkipReason: reason,
					})
				}
				continue
			}
			results = append(results, spec.Run(env, nd.Name, nd.Driver))
		}
		if req.Mount != "" && !matched {
			http.Error(w, fmt.Sprintf("mount %q not found", req.Mount), http.StatusNotFound)
			return
		}
		if len(results) == 0 {
			http.Error(w, "no mounts enabled for debug tests; set test_enabled = true in [[mounts]]", http.StatusForbidden)
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
		size := contracttest.ParseXferSize(req.Size)
		srcDriver, srcOK := findDebugTestDriver(drivers, srcMount)
		if !srcOK {
			http.Error(w, fmt.Sprintf("source mount %q not found", srcMount), http.StatusNotFound)
			return
		}
		if !srcDriver.TestEnabled {
			http.Error(w, debugTestDisabledError(srcMount), http.StatusForbidden)
			return
		}
		dstDriver, dstOK := findDebugTestDriver(drivers, dstMount)
		if !dstOK {
			http.Error(w, fmt.Sprintf("dest mount %q not found", dstMount), http.StatusNotFound)
			return
		}
		if !dstDriver.TestEnabled {
			http.Error(w, debugTestDisabledError(dstMount), http.StatusForbidden)
			return
		}

		if req.VFS {
			filesys, ok := s.source.(vfs.FileSystem)
			if !ok {
				http.Error(w, "VFS xfer test not available: source does not implement FileSystem", http.StatusNotImplemented)
				return
			}
			result := contracttest.RunVFSXferTest(r.Context(), filesys, srcMount, dstMount, size)
			writeJSON(w, []contracttest.TestRun{contracttest.FromXferTestResult(*result)})
		} else {
			result := contracttest.RunDriverXferTest(r.Context(), srcMount, srcDriver.Driver, dstMount, dstDriver.Driver, size)
			writeJSON(w, []contracttest.TestRun{contracttest.FromXferTestResult(*result)})
		}

	default:
		http.Error(w, fmt.Sprintf("unknown driver test: %s", req.Test), http.StatusBadRequest)
		return
	}
}

func debugTestDisabledError(mount string) string {
	return fmt.Sprintf("mount %q is not enabled for debug tests; set test_enabled = true in [[mounts]]", mount)
}

func findDebugTestDriver(drivers []diagnostics.NamedDriver, mount string) (diagnostics.NamedDriver, bool) {
	for _, nd := range drivers {
		if nd.Name == mount {
			return nd, true
		}
	}
	return diagnostics.NamedDriver{}, false
}

func (s *Server) handleBench(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	provider, ok := s.source.(diagnostics.DriverProvider)
	if !ok {
		http.Error(w, "benchmark not available", http.StatusNotImplemented)
		return
	}
	var req contracttest.DriverTestRequest
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

func (s *Server) writeCRUDBenchmark(w http.ResponseWriter, r *http.Request, provider diagnostics.DriverProvider, req contracttest.DriverTestRequest, samples int, interval time.Duration) {
	var reports []contracttest.BenchmarkReport
	matched := false
	for _, nd := range provider.Drivers() {
		if req.Mount != "" && nd.Name != req.Mount {
			continue
		}
		matched = true
		if !nd.TestEnabled {
			if req.Mount != "" {
				http.Error(w, debugTestDisabledError(nd.Name), http.StatusForbidden)
				return
			}
			continue
		}
		probe := contracttest.RunBenchmarkNetworkProbe(r.Context(), nd.Driver)
		results := make([]contracttest.CRUDTestResult, 0, samples)
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
			result := contracttest.RunDriverCRUDTest(r.Context(), nd.Name, nd.Driver)
			results = append(results, *result)
		}
		reports = append(reports, contracttest.NewCRUDBenchmarkReportSamplesWithEnvironment(results, contracttest.BenchmarkEnvironment{NetworkProbe: &probe}))
	}
	if req.Mount != "" && !matched {
		http.Error(w, fmt.Sprintf("mount %q not found", req.Mount), http.StatusNotFound)
		return
	}
	if len(reports) == 0 {
		http.Error(w, "no mounts enabled for debug tests; set test_enabled = true in [[mounts]]", http.StatusForbidden)
		return
	}
	writeJSON(w, reports)
}

func (s *Server) writeFSBenchmark(w http.ResponseWriter, r *http.Request, provider diagnostics.DriverProvider, filesys vfs.FileSystem, req contracttest.DriverTestRequest, samples int, interval time.Duration) {
	nd, ok := findDebugTestDriver(provider.Drivers(), req.Mount)
	if !ok {
		http.Error(w, fmt.Sprintf("mount %q not found", req.Mount), http.StatusNotFound)
		return
	}
	if !nd.TestEnabled {
		http.Error(w, debugTestDisabledError(req.Mount), http.StatusForbidden)
		return
	}
	probe := contracttest.RunBenchmarkNetworkProbe(r.Context(), nd.Driver)
	results := make([]contracttest.FSTestResult, 0, samples)
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
		result := contracttest.RunVFSSmokeTest(r.Context(), filesys, req.Mount, contracttest.ParseXferSize(req.Size))
		results = append(results, *result)
	}
	writeJSON(w, []contracttest.BenchmarkReport{contracttest.NewFSBenchmarkReportSamplesWithEnvironment(results, contracttest.BenchmarkEnvironment{NetworkProbe: &probe})})
}

func (s *Server) writeXferBenchmark(w http.ResponseWriter, r *http.Request, provider diagnostics.DriverProvider, req contracttest.DriverTestRequest, samples int, interval time.Duration) {
	drivers := provider.Drivers()
	srcDriver, srcOK := findDebugTestDriver(drivers, req.Source)
	if !srcOK {
		http.Error(w, fmt.Sprintf("source mount %q not found", req.Source), http.StatusNotFound)
		return
	}
	if !srcDriver.TestEnabled {
		http.Error(w, debugTestDisabledError(req.Source), http.StatusForbidden)
		return
	}
	dstDriver, dstOK := findDebugTestDriver(drivers, req.Dest)
	if !dstOK {
		http.Error(w, fmt.Sprintf("dest mount %q not found", req.Dest), http.StatusNotFound)
		return
	}
	if !dstDriver.TestEnabled {
		http.Error(w, debugTestDisabledError(req.Dest), http.StatusForbidden)
		return
	}

	probe := mergeBenchmarkNetworkProbes(
		contracttest.RunBenchmarkNetworkProbe(r.Context(), srcDriver.Driver),
		contracttest.RunBenchmarkNetworkProbe(r.Context(), dstDriver.Driver),
	)
	results := make([]contracttest.XferTestResult, 0, samples)
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
			results = append(results, *contracttest.RunVFSXferTest(r.Context(), filesys, req.Source, req.Dest, contracttest.ParseXferSize(req.Size)))
		} else {
			results = append(results, *contracttest.RunDriverXferTest(r.Context(), req.Source, srcDriver.Driver, req.Dest, dstDriver.Driver, contracttest.ParseXferSize(req.Size)))
		}
	}
	writeJSON(w, []contracttest.BenchmarkReport{contracttest.NewXferBenchmarkReportSamplesWithEnvironment(results, contracttest.BenchmarkEnvironment{NetworkProbe: &probe})})
}

func mergeBenchmarkNetworkProbes(src, dst contracttest.BenchmarkNetworkProbe) contracttest.BenchmarkNetworkProbe {
	probe := contracttest.BenchmarkNetworkProbe{
		Status:          "ok",
		Started:         src.Started,
		Finished:        dst.Finished,
		Steps:           append(append([]contracttest.BenchmarkProbeStep(nil), src.Steps...), dst.Steps...),
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
		probe.DurationMS = contracttest.DurationMillis(duration)
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
	probe.APILatency = contracttest.ComputeStats(contracttest.ProbeStepDurations(probe.Steps))
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
