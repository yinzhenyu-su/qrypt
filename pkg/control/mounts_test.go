package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type mountServiceStub struct {
	mounts      []MountStatus
	attachErr   error
	detachErr   error
	attachCalls int
	detachIDs   []string
}

func (s *mountServiceStub) ListMounts(context.Context) ([]MountStatus, error) {
	return s.mounts, nil
}

func (s *mountServiceStub) AttachMount(_ context.Context, req MountAttachRequest) (MountStatus, error) {
	s.attachCalls++
	if s.attachErr != nil {
		return MountStatus{}, s.attachErr
	}
	return MountStatus{ID: "id-1", Name: req.Name, MountPoint: req.MountPoint, ReadOnly: req.ReadOnly, StartedAt: time.Unix(100, 0).UTC()}, nil
}

func (s *mountServiceStub) DetachMount(_ context.Context, id string) error {
	s.detachIDs = append(s.detachIDs, id)
	return s.detachErr
}

func TestCommandServerMountsList(t *testing.T) {
	server := &CommandServer{mounts: &mountServiceStub{mounts: []MountStatus{{ID: "a", MountPoint: "/mnt/a"}}}}
	resp := httptest.NewRecorder()
	server.handleMounts(resp, httptest.NewRequest(http.MethodGet, "/v1/control/mounts", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var got struct {
		Mounts []MountStatus `json:"mounts"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Mounts) != 1 || got.Mounts[0].ID != "a" {
		t.Fatalf("mounts = %+v", got.Mounts)
	}
}

func TestCommandServerMountAttach(t *testing.T) {
	stub := &mountServiceStub{}
	server := &CommandServer{mounts: stub}
	body := strings.NewReader(`{"name":"docs","mount_point":"/mnt/docs","read_only":true}`)
	resp := httptest.NewRecorder()
	server.handleMounts(resp, httptest.NewRequest(http.MethodPost, "/v1/control/mounts", body))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var got MountStatus
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "id-1" || got.MountPoint != "/mnt/docs" || !got.ReadOnly {
		t.Fatalf("status = %+v", got)
	}
	if stub.attachCalls != 1 {
		t.Fatalf("attach calls = %d", stub.attachCalls)
	}

	stub.attachErr = errTest("already mounted")
	resp = httptest.NewRecorder()
	server.handleMounts(resp, httptest.NewRequest(http.MethodPost, "/v1/control/mounts", strings.NewReader(`{"mount_point":"/mnt/docs"}`)))
	if resp.Code != http.StatusConflict {
		t.Fatalf("attach error status = %d, want 409", resp.Code)
	}
}

func TestCommandServerMountDetach(t *testing.T) {
	stub := &mountServiceStub{}
	server := &CommandServer{mounts: stub}
	resp := httptest.NewRecorder()
	server.handleMountByID(resp, httptest.NewRequest(http.MethodDelete, "/v1/control/mounts/id-1", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if len(stub.detachIDs) != 1 || stub.detachIDs[0] != "id-1" {
		t.Fatalf("detach ids = %v", stub.detachIDs)
	}

	stub.detachErr = errTest(`mount "missing" not found`)
	resp = httptest.NewRecorder()
	server.handleMountByID(resp, httptest.NewRequest(http.MethodDelete, "/v1/control/mounts/missing", nil))
	if resp.Code != http.StatusNotFound {
		t.Fatalf("not found status = %d, want 404", resp.Code)
	}

	resp = httptest.NewRecorder()
	server.handleMountByID(resp, httptest.NewRequest(http.MethodPost, "/v1/control/mounts/x", nil))
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post status = %d, want 405", resp.Code)
	}
}

func TestCommandServerMountsUnavailable(t *testing.T) {
	server := &CommandServer{}
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/v1/control/mounts", nil),
		httptest.NewRequest(http.MethodDelete, "/v1/control/mounts/x", nil),
	} {
		resp := httptest.NewRecorder()
		if req.Method == http.MethodGet {
			server.handleMounts(resp, req)
		} else {
			server.handleMountByID(resp, req)
		}
		if resp.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503", req.Method, resp.Code)
		}
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
