package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	shareapi "github.com/yinzhenyu/qrypt/pkg/share"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

type shareServiceStub struct {
	createdLink  shareapi.LinkCreateRequest
	listedLinks  shareapi.LinkListRequest
	updatedLink  shareapi.LinkUpdateRequest
	gotMount     string
	gotID        string
	removedID    string
	inspected    shareapi.InspectRequest
	resolved     shareapi.ResolveRequest
	resolveValue shareapi.ResolveResult
	createdTask  task.OperationRequest
	listedTasks  task.Filter
	waitedID     string
	waitedFor    time.Duration
	cancelled    string
	retried      string
	linkErr      error
	taskErr      error
	listTasks    []task.Task
	inspectValue shareapi.InspectResult
}

func (s *shareServiceStub) CreateShareLink(_ context.Context, req shareapi.LinkCreateRequest) (drive.Share, error) {
	s.createdLink = req
	if s.linkErr != nil {
		return drive.Share{}, s.linkErr
	}
	return drive.Share{LinkID: "link00000001", URL: "https://x/s/link00000001", Passcode: req.Passcode}, nil
}

func (s *shareServiceStub) ListShareLinks(_ context.Context, req shareapi.LinkListRequest) (drive.SharePage, error) {
	s.listedLinks = req
	if s.linkErr != nil {
		return drive.SharePage{}, s.linkErr
	}
	return drive.SharePage{Shares: []drive.Share{{LinkID: "link00000001"}}, Next: "2"}, nil
}

func (s *shareServiceStub) GetShareLink(_ context.Context, mount, id string) (drive.Share, error) {
	s.gotMount, s.gotID = mount, id
	if s.linkErr != nil {
		return drive.Share{}, s.linkErr
	}
	return drive.Share{LinkID: id, NativeID: "native1"}, nil
}

func (s *shareServiceStub) UpdateShareLink(_ context.Context, req shareapi.LinkUpdateRequest) (drive.Share, error) {
	s.updatedLink = req
	if s.linkErr != nil {
		return drive.Share{}, s.linkErr
	}
	return drive.Share{LinkID: req.ID}, nil
}

func (s *shareServiceStub) RemoveShareLink(_ context.Context, mount, id string) error {
	s.gotMount, s.removedID = mount, id
	return s.linkErr
}

func (s *shareServiceStub) ResolveShareLink(_ context.Context, req shareapi.ResolveRequest) (shareapi.ResolveResult, error) {
	s.resolved = req
	if s.linkErr != nil {
		return shareapi.ResolveResult{}, s.linkErr
	}
	return s.resolveValue, nil
}

func (s *shareServiceStub) InspectShareLink(_ context.Context, req shareapi.InspectRequest) (shareapi.InspectResult, error) {
	s.inspected = req
	if s.linkErr != nil {
		return shareapi.InspectResult{}, s.linkErr
	}
	return s.inspectValue, nil
}

func (s *shareServiceStub) CreateShareTask(_ context.Context, req task.OperationRequest) (task.Task, error) {
	s.createdTask = req
	if s.taskErr != nil {
		return task.Task{}, s.taskErr
	}
	return task.Task{ID: "share-1", Type: task.TypeShareSave, State: task.StateQueued}, nil
}

func (s *shareServiceStub) ListShareTasks(_ context.Context, filter task.Filter) ([]task.Task, error) {
	s.listedTasks = filter
	return s.listTasks, nil
}

func (s *shareServiceStub) GetShareTask(_ context.Context, id string) (task.Task, error) {
	s.gotID = id
	if s.taskErr != nil {
		return task.Task{}, s.taskErr
	}
	return task.Task{ID: id, Type: task.TypeShareFetch, State: task.StateRunning}, nil
}

func (s *shareServiceStub) WaitShareTask(_ context.Context, id string, timeout time.Duration) (task.Task, error) {
	s.waitedID, s.waitedFor = id, timeout
	if s.taskErr != nil {
		return task.Task{}, s.taskErr
	}
	return task.Task{ID: id, Type: task.TypeShareSave, State: task.StateSucceeded}, nil
}

func (s *shareServiceStub) CancelTask(_ context.Context, id string) error {
	s.cancelled = id
	return nil
}

func (s *shareServiceStub) RetryTask(_ context.Context, id string) error {
	s.retried = id
	return nil
}

func newShareServer(service shareapi.Service) *CommandServer {
	server := &CommandServer{service: &commandServiceStub{}}
	server.SetShareService(service)
	return server
}

