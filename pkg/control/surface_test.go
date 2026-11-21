package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

func decodeEnvelope(t *testing.T, resp *httptest.ResponseRecorder) ErrorEnvelope {
	t.Helper()
	var envelope ErrorEnvelope
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v (body %q)", err, resp.Body.String())
	}
	return envelope
}

func TestErrorEnvelopeShapeAndCodes(t *testing.T) {
	cases := []struct {
		name      string
		write     func(http.ResponseWriter)
		status    int
		code      ErrorCode
		retryable bool
	}{
		{
			name:   "bad request",
			write:  func(w http.ResponseWriter) { writeBadRequest(w, "path required") },
			status: http.StatusBadRequest,
			code:   CodeInvalid,
		},
		{
			name:   "method not allowed",
			write:  writeMethodNotAllowed,
			status: http.StatusMethodNotAllowed,
			code:   CodeInvalid,
		},
		{
			name:      "unavailable is retryable",
			write:     func(w http.ResponseWriter) { writeUnavailable(w, "mount service unavailable") },
			status:    http.StatusServiceUnavailable,
			code:      CodeUnavailable,
			retryable: true,
		},
		{
			name:      "timeout is retryable",
			write:     func(w http.ResponseWriter) { writeTimeout(w, "wait timed out") },
			status:    http.StatusGatewayTimeout,
			code:      CodeTimeout,
			retryable: true,
		},
		{
			name:   "not found",
			write:  func(w http.ResponseWriter) { writeNotFound(w, "task not found") },
			status: http.StatusNotFound,
			code:   CodeNotFound,
		},
		{
			name:   "explicit status",
			write:  func(w http.ResponseWriter) { writeStatus(w, http.StatusConflict, "busy") },
			status: http.StatusConflict,
			code:   CodeConflict,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := httptest.NewRecorder()
			tc.write(resp)
			if resp.Code != tc.status {
				t.Fatalf("status = %d, want %d", resp.Code, tc.status)
			}
			if got := resp.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Fatalf("content type = %q", got)
			}
			envelope := decodeEnvelope(t, resp)
			if envelope.Error.Code != tc.code {
				t.Fatalf("code = %q, want %q", envelope.Error.Code, tc.code)
			}
			if envelope.Error.Message == "" {
				t.Fatal("expected a message")
			}
			if envelope.Error.Retryable != tc.retryable {
				t.Fatalf("retryable = %t, want %t", envelope.Error.Retryable, tc.retryable)
			}
		})
	}
}

func TestFSErrorClassification(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		status    int
		code      ErrorCode
		retryable bool
	}{
		{name: "cross mount is unsupported", err: vfs.ErrCrossMount, status: http.StatusConflict, code: CodeUnsupported},
		{name: "missing path", err: errors.New("vfs: stat /boom: not found"), status: http.StatusNotFound, code: CodeNotFound},
		{name: "deadline", err: context.DeadlineExceeded, status: http.StatusGatewayTimeout, code: CodeTimeout, retryable: true},
		{name: "permission", err: errors.New("permission denied"), status: http.StatusForbidden, code: CodeForbidden},
		{name: "read only", err: errors.New("read-only filesystem"), status: http.StatusForbidden, code: CodeForbidden},
		{name: "unknown is a conflict", err: errors.New("unexpected"), status: http.StatusConflict, code: CodeConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := httptest.NewRecorder()
			writeFSError(resp, tc.err)
			if resp.Code != tc.status {
				t.Fatalf("status = %d, want %d", resp.Code, tc.status)
			}
			envelope := decodeEnvelope(t, resp)
			if envelope.Error.Code != tc.code {
				t.Fatalf("code = %q, want %q", envelope.Error.Code, tc.code)
			}
			if envelope.Error.Retryable != tc.retryable {
				t.Fatalf("retryable = %t, want %t", envelope.Error.Retryable, tc.retryable)
			}
		})
	}
}

func TestFSErrorPathAddsDetails(t *testing.T) {
	resp := httptest.NewRecorder()
	writeFSErrorPath(resp, errors.New("not found"), "/m/a.txt")
	envelope := decodeEnvelope(t, resp)
	if envelope.Error.Details["path"] != "/m/a.txt" {
		t.Fatalf("details = %#v", envelope.Error.Details)
	}
}

