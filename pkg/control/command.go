package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
)

// CommandService is the mutable application surface exposed to local CLI
// commands. It is intentionally separate from Snapshotter and taskDebugger:
// debug clients can observe a Core without receiving mutation methods.
type CommandService interface {
	CancelTask(context.Context, string) error
	RetryTask(context.Context, string) error
}

type CommandServer struct {
	network  string
	address  string
	service  CommandService
	server   *http.Server
	listener net.Listener
	stop     context.CancelFunc
}

func NewCommandServer(endpoint string, service CommandService) (*CommandServer, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("control: command endpoint required")
	}
	if service == nil {
		return nil, fmt.Errorf("control: command service required")
	}
	network, address, err := listenEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if network == "tcp" {
		return nil, fmt.Errorf("control: command server requires a local endpoint")
	}
	return &CommandServer{network: network, address: address, service: service}, nil
}

func (s *CommandServer) Start(ctx context.Context) error {
	if s.network == "unix" {
		if err := os.MkdirAll(filepath.Dir(s.address), 0o755); err != nil {
			return err
		}
		if err := removeStaleSocket(s.address); err != nil {
			return err
		}
	}
	listener, err := listenEndpointNetwork(s.network, s.address)
	if err != nil {
		return err
	}
	if s.network == "unix" {
		if err := os.Chmod(s.address, 0o600); err != nil {
			_ = listener.Close()
			return err
		}
	}
	s.listener = listener
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/control/tasks/cancel", s.handleCancelTask)
	mux.HandleFunc("/v1/control/tasks/retry", s.handleRetryTask)
	s.server = &http.Server{Handler: mux}
	ctx, stop := context.WithCancel(ctx)
	s.stop = stop
	go func() {
		<-ctx.Done()
		_ = s.Close(context.Background())
	}()
	go func() { _ = s.server.Serve(listener) }()
	return nil
}

func (s *CommandServer) Close(ctx context.Context) error {
	if s.stop != nil {
		s.stop()
	}
	if s.server != nil {
		_ = s.server.Shutdown(ctx)
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
	if s.network == "unix" && s.address != "" {
		_ = os.Remove(s.address)
	}
	return nil
}

type taskActionRequest struct {
	ID string `json:"id"`
}

func (s *CommandServer) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	s.handleTaskAction(w, r, s.service.CancelTask)
}

func (s *CommandServer) handleRetryTask(w http.ResponseWriter, r *http.Request) {
	s.handleTaskAction(w, r, s.service.RetryTask)
}

func (s *CommandServer) handleTaskAction(w http.ResponseWriter, r *http.Request, action func(context.Context, string) error) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req taskActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "task id required", http.StatusBadRequest)
		return
	}
	if err := action(r.Context(), req.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"id": req.ID})
}
