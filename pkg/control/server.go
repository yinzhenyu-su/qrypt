package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

const APIVersion = "v1"

// controlShutdownGrace bounds the background drain that Close starts. The
// initiating caller may return as soon as its own context expires, but the
// drain itself must still finish so no goroutine or listener outlives the
// server.
const controlShutdownGrace = 5 * time.Second

type Snapshotter interface {
	DebugSnapshot() diagnostics.DebugSnapshot
}

type debugResetter interface {
	DebugReset(context.Context) error
}

type taskPersistenceHealth interface {
	TaskPersistenceError() error
}

type taskDebugger interface {
	ListTasks(context.Context, task.Filter) ([]task.Task, error)
	ListTaskItems(context.Context, string, task.ItemFilter) ([]task.ItemTracking, error)
}

type taskEventDebugger interface {
	OpenTaskEventsFrom(context.Context, task.Filter, uint64) (*task.Subscription, error)
}

type Server struct {
	socketPath string
	endpoint   string
	network    string
	address    string
	source     Snapshotter
	taskHealth taskPersistenceHealth
	taskDebug  taskDebugger
	taskEvents taskEventDebugger
	server     *http.Server
	listener   net.Listener
	stop       context.CancelFunc
	cancelBase context.CancelFunc
	mutable    bool
	closeOnce  sync.Once
	closeDone  chan struct{}
	closeErr   error
}

// SetTaskDebugger attaches the application task journal to the local debug API.
func (s *Server) SetTaskDebugger(debugger taskDebugger) {
	if s != nil {
		s.taskDebug = debugger
		s.taskEvents, _ = debugger.(taskEventDebugger)
	}
}

// SetTaskPersistenceHealth attaches application-level task persistence health
// without coupling the VFS snapshot schema to the task subsystem.
func (s *Server) SetTaskPersistenceHealth(health taskPersistenceHealth) {
	if s != nil {
		s.taskHealth = health
	}
}

func NewServer(socketPath string, source Snapshotter) (*Server, error) {
	if socketPath == "" {
		return nil, fmt.Errorf("control: socket path required")
	}
	if source == nil {
		return nil, fmt.Errorf("control: snapshot source required")
	}
	network, address, err := listenEndpoint(socketPath)
	if err != nil {
		return nil, err
	}
	server := &Server{
		endpoint: socketPath,
		network:  network,
		address:  address,
		source:   source,
	}
	if network == "unix" {
		server.socketPath = address
	}
	return server, nil
}

func (s *Server) Start(ctx context.Context) error {
	if s.network == "unix" {
		if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o755); err != nil {
			return err
		}
		if err := removeStaleSocket(s.socketPath); err != nil {
			return err
		}
	}
	listener, err := listenEndpointNetwork(s.network, s.address)
	if err != nil {
		return err
	}
	if s.network == "unix" {
		if err := os.Chmod(s.socketPath, 0o600); err != nil {
			listener.Close()
			return err
		}
	}
	s.listener = listener
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", s.handleHealth)
	if s.mutable {
		mux.HandleFunc("/v1/debug/reset", s.handleDebugReset)
		mux.HandleFunc("/v1/bench", s.handleBench)
		mux.HandleFunc("/v1/driver/test", s.handleDriverTest)
		mux.HandleFunc("/v1/debug/faults/upload-cancel", s.handleDebugUploadCancelFaults)
	}
	mux.HandleFunc("/v1/state", s.handleState)
	mux.HandleFunc("/v1/pending", s.handlePending)
	mux.HandleFunc("/v1/tasks", s.handleTasks)
	mux.HandleFunc("/v1/task-events", s.handleTaskEvents)
	mux.HandleFunc("/v1/uploads", s.handleUploads)
	mux.HandleFunc("/v1/ops", s.handleOps)
	mux.HandleFunc("/v1/reads", s.handleReads)
	mux.HandleFunc("/v1/driver", s.handleDriver)
	mux.HandleFunc("/v1/mounts/health", s.handleMountHealth)
	mux.HandleFunc("/v1/events", s.handleEvents)
	mux.HandleFunc("/v1/list", s.handleList)
	mux.HandleFunc("/v1/resolve", s.handleResolve)
	mux.HandleFunc("/v1/transfer/context", s.handleTransferContext)
	mux.HandleFunc("/v1/cache", s.handleCache)
	mux.HandleFunc("/v1/read-memory", s.handleReadMemory)
	mux.HandleFunc("/v1/staging", s.handleStaging)
	mux.HandleFunc("/v1/consistency", s.handleConsistency)
	mux.HandleFunc("/v1/runtime", s.handleRuntime)
	mux.HandleFunc("/v1/upload-memory", s.handleUploadMemory)
	mux.HandleFunc("/v1/goroutines", s.handleGoroutines)
	mux.HandleFunc("/v1/debug/stacks", s.handleGoroutines)
	// BaseContext is cancelled by Close so long-poll handlers (task events,
	// offline wait) observe shutdown even while http.Server.Shutdown is still
	// waiting for them.
	baseCtx, cancelBase := context.WithCancel(context.Background())
	s.cancelBase = cancelBase
	s.server = &http.Server{
		Handler: mux,
		BaseContext: func(net.Listener) context.Context {
			return baseCtx
		},
	}
	s.closeDone = make(chan struct{})
	// Derive our own context so the goroutine below exits on Close even when
	// the caller passed a background context (context.Background().Done() is
	// nil, which would block the <-ctx.Done() receive forever).
	ctx, stop := context.WithCancel(ctx)
	s.stop = stop
	go func() {
		<-ctx.Done()
		_ = s.Close(context.Background())
	}()
	go func() {
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			logging.L.Warnf("[CONTROL] server stopped with error listen=%q err=%v", s.ListenAddress(), err)
		}
	}()
	logging.L.Infof("[CONTROL] listening %s", s.ListenAddress())
	return nil
}

func removeStaleSocket(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("control: socket already in use: %s", path)
	}
	return os.Remove(path)
}

// isNumericPort reports whether port is a bare decimal port number. A bare
// endpoint is treated as TCP only when its port is numeric; on Windows a
// unix socket path like "C:\...\qrypt.sock" also splits as host:port, so it
// must not be routed into the TCP branch.
func isNumericPort(port string) bool {
	if port == "" {
		return false
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Close stops the server. Exactly one drain runs; every caller waits for that
// drain or for its own context, whichever happens first, so a caller with a
// deadline is never held hostage by another caller's blocking shutdown.
func (s *Server) Close(ctx context.Context) error {
	if s == nil || s.closeDone == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		// Notify the ctx.Done goroutine started by Start so it cannot leak even
		// if the original Start context was background.
		if s.stop != nil {
			s.stop()
		}
		// Cancel handler contexts first: long-poll requests must stop waiting
		// before Shutdown can drain them.
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

// shutdown drains connections and releases the listener. It always terminates
// because the graceful phase is bounded and a timed-out drain forces the
// remaining connections closed.
func (s *Server) shutdown() {
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
	if s.socketPath != "" {
		if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	s.closeErr = errors.Join(errs...)
}

func (s *Server) SocketPath() string {
	if s.network == "pipe" {
		return "pipe:" + s.address
	}
	return s.socketPath
}

func (s *Server) ListenAddress() string {
	if s.listener != nil {
		return s.network + ":" + s.listener.Addr().String()
	}
	if s.network == "unix" {
		return "unix:" + s.socketPath
	}
	return s.network + ":" + s.address
}