func TestTransportErrorDecodesEnvelope(t *testing.T) {
	body := []byte(`{"error":{"code":"unsupported","message":"vfs: cross-mount rename","retryable":false,"details":{"path":"/m/x"}}}`)
	transport := newTransportError("/v1/fs/rename", http.StatusConflict, body)
	if transport.Status != http.StatusConflict {
		t.Fatalf("status = %d", transport.Status)
	}
	if transport.Code != CodeUnsupported {
		t.Fatalf("code = %q", transport.Code)
	}
	if transport.Body != "vfs: cross-mount rename" {
		t.Fatalf("body = %q", transport.Body)
	}
	if transport.Details["path"] != "/m/x" {
		t.Fatalf("details = %#v", transport.Details)
	}
	if transport.Retryable {
		t.Fatal("expected non-retryable")
	}
}

func TestTransportErrorKeepsPlainTextBody(t *testing.T) {
	transport := newTransportError("/v1/fs/list", http.StatusNotFound, []byte("404 page not found\n"))
	if transport.Code != "" {
		t.Fatalf("code = %q", transport.Code)
	}
	if transport.Body != "404 page not found" {
		t.Fatalf("body = %q", transport.Body)
	}
}

type journalStub struct {
	tasks       []task.Task
	items       map[string][]task.ItemTracking
	persistence error
	err         error
}

