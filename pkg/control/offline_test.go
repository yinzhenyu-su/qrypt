package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

type offlineServiceStub struct {
	created    task.OperationRequest
	listed     task.Filter
	inspected  task.OfflineOperationRequest
	waitedID   string
	waitedFor  time.Duration
	cancelled  string
	retried    string
	tasks      []task.Task
	preview    OfflinePreviewResult
	createErr  error
	inspectErr error
	waitErr    error
	getErr     error
}

func (s *offlineServiceStub) CreateOfflineTask(_ context.Context, req task.OperationRequest) (task.Task, error) {
	s.created = req
	if s.createErr != nil {
		return task.Task{}, s.createErr
	}
	return task.Task{ID: "task-1", Type: task.TypeOfflineDownload, State: task.StateQueued}, nil
}

func (s *offlineServiceStub) ListOfflineTasks(_ context.Context, filter task.Filter) ([]task.Task, error) {
	s.listed = filter
	return s.tasks, nil
}

func (s *offlineServiceStub) GetOfflineTask(_ context.Context, id string) (task.Task, error) {
	if s.getErr != nil {
		return task.Task{}, s.getErr
	}
	return task.Task{ID: id, Type: task.TypeOfflineFetch, State: task.StateRunning}, nil
}

func (s *offlineServiceStub) InspectOffline(_ context.Context, req task.OfflineOperationRequest) (OfflinePreviewResult, error) {
	s.inspected = req
	if s.inspectErr != nil {
		return OfflinePreviewResult{}, s.inspectErr
	}
	return s.preview, nil
}

func (s *offlineServiceStub) WaitOfflineTask(_ context.Context, id string, timeout time.Duration) (task.Task, error) {
	s.waitedID = id
	s.waitedFor = timeout
	if s.waitErr != nil {
		return task.Task{}, s.waitErr
	}
	return task.Task{ID: id, Type: task.TypeOfflineDownload, State: task.StateSucceeded}, nil
}

func (s *offlineServiceStub) CancelTask(_ context.Context, id string) error {
	s.cancelled = id
	return nil
}

func (s *offlineServiceStub) RetryTask(_ context.Context, id string) error {
	s.retried = id
	return nil
}

func newOfflineServer(service OfflineService) *CommandServer {
	server := &CommandServer{service: &commandServiceStub{}}
	server.SetOfflineService(service)
	return server
}

func TestOfflineCreateRoute(t *testing.T) {
	t.Parallel()

	service := &offlineServiceStub{}
	server := newOfflineServer(service)
	body, _ := json.Marshal(OfflineCreateRequest{Source: "magnet:?xt=urn:btih:abc", ParseTo: "/quark/downloads", Mkdir: true})
	resp := httptest.NewRecorder()
	server.handleOfflineTasks(resp, httptest.NewRequest(http.MethodPost, "/v1/control/offline/tasks", strings.NewReader(string(body))))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if service.created.Operation != task.OperationDownload || service.created.Offline == nil {
		t.Fatalf("created request = %+v", service.created)
	}
	if service.created.Offline.Source != "magnet:?xt=urn:btih:abc" || service.created.Offline.ParseTo != "/quark/downloads" || !service.created.Offline.Mkdir {
		t.Fatalf("offline inputs = %+v", service.created.Offline)
	}
	var created task.Task
	if err := json.Unmarshal(resp.Body.Bytes(), &created); err != nil || created.ID != "task-1" {
		t.Fatalf("created task = %+v err=%v", created, err)
	}
}

func TestOfflineCreateSelectsFetchWithDestination(t *testing.T) {
	t.Parallel()

	service := &offlineServiceStub{}
	server := newOfflineServer(service)
	body, _ := json.Marshal(OfflineCreateRequest{Source: "https://example.com/a.iso", DownloadTo: "/out"})
	resp := httptest.NewRecorder()
	server.handleOfflineTasks(resp, httptest.NewRequest(http.MethodPost, "/v1/control/offline/tasks", strings.NewReader(string(body))))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if service.created.Offline.DownloadTo != "/out" {
		t.Fatalf("download destination dropped: %+v", service.created.Offline)
	}
}

