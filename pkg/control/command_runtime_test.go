package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type runtimeServiceStub struct {
	status      RuntimeStatus
	statusErr   error
	shutdownErr error
	shutdowns   int
}

func (s *runtimeServiceStub) Status(context.Context) (RuntimeStatus, error) {
	return s.status, s.statusErr
}

func (s *runtimeServiceStub) Shutdown(context.Context) error {
	s.shutdowns++
	return s.shutdownErr
}

func TestCommandServerRuntimeStatus(t *testing.T) {
	stub := &runtimeServiceStub{status: RuntimeStatus{PID: 42, Version: "v0.7.1", Busy: true, StartedAt: time.Unix(100, 0).UTC()}}
	server := &CommandServer{runtime: stub}

	resp := httptest.NewRecorder()
	server.handleRuntimeStatus(resp, httptest.NewRequest(http.MethodGet, "/v1/control/status", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var got RuntimeStatus
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.PID != 42 || got.Version != "v0.7.1" || !got.Busy {
		t.Fatalf("status = %+v", got)
	}

	resp = httptest.NewRecorder()
	server.handleRuntimeStatus(resp, httptest.NewRequest(http.MethodPost, "/v1/control/status", nil))
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", resp.Code)
	}
}

func TestCommandServerRuntimeStatusUnavailable(t *testing.T) {
	server := &CommandServer{}
	resp := httptest.NewRecorder()
	server.handleRuntimeStatus(resp, httptest.NewRequest(http.MethodGet, "/v1/control/status", nil))
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.Code)
	}
}

func TestCommandServerRuntimeShutdown(t *testing.T) {
	stub := &runtimeServiceStub{}
	server := &CommandServer{runtime: stub}

	resp := httptest.NewRecorder()
	server.handleRuntimeShutdown(resp, httptest.NewRequest(http.MethodPost, "/v1/control/shutdown", nil))
	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var body map[string]bool
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body["stopping"] || stub.shutdowns != 1 {
		t.Fatalf("body = %v, shutdowns = %d", body, stub.shutdowns)
	}

	resp = httptest.NewRecorder()
	server.handleRuntimeShutdown(resp, httptest.NewRequest(http.MethodGet, "/v1/control/shutdown", nil))
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET shutdown = %d, want 405", resp.Code)
	}
}

func TestCommandServerRuntimeShutdownUnavailable(t *testing.T) {
	server := &CommandServer{}
	resp := httptest.NewRecorder()
	server.handleRuntimeShutdown(resp, httptest.NewRequest(http.MethodPost, "/v1/control/shutdown", nil))
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.Code)
	}
}

func TestCommandServerRuntimeShutdownConflict(t *testing.T) {
	stub := &runtimeServiceStub{shutdownErr: context.Canceled}
	server := &CommandServer{runtime: stub}
	resp := httptest.NewRecorder()
	server.handleRuntimeShutdown(resp, httptest.NewRequest(http.MethodPost, "/v1/control/shutdown", nil))
	if resp.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.Code)
	}
}
