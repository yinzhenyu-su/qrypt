package contracttest

import (
	"context"
	"fmt"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// DriverTestRequest is the JSON body for the /v1/driver/test and
// /v1/driver/benchmark endpoints.
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

// Specs returns the registered test specs (identity, capability
// prerequisites, runner). The debug server schedules mounts through it.
func Specs() map[string]TestSpec {
	return driverTestSpecs
}

// FromXferTestResult converts a transfer test outcome into the unified
// TestRun envelope consumed by the debug API.
func FromXferTestResult(r XferTestResult) TestRun {
	return fromXferTestResult(r)
}

// TestStep is the unified step record every test spec emits. Consumers
// (CLI output, alerting, result storage) can rely on one schema regardless
// of which spec produced the run.
type TestStep struct {
	Operation     string `json:"operation"`
	Name          string `json:"name,omitempty"`
	OK            bool   `json:"ok"`
	Error         string `json:"error,omitempty"`
	ErrorCategory string `json:"error_category,omitempty"`
	Duration      string `json:"duration"`
	DurationMS    int64  `json:"duration_ms"`
}

// TestRun is the unified envelope every debug test returns. It carries the
// scheduling context (spec, mount, capability matrix, skip reason) plus the
// step trace, residual objects, and driver metrics.
type TestRun struct {
	Spec             string                 `json:"spec"`
	Mount            string                 `json:"mount"`
	Driver           string                 `json:"driver,omitempty"`
	Pass             bool                   `json:"pass"`
	Skipped          bool                   `json:"skipped,omitempty"`
	SkipReason       string                 `json:"skip_reason,omitempty"`
	Error            string                 `json:"error,omitempty"`
	ErrorCategory    string                 `json:"error_category,omitempty"`
	Capabilities     []drive.Capability     `json:"capabilities,omitempty"`
	Requires         []drive.Capability     `json:"requires,omitempty"`
	Steps            []TestStep             `json:"steps"`
	Residual         []CRUDTestArtifact     `json:"residual,omitempty"`
	ResidualTimeline []CRUDVisibilitySample `json:"residual_timeline,omitempty"`
	Metrics          []drive.MetricEvent    `json:"metrics,omitempty"`
	RetryCommand     string                 `json:"retry_command,omitempty"`
	Started          time.Time              `json:"started_at"`
	Finished         time.Time              `json:"finished_at"`
	Duration         string                 `json:"duration"`
	DurationMS       int64                  `json:"duration_ms"`
}

// TestEnv carries the objects a spec runner may need, resolved once by the
// scheduler per request. Driver-layer specs use Driver; VFS-layer specs
// (fs, resume) use FileSys.
type TestEnv struct {
	Ctx     context.Context
	Req     DriverTestRequest
	FileSys vfs.FileSystem
}

// TestSpec describes one debug test: its identity, the driver capabilities
// it requires (the scheduler filters mounts by these before running), and a
// runner that produces a unified TestRun.
type TestSpec struct {
	Name        string
	Requires    []drive.Capability
	RequiresVFS bool // runs against the VFS layer instead of the raw driver
	Run         func(env TestEnv, mount string, d drive.Driver) TestRun
}

// driverTestSpecs registers every single-mount test spec. Adding a new test
// is one entry here; the scheduler handles traversal, capability filtering,
// test_enabled checks, VFS preconditions, and result aggregation. The xfer
// spec is the one exception: it drives two mounts and is scheduled
// separately in handleDriverTest.
var driverTestSpecs = map[string]TestSpec{
	"crud": {
		Name:     "crud",
		Requires: []drive.Capability{drive.CapabilityWriter, drive.CapabilitySourceUploader},
		Run: func(env TestEnv, mount string, d drive.Driver) TestRun {
			return fromCRUDTestResult("crud", *RunDriverCRUDTest(env.Ctx, mount, d))
		},
	},
	"multipart": {
		Name:     "multipart",
		Requires: []drive.Capability{drive.CapabilityWriter, drive.CapabilitySourceUploader},
		Run: func(env TestEnv, mount string, d drive.Driver) TestRun {
			return fromCRUDTestResult("multipart", *RunDriverMultipartTest(env.Ctx, mount, d, ParseXferSize(env.Req.Size)))
		},
	},
	"instantupload": {
		Name:     "instantupload",
		Requires: []drive.Capability{drive.CapabilityWriter, drive.CapabilitySourceUploader},
		Run: func(env TestEnv, mount string, d drive.Driver) TestRun {
			return fromCRUDTestResult("instantupload", *RunDriverInstantUploadTest(env.Ctx, mount, d))
		},
	},
	"auth": {
		Name: "auth",
		Run: func(env TestEnv, mount string, d drive.Driver) TestRun {
			return fromAuthTestResult(*RunDriverAuthTest(env.Ctx, mount, d))
		},
	},
	"fs": {
		Name:        "fs",
		RequiresVFS: true,
		Run: func(env TestEnv, mount string, d drive.Driver) TestRun {
			return fromFSTestResult("fs", *RunVFSSmokeTest(env.Ctx, env.FileSys, mount, ParseXferSize(env.Req.Size)))
		},
	},
	"contract": {
		Name:     "contract",
		Requires: []drive.Capability{drive.CapabilityWriter, drive.CapabilitySourceUploader},
		Run: func(env TestEnv, mount string, d drive.Driver) TestRun {
			return runContractChecks(env.Ctx, mount, d)
		},
	},
	"resume": {
		Name:        "resume",
		RequiresVFS: true,
		Run: func(env TestEnv, mount string, d drive.Driver) TestRun {
			return fromResumeTestResult(*RunVFSResumeTest(env.Ctx, env.FileSys, mount, ParseXferSize(env.Req.Size)))
		},
	},
}

// missingCapabilities returns the driver capabilities required by a spec
// that the driver does not provide.
func MissingCapabilities(d drive.Driver, requires []drive.Capability) []drive.Capability {
	var missing []drive.Capability
	for _, capability := range requires {
		if !drive.HasCapability(d, capability) {
			missing = append(missing, capability)
		}
	}
	return missing
}

func fromCRUDTestResult(spec string, r CRUDTestResult) TestRun {
	tr := TestRun{
		Spec:             spec,
		Mount:            r.Mount,
		Driver:           r.Driver,
		Pass:             r.Pass,
		Residual:         r.Residual,
		ResidualTimeline: r.ResidualTimeline,
		Metrics:          r.Metrics,
		RetryCommand:     r.RetryCommand,
		Started:          r.Started,
		Finished:         r.Finished,
		Duration:         r.Duration,
		DurationMS:       r.DurationMS,
	}
	tr.Steps = make([]TestStep, len(r.Steps))
	for i, s := range r.Steps {
		tr.Steps[i] = TestStep{
			Operation: s.Operation, Name: s.Name, OK: s.OK,
			Error: s.Error, ErrorCategory: s.ErrorCategory,
			Duration: s.Duration, DurationMS: s.DurationMS,
		}
	}
	return tr
}

func fromAuthTestResult(r AuthTestResult) TestRun {
	tr := TestRun{
		Spec:         "auth",
		Mount:        r.Mount,
		Driver:       r.Driver,
		Pass:         r.Pass,
		Capabilities: r.Capabilities,
		RetryCommand: r.RetryCommand,
		Started:      r.Started,
		Finished:     r.Finished,
		Duration:     r.Duration,
		DurationMS:   r.DurationMS,
	}
	if !r.Pass {
		tr.Error = fmt.Sprintf("auth failed: status=%s", r.AuthStatus)
	}
	tr.Steps = make([]TestStep, len(r.Steps))
	for i, s := range r.Steps {
		tr.Steps[i] = TestStep{
			Operation: s.Operation, OK: s.OK,
			Error: s.Error, ErrorCategory: s.ErrorCategory,
			Duration: s.Duration, DurationMS: s.DurationMS,
		}
	}
	return tr
}

func fromFSTestResult(spec string, r FSTestResult) TestRun {
	tr := TestRun{
		Spec:         spec,
		Mount:        r.Mount,
		Pass:         r.Pass,
		RetryCommand: r.RetryCommand,
		Started:      r.Started,
		Finished:     r.Finished,
		Duration:     r.Duration,
		DurationMS:   r.DurationMS,
	}
	tr.Steps = make([]TestStep, len(r.Steps))
	for i, s := range r.Steps {
		tr.Steps[i] = TestStep{
			Operation: s.Operation, OK: s.OK,
			Error: s.Error, ErrorCategory: s.ErrorCategory,
			Duration: s.Duration, DurationMS: s.DurationMS,
		}
	}
	return tr
}

func fromXferTestResult(r XferTestResult) TestRun {
	tr := TestRun{
		Spec:       "xfer",
		Mount:      r.SourceMount,
		Pass:       r.Pass,
		Started:    r.Started,
		Finished:   r.Finished,
		Duration:   r.Finished.Sub(r.Started).String(),
		DurationMS: DurationMillis(r.Finished.Sub(r.Started)),
	}
	tr.Steps = make([]TestStep, len(r.Steps))
	for i, s := range r.Steps {
		tr.Steps[i] = TestStep{
			Operation: s.Phase, OK: s.OK,
			Error: s.Error, ErrorCategory: s.ErrorCategory,
			Duration: s.Duration, DurationMS: s.DurationMS,
		}
	}
	return tr
}

func fromResumeTestResult(r ResumeTestResult) TestRun {
	tr := TestRun{
		Spec:         "resume",
		Mount:        r.Mount,
		Pass:         r.Pass,
		RetryCommand: r.RetryCommand,
		Started:      r.Started,
		Finished:     r.Finished,
		Duration:     r.Duration,
		DurationMS:   r.DurationMS,
	}
	tr.Steps = make([]TestStep, len(r.Steps))
	for i, s := range r.Steps {
		tr.Steps[i] = TestStep{
			Operation: s.Operation, OK: s.OK,
			Error: s.Error, ErrorCategory: s.ErrorCategory,
			Duration: s.Duration, DurationMS: s.DurationMS,
		}
	}
	return tr
}