func TestOfflineListRouteDefaultView(t *testing.T) {
	t.Parallel()

	service := &offlineServiceStub{tasks: []task.Task{{ID: "a", Type: task.TypeOfflineDownload}}}
	server := newOfflineServer(service)
	resp := httptest.NewRecorder()
	server.handleOfflineTasks(resp, httptest.NewRequest(http.MethodGet, "/v1/control/offline/tasks", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}
	if service.listed.Limit != offlineListDefaultLimit {
		t.Fatalf("default limit = %d, want %d", service.listed.Limit, offlineListDefaultLimit)
	}
	for _, state := range service.listed.States {
		if state == task.StateSucceeded || state == task.StateCanceled {
			t.Fatalf("default view must exclude %s", state)
		}
	}
	if len(service.listed.Types) != 2 {
		t.Fatalf("list types = %v", service.listed.Types)
	}

	all := httptest.NewRecorder()
	server.handleOfflineTasks(all, httptest.NewRequest(http.MethodGet, "/v1/control/offline/tasks?all=true&limit=5", nil))
	if len(service.listed.States) != 0 {
		t.Fatalf("--all must not set a state filter: %v", service.listed.States)
	}
	if service.listed.Limit != 5 {
		t.Fatalf("limit = %d, want 5", service.listed.Limit)
	}
}

func TestOfflineShowCancelRetryRoutes(t *testing.T) {
	t.Parallel()

	service := &offlineServiceStub{}
	server := newOfflineServer(service)

	show := httptest.NewRecorder()
	server.handleOfflineTask(show, httptest.NewRequest(http.MethodGet, "/v1/control/offline/tasks/task-9", nil))
	if show.Code != http.StatusOK {
		t.Fatalf("show status = %d", show.Code)
	}

	for _, action := range []string{"cancel", "retry"} {
		resp := httptest.NewRecorder()
		server.handleOfflineTask(resp, httptest.NewRequest(http.MethodPost, "/v1/control/offline/tasks/task-9/"+action, nil))
		if resp.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", action, resp.Code, resp.Body.String())
		}
	}
	if service.cancelled != "task-9" || service.retried != "task-9" {
		t.Fatalf("cancel=%q retry=%q", service.cancelled, service.retried)
	}
}

func TestOfflineInspectAndWaitRoutes(t *testing.T) {
	t.Parallel()

	service := &offlineServiceStub{preview: OfflinePreviewResult{Title: "Example", Files: []OfflinePreviewFile{{Name: "a.mkv", Selected: true}}}}
	server := newOfflineServer(service)

	body, _ := json.Marshal(OfflineInspectRequest{Source: "magnet:?xt=urn:btih:abc", ParseTo: "/quark/downloads"})
	inspect := httptest.NewRecorder()
	server.handleOfflineInspect(inspect, httptest.NewRequest(http.MethodPost, "/v1/control/offline/inspect", strings.NewReader(string(body))))
	if inspect.Code != http.StatusOK {
		t.Fatalf("inspect status = %d body=%s", inspect.Code, inspect.Body.String())
	}
	var preview OfflinePreviewResult
	if err := json.Unmarshal(inspect.Body.Bytes(), &preview); err != nil || len(preview.Files) != 1 {
		t.Fatalf("preview = %+v err=%v", preview, err)
	}

	waitBody, _ := json.Marshal(OfflineWaitRequest{ID: "task-3", TimeoutMillis: 1500})
	wait := httptest.NewRecorder()
	server.handleOfflineWait(wait, httptest.NewRequest(http.MethodPost, "/v1/control/offline/wait", strings.NewReader(string(waitBody))))
	if wait.Code != http.StatusOK {
		t.Fatalf("wait status = %d body=%s", wait.Code, wait.Body.String())
	}
	if service.waitedID != "task-3" || service.waitedFor != 1500*time.Millisecond {
		t.Fatalf("wait = %q %s", service.waitedID, service.waitedFor)
	}
}

func TestOfflineServiceClassifiedErrors(t *testing.T) {
	t.Parallel()

	usage := &offlineServiceStub{createErr: &OfflineError{Class: OfflineErrorUsage, Code: "offline_source_required", Message: "source required"}}
	server := newOfflineServer(usage)
	body, _ := json.Marshal(OfflineCreateRequest{Source: ""})
	resp := httptest.NewRecorder()
	server.handleOfflineTasks(resp, httptest.NewRequest(http.MethodPost, "/v1/control/offline/tasks", strings.NewReader(string(body))))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("usage error status = %d, want 400", resp.Code)
	}

	notFound := &offlineServiceStub{getErr: &OfflineError{Class: OfflineErrorNotFound, Message: "nope"}}
	server = newOfflineServer(notFound)
	show := httptest.NewRecorder()
	server.handleOfflineTask(show, httptest.NewRequest(http.MethodGet, "/v1/control/offline/tasks/missing", nil))
	if show.Code != http.StatusNotFound {
		t.Fatalf("not-found status = %d, want 404", show.Code)
	}
}

func TestOfflineRoutesWithoutCore(t *testing.T) {
	t.Parallel()

	server := &CommandServer{service: &commandServiceStub{}}
	for name, call := range map[string]func(http.ResponseWriter, *http.Request){
		"create":  server.handleOfflineTasks,
		"inspect": server.handleOfflineInspect,
		"wait":    server.handleOfflineWait,
	} {
		resp := httptest.NewRecorder()
		call(resp, httptest.NewRequest(http.MethodPost, "/v1/control/offline/"+name, strings.NewReader("{}")))
		if resp.Code != http.StatusNotImplemented {
			t.Fatalf("%s without a Core = %d, want 501", name, resp.Code)
		}
	}
}