func (s *journalStub) ListTasks(_ context.Context, filter task.Filter) ([]task.Task, error) {
	if s.err != nil {
		return nil, s.err
	}
	var out []task.Task
	for _, item := range s.tasks {
		if filter.ID != "" && item.ID != filter.ID {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *journalStub) ListTaskItems(_ context.Context, id string, _ task.ItemFilter) ([]task.ItemTracking, error) {
	return s.items[id], nil
}

func (s *journalStub) TaskPersistenceError() error { return s.persistence }

type healthSourceStub struct {
	mounts []diagnostics.MountHealth
	err    error
}

func (s healthSourceStub) MountHealth(_ context.Context, mountName string) ([]diagnostics.MountHealth, error) {
	if s.err != nil {
		return nil, s.err
	}
	if mountName == "" {
		return s.mounts, nil
	}
	for _, mount := range s.mounts {
		if mount.Mount == mountName {
			return []diagnostics.MountHealth{mount}, nil
		}
	}
	return nil, nil
}

type catalogStub struct {
	mounts []CapabilityMount
	err    error
}

func (s catalogStub) Mounts(context.Context) ([]CapabilityMount, error) { return s.mounts, s.err }

func TestCapabilitiesReportFeaturesAndMounts(t *testing.T) {
	server := &CommandServer{
		service:     &commandServiceStub{},
		journal:     &journalStub{},
		catalog:     catalogStub{mounts: []CapabilityMount{{Name: "one", Path: "/one"}, {Name: "bad", State: "failed"}}},
		mounts:      &mountServiceStub{},
		mountHealth: healthSourceStub{},
		runtime:     &runtimeServiceStub{status: RuntimeStatus{PID: 7, Version: "0.7.2", ProtocolVersion: ProtocolVersion}},
	}
	resp := httptest.NewRecorder()
	server.handleCapabilities(resp, httptest.NewRequest(http.MethodGet, "/v1/control/capabilities", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var capabilities CapabilitiesResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities.ProtocolVersion != ProtocolVersion {
		t.Fatalf("protocol version = %d", capabilities.ProtocolVersion)
	}
	if capabilities.Version != "0.7.2" {
		t.Fatalf("version = %q", capabilities.Version)
	}
	if capabilities.WriteBytes != MaxWriteBytes {
		t.Fatalf("write bytes = %d", capabilities.WriteBytes)
	}
	features := map[string]bool{}
	for _, feature := range capabilities.Features {
		features[feature] = true
	}
	for _, want := range []string{"fs.read", "fs.write", "runtime.status", "runtime.shutdown", "tasks.control", "tasks.list", "mounts.attach", "health.mounts"} {
		if !features[want] {
			t.Fatalf("missing feature %q in %v", want, capabilities.Features)
		}
	}
	if len(capabilities.Mounts) != 2 || capabilities.Mounts[1].State != "failed" {
		t.Fatalf("mounts = %#v", capabilities.Mounts)
	}
}

func TestCapabilitiesRequiresGet(t *testing.T) {
	server := &CommandServer{}
	resp := httptest.NewRecorder()
	server.handleCapabilities(resp, httptest.NewRequest(http.MethodPost, "/v1/control/capabilities", nil))
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", resp.Code)
	}
	if decodeEnvelope(t, resp).Error.Code != CodeInvalid {
		t.Fatal("expected invalid code")
	}
}

func TestHealthReportsOK(t *testing.T) {
	server := &CommandServer{
		runtime:     &runtimeServiceStub{status: RuntimeStatus{PID: 7, ProtocolVersion: ProtocolVersion}},
		mountHealth: healthSourceStub{mounts: []diagnostics.MountHealth{{Mount: "one", OK: true, CheckedAt: time.Now()}}},
		journal:     &journalStub{},
		mounts:      &mountServiceStub{},
	}
	resp := httptest.NewRecorder()
	server.handleHealth(resp, httptest.NewRequest(http.MethodGet, "/v1/control/health", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var health RuntimeHealth
	if err := json.Unmarshal(resp.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.Status != "ok" {
		t.Fatalf("status = %q", health.Status)
	}
	if health.Runtime == nil || health.Runtime.PID != 7 {
		t.Fatalf("runtime = %#v", health.Runtime)
	}
	if !health.Tasks.Available || len(health.Mounts) != 1 {
		t.Fatalf("health = %#v", health)
	}
}

func TestHealthReportsDegraded(t *testing.T) {
	server := &CommandServer{
		runtime:     &runtimeServiceStub{status: RuntimeStatus{PID: 7}},
		mountHealth: healthSourceStub{mounts: []diagnostics.MountHealth{{Mount: "one", OK: false, Error: "boom"}}},
		journal:     &journalStub{persistence: errors.New("journal corrupt")},
	}
	resp := httptest.NewRecorder()
	server.handleHealth(resp, httptest.NewRequest(http.MethodGet, "/v1/control/health", nil))
	var health RuntimeHealth
	if err := json.Unmarshal(resp.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.Status != "degraded" {
		t.Fatalf("status = %q", health.Status)
	}
	if health.Tasks.PersistenceError != "journal corrupt" {
		t.Fatalf("persistence error = %q", health.Tasks.PersistenceError)
	}
}

func TestHealthWithoutMountSourceIsDegraded(t *testing.T) {
	server := &CommandServer{}
	resp := httptest.NewRecorder()
	server.handleHealth(resp, httptest.NewRequest(http.MethodGet, "/v1/control/health", nil))
	var health RuntimeHealth
	if err := json.Unmarshal(resp.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.Status != "degraded" {
		t.Fatalf("status = %q", health.Status)
	}
	if len(health.Mounts) != 0 || health.Mounts == nil {
		t.Fatalf("mounts must be an empty array, got %#v", health.Mounts)
	}
}

func TestTasksListAndDetail(t *testing.T) {
	journal := &journalStub{
		tasks: []task.Task{{ID: "t1", Type: task.TypeCopy}, {ID: "t2", Type: task.TypeDeleteBatch}},
		items: map[string][]task.ItemTracking{"t1": {{Path: "/m/a"}}},
	}
	server := &CommandServer{journal: journal}

	resp := httptest.NewRecorder()
	server.handleTasks(resp, httptest.NewRequest(http.MethodGet, "/v1/control/tasks?limit=10", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var list TaskListResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Tasks) != 2 {
		t.Fatalf("tasks = %#v", list.Tasks)
	}

	resp = httptest.NewRecorder()
	server.handleTaskByID(resp, httptest.NewRequest(http.MethodGet, "/v1/control/tasks/t1", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var detail TaskDetailResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Task.ID != "t1" || len(detail.Items) != 1 {
		t.Fatalf("detail = %#v", detail)
	}

	resp = httptest.NewRecorder()
	server.handleTaskByID(resp, httptest.NewRequest(http.MethodGet, "/v1/control/tasks/missing", nil))
	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if decodeEnvelope(t, resp).Error.Code != CodeNotFound {
		t.Fatal("expected not_found code")
	}
}

func TestTasksWithoutJournalIsUnsupported(t *testing.T) {
	server := &CommandServer{}
	resp := httptest.NewRecorder()
	server.handleTasks(resp, httptest.NewRequest(http.MethodGet, "/v1/control/tasks", nil))
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d", resp.Code)
	}
	if decodeEnvelope(t, resp).Error.Code != CodeUnsupported {
		t.Fatal("expected unsupported code")
	}
}

func TestTasksRejectInvalidLimit(t *testing.T) {
	server := &CommandServer{journal: &journalStub{}}
	resp := httptest.NewRecorder()
	server.handleTasks(resp, httptest.NewRequest(http.MethodGet, "/v1/control/tasks?limit=0", nil))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
}
