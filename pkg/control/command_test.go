package control

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type commandServiceStub struct {
	cancel string
	retry  string
}

func (s *commandServiceStub) CancelTask(_ context.Context, id string) error {
	s.cancel = id
	return nil
}

func (s *commandServiceStub) RetryTask(_ context.Context, id string) error {
	s.retry = id
	return nil
}

func TestCommandServerTaskActions(t *testing.T) {
	service := &commandServiceStub{}
	server := &CommandServer{service: service}
	for name, handler := range map[string]func(http.ResponseWriter, *http.Request){
		"cancel": server.handleCancelTask,
		"retry":  server.handleRetryTask,
	} {
		t.Run(name, func(t *testing.T) {
			resp := httptest.NewRecorder()
			handler(resp, httptest.NewRequest(http.MethodPost, "/v1/control/tasks/"+name, jsonReader(taskActionRequest{ID: "task-1"})))
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
			}
		})
	}
	if service.cancel != "task-1" || service.retry != "task-1" {
		t.Fatalf("cancel=%q retry=%q", service.cancel, service.retry)
	}
}

func jsonReader(value any) *jsonReadCloser {
	data, _ := json.Marshal(value)
	return &jsonReadCloser{Reader: bytes.NewReader(data)}
}

type jsonReadCloser struct{ *bytes.Reader }

func (*jsonReadCloser) Close() error { return nil }

var _ io.ReadCloser = (*jsonReadCloser)(nil)