func TestShareLinkRoutes(t *testing.T) {
	t.Parallel()

	service := &shareServiceStub{}
	server := newShareServer(service)

	body := `{"paths":["/quark/a.bin"],"title":"t","expiry":604800000000000,"passcode":"ab12","visit_limit":3}`
	resp := httptest.NewRecorder()
	server.handleShareLinks(resp, httptest.NewRequest(http.MethodPost, "/v1/control/share/links", strings.NewReader(body)))
	if resp.Code != http.StatusOK {
		t.Fatalf("create status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(service.createdLink.Paths) != 1 || service.createdLink.Paths[0] != "/quark/a.bin" {
		t.Fatalf("created request = %+v", service.createdLink)
	}
	if service.createdLink.Expiry != drive.ShareExpiryWeek || service.createdLink.Passcode != "ab12" || service.createdLink.VisitLimit != 3 {
		t.Fatalf("created options = %+v", service.createdLink)
	}

	resp = httptest.NewRecorder()
	server.handleShareLinks(resp, httptest.NewRequest(http.MethodGet, "/v1/control/share/links?mount=quark&all=true&limit=5&cursor=2", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", resp.Code, resp.Body.String())
	}
	if service.listedLinks.Mount != "quark" || !service.listedLinks.IncludeExpired || service.listedLinks.Limit != 5 || service.listedLinks.Cursor != "2" {
		t.Fatalf("list request = %+v", service.listedLinks)
	}
	var listed shareapi.LinkList
	if err := json.Unmarshal(resp.Body.Bytes(), &listed); err != nil || len(listed.Shares) != 1 || listed.Next != "2" {
		t.Fatalf("listed = %+v err=%v", listed, err)
	}

	resp = httptest.NewRecorder()
	server.handleShareLink(resp, httptest.NewRequest(http.MethodGet, "/v1/control/share/links/link00000001?mount=quark", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("get status = %d body = %s", resp.Code, resp.Body.String())
	}
	if service.gotMount != "quark" || service.gotID != "link00000001" {
		t.Fatalf("get = mount %q id %q", service.gotMount, service.gotID)
	}

	title := "renamed"
	update, _ := json.Marshal(shareapi.LinkUpdateRequest{Title: &title})
	resp = httptest.NewRecorder()
	server.handleShareLink(resp, httptest.NewRequest(http.MethodPatch, "/v1/control/share/links/link00000001", strings.NewReader(string(update))))
	if resp.Code != http.StatusOK {
		t.Fatalf("patch status = %d body = %s", resp.Code, resp.Body.String())
	}
	if service.updatedLink.ID != "link00000001" || service.updatedLink.Title == nil || *service.updatedLink.Title != "renamed" {
		t.Fatalf("update request = %+v", service.updatedLink)
	}

	resp = httptest.NewRecorder()
	server.handleShareLink(resp, httptest.NewRequest(http.MethodDelete, "/v1/control/share/links/link00000001?mount=quark", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("delete status = %d body = %s", resp.Code, resp.Body.String())
	}
	if service.removedID != "link00000001" || service.gotMount != "quark" {
		t.Fatalf("removed = %+v", service)
	}
}

func TestShareReceiveRoutes(t *testing.T) {
	t.Parallel()

	service := &shareServiceStub{}
	server := newShareServer(service)

	save, _ := json.Marshal(shareapi.ReceiveRequest{Link: "https://x/s/abc", To: "/quark/inbox"})
	resp := httptest.NewRecorder()
	server.handleShareTasks(resp, httptest.NewRequest(http.MethodPost, "/v1/control/share/tasks", strings.NewReader(string(save))))
	if resp.Code != http.StatusOK {
		t.Fatalf("save status = %d body = %s", resp.Code, resp.Body.String())
	}
	if service.createdTask.Operation != task.OperationShare || service.createdTask.Share == nil {
		t.Fatalf("save operation = %+v", service.createdTask)
	}
	if service.createdTask.Share.Link != "https://x/s/abc" || service.createdTask.Share.To != "/quark/inbox" {
		t.Fatalf("save inputs = %+v", service.createdTask.Share)
	}

	fetch, _ := json.Marshal(shareapi.ReceiveRequest{Link: "https://x/s/abc", DownloadTo: "./out", Selection: task.OfflineSelection{Mode: task.OfflineSelectionLargest, Largest: 2}})
	resp = httptest.NewRecorder()
	server.handleShareTasks(resp, httptest.NewRequest(http.MethodPost, "/v1/control/share/tasks", strings.NewReader(string(fetch))))
	if resp.Code != http.StatusOK {
		t.Fatalf("fetch status = %d body = %s", resp.Code, resp.Body.String())
	}
	if service.createdTask.Operation != task.OperationDownload || service.createdTask.Share.DownloadTo != "./out" {
		t.Fatalf("fetch operation = %+v", service.createdTask)
	}

	resp = httptest.NewRecorder()
	server.handleShareTasks(resp, httptest.NewRequest(http.MethodGet, "/v1/control/share/tasks?mount=quark&limit=7", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", resp.Code, resp.Body.String())
	}
	if service.listedTasks.Mount != "quark" || service.listedTasks.Limit != 7 || len(service.listedTasks.States) != 7 {
		t.Fatalf("list filter = %+v", service.listedTasks)
	}

	resp = httptest.NewRecorder()
	server.handleShareTask(resp, httptest.NewRequest(http.MethodPost, "/v1/control/share/tasks/share-1/cancel", nil))
	if resp.Code != http.StatusOK || service.cancelled != "share-1" {
		t.Fatalf("cancel status = %d service = %+v", resp.Code, service)
	}
	resp = httptest.NewRecorder()
	server.handleShareTask(resp, httptest.NewRequest(http.MethodPost, "/v1/control/share/tasks/share-1/retry", nil))
	if resp.Code != http.StatusOK || service.retried != "share-1" {
		t.Fatalf("retry status = %d service = %+v", resp.Code, service)
	}

	wait, _ := json.Marshal(shareapi.WaitRequest{ID: "share-1", TimeoutMillis: 1500})
	resp = httptest.NewRecorder()
	server.handleShareWait(resp, httptest.NewRequest(http.MethodPost, "/v1/control/share/wait", strings.NewReader(string(wait))))
	if resp.Code != http.StatusOK {
		t.Fatalf("wait status = %d body = %s", resp.Code, resp.Body.String())
	}
	if service.waitedID != "share-1" || service.waitedFor != 1500*time.Millisecond {
		t.Fatalf("wait = %q %s", service.waitedID, service.waitedFor)
	}
}

func TestShareResolveRoute(t *testing.T) {
	t.Parallel()

	service := &shareServiceStub{resolveValue: shareapi.ResolveResult{
		Link:       drive.ShareLink{URL: "u", LinkID: "abc"},
		Candidates: []string{"alpha", "beta"},
	}}
	server := newShareServer(service)
	body := `{"link":"https://x/s/abc"}`
	resp := httptest.NewRecorder()
	server.handleShareResolve(resp, httptest.NewRequest(http.MethodPost, "/v1/control/share/resolve", strings.NewReader(body)))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if service.resolved.Link != "https://x/s/abc" {
		t.Fatalf("resolve request = %+v", service.resolved)
	}
	var result shareapi.ResolveResult
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil || len(result.Candidates) != 2 {
		t.Fatalf("result = %+v err=%v", result, err)
	}
}

func TestShareInspectRoute(t *testing.T) {
	t.Parallel()

	service := &shareServiceStub{inspectValue: shareapi.InspectResult{Link: drive.ShareLink{URL: "u", LinkID: "abc"}}}
	server := newShareServer(service)
	body := `{"link":"https://x/s/abc","passcode":"pw","dir":"42"}`
	resp := httptest.NewRecorder()
	server.handleShareInspect(resp, httptest.NewRequest(http.MethodPost, "/v1/control/share/inspect", strings.NewReader(body)))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if service.inspected.Link != "https://x/s/abc" || service.inspected.Passcode != "pw" || service.inspected.Dir != "42" {
		t.Fatalf("inspect request = %+v", service.inspected)
	}
}

func TestShareRouteErrorsAreClassified(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"usage", drive.NewShareError(drive.ShareErrUnsupported, "", "no"), http.StatusBadRequest},
		{"not found", drive.NewShareError(drive.ShareErrNotFound, "", "gone"), http.StatusNotFound},
		{"runtime", drive.NewShareError(drive.ShareErrProvider, "", "boom"), http.StatusConflict},
		{"request usage", &shareapi.RequestError{Class: shareapi.ErrorUsage, Message: "bad input"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := newShareServer(&shareServiceStub{linkErr: tc.err})
			resp := httptest.NewRecorder()
			server.handleShareLinks(resp, httptest.NewRequest(http.MethodGet, "/v1/control/share/links?mount=quark", nil))
			if resp.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", resp.Code, tc.status, resp.Body.String())
			}
		})
	}
}

func TestShareRoutesWithoutService(t *testing.T) {
	t.Parallel()

	server := &CommandServer{service: &commandServiceStub{}}
	resp := httptest.NewRecorder()
	server.handleShareLinks(resp, httptest.NewRequest(http.MethodGet, "/v1/control/share/links", nil))
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.Code)
	}
	if !strings.Contains(resp.Body.String(), "no running Core") {
		t.Fatalf("body = %q", resp.Body.String())
	}
}

func TestShareListRejectsBadLimit(t *testing.T) {
	t.Parallel()

	server := newShareServer(&shareServiceStub{})
	for _, path := range []string{"/v1/control/share/links?limit=0", "/v1/control/share/tasks?limit=-2"} {
		handler := server.handleShareLinks
		if strings.Contains(path, "/tasks") {
			handler = server.handleShareTasks
		}
		resp := httptest.NewRecorder()
		handler(resp, httptest.NewRequest(http.MethodGet, path, nil))
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", path, resp.Code)
		}
	}
}

func TestShareErrorClassOfWraps(t *testing.T) {
	t.Parallel()

	cause := errors.New("cause")
	classified := &shareapi.RequestError{Class: shareapi.ErrorNotFound, Message: "missing", Err: cause}
	if got := shareapi.ErrorClassOf(classified); got != shareapi.ErrorNotFound {
		t.Fatalf("class = %q", got)
	}
	if !errors.Is(classified, cause) {
		t.Fatal("classified error does not unwrap")
	}
	if got := shareapi.ErrorClassOf(errors.New("other")); got != shareapi.ErrorRuntime {
		t.Fatalf("default class = %q", got)
	}
}
