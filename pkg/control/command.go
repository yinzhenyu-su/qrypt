package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	shareapi "github.com/yinzhenyu/qrypt/pkg/share"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

// CommandService is retained as the transport-facing name for the shared task
// control contract. It is intentionally separate from Snapshotter and
// taskDebugger: debug clients can observe a Core without receiving mutation
// methods.
type CommandService = task.Controller

type CommandServer struct {
	network    string
	address    string
	service    CommandService
	offline    OfflineService
	share      shareapi.Service
	server     *http.Server
	listener   net.Listener
	stop       context.CancelFunc
	cancelBase context.CancelFunc
	closeOnce  sync.Once
	closeDone  chan struct{}
	closeErr   error
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
	mux.HandleFunc("/v1/control/offline/tasks", s.handleOfflineTasks)
	mux.HandleFunc("/v1/control/offline/tasks/", s.handleOfflineTask)
	mux.HandleFunc("/v1/control/offline/inspect", s.handleOfflineInspect)
	mux.HandleFunc("/v1/control/offline/wait", s.handleOfflineWait)
	mux.HandleFunc("/v1/control/share/links", s.handleShareLinks)
	mux.HandleFunc("/v1/control/share/links/", s.handleShareLink)
	mux.HandleFunc("/v1/control/share/inspect", s.handleShareInspect)
	mux.HandleFunc("/v1/control/share/tasks", s.handleShareTasks)
	mux.HandleFunc("/v1/control/share/tasks/", s.handleShareTask)
	mux.HandleFunc("/v1/control/share/wait", s.handleShareWait)
	// BaseContext is cancelled by Close so the offline wait long-poll handler
	// stops even while http.Server.Shutdown is draining.
	baseCtx, cancelBase := context.WithCancel(context.Background())
	s.cancelBase = cancelBase
	s.server = &http.Server{
		Handler: mux,
		BaseContext: func(net.Listener) context.Context {
			return baseCtx
		},
	}
	s.closeDone = make(chan struct{})
	ctx, stop := context.WithCancel(ctx)
	s.stop = stop
	go func() {
		<-ctx.Done()
		_ = s.Close(context.Background())
	}()
	go func() { _ = s.server.Serve(listener) }()
	return nil
}

// Close stops the command server. One drain runs; each caller waits for it or
// for its own context, so a deadline is never defeated by a concurrent caller.
func (s *CommandServer) Close(ctx context.Context) error {
	if s == nil || s.closeDone == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.stop != nil {
			s.stop()
		}
		if s.cancelBase != nil {
			s.cancelBase()
		}
		go s.shutdown()
	})
	select {
	case <-s.closeDone:
		return s.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *CommandServer) shutdown() {
	defer close(s.closeDone)
	var errs []error
	if s.server != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), controlShutdownGrace)
		err := s.server.Shutdown(shutdownCtx)
		cancel()
		if err != nil {
			errs = append(errs, err)
			if closeErr := s.server.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
				errs = append(errs, closeErr)
			}
		}
	}
	if s.listener != nil {
		if err := s.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}
	if s.network == "unix" && s.address != "" {
		if err := os.Remove(s.address); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	s.closeErr = errors.Join(errs...)
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
