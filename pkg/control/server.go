package control

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

const APIVersion = "v1"

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
	ListTaskItems(context.Context, string, task.ItemFilter) ([]task.ItemResult, error)
}

type Server struct {
	socketPath string
	endpoint   string
	network    string
	address    string
	source     Snapshotter
	taskHealth taskPersistenceHealth
	taskDebug  taskDebugger
	server     *http.Server
	listener   net.Listener
	stop       context.CancelFunc
}

// SetTaskDebugger attaches the application task journal to the local debug API.
func (s *Server) SetTaskDebugger(debugger taskDebugger) {
	if s != nil {
		s.taskDebug = debugger
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
	listener, err := net.Listen(s.network, s.address)
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
	mux.HandleFunc("/v1/debug/reset", s.handleDebugReset)
	mux.HandleFunc("/v1/state", s.handleState)
	mux.HandleFunc("/v1/pending", s.handlePending)
	mux.HandleFunc("/v1/tasks", s.handleTasks)
	mux.HandleFunc("/v1/uploads", s.handleUploads)
	mux.HandleFunc("/v1/ops", s.handleOps)
	mux.HandleFunc("/v1/reads", s.handleReads)
	mux.HandleFunc("/v1/bench", s.handleBench)
	mux.HandleFunc("/v1/driver", s.handleDriver)
	mux.HandleFunc("/v1/driver/test", s.handleDriverTest)
	mux.HandleFunc("/v1/mounts/health", s.handleMountHealth)
	mux.HandleFunc("/v1/events", s.handleEvents)
	mux.HandleFunc("/v1/list", s.handleList)
	mux.HandleFunc("/v1/resolve", s.handleResolve)
	mux.HandleFunc("/v1/transfer/context", s.handleTransferContext)
	mux.HandleFunc("/v1/cache", s.handleCache)
	mux.HandleFunc("/v1/read-memory", s.handleReadMemory)
	mux.HandleFunc("/v1/staging", s.handleStaging)
	mux.HandleFunc("/v1/debug/faults/upload-cancel", s.handleDebugUploadCancelFaults)
	mux.HandleFunc("/v1/consistency", s.handleConsistency)
	mux.HandleFunc("/v1/runtime", s.handleRuntime)
	mux.HandleFunc("/v1/upload-memory", s.handleUploadMemory)
	mux.HandleFunc("/v1/goroutines", s.handleGoroutines)
	mux.HandleFunc("/v1/debug/stacks", s.handleGoroutines)
	s.server = &http.Server{Handler: mux}
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

func listenEndpoint(endpoint string) (network, address string, err error) {
	endpoint = strings.TrimSpace(endpoint)
	switch {
	case endpoint == "":
		return "", "", fmt.Errorf("control: listen endpoint required")
	case strings.HasPrefix(endpoint, "unix:"):
		path := strings.TrimPrefix(endpoint, "unix:")
		if path == "" {
			return "", "", fmt.Errorf("control: unix listen path required")
		}
		return "unix", util.ExpandHome(path), nil
	case strings.HasPrefix(endpoint, "tcp:"):
		return tcpListenEndpoint(strings.TrimPrefix(endpoint, "tcp:"))
	case strings.HasPrefix(endpoint, "http://"):
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return "", "", err
		}
		return tcpListenEndpoint(parsed.Host)
	case strings.HasPrefix(endpoint, "https://"):
		return "", "", fmt.Errorf("control: https listen is not supported")
	}
	if _, port, err := net.SplitHostPort(endpoint); err == nil && isNumericPort(port) {
		return tcpListenEndpoint(endpoint)
	}
	return "unix", util.ExpandHome(endpoint), nil
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

func tcpListenEndpoint(address string) (string, string, error) {
	if address == "" {
		return "", "", fmt.Errorf("control: tcp listen address required")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return "", "", err
	}
	if host == "" {
		return "", "", fmt.Errorf("control: tcp debug listen must bind to loopback, got %q", address)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", "", fmt.Errorf("control: tcp debug listen must bind to loopback, got %q", address)
	}
	return "tcp", address, nil
}

func (s *Server) Close(ctx context.Context) error {
	// Notify the ctx.Done goroutine started by Start so it cannot leak even
	// if the original Start context was background.
	if s.stop != nil {
		s.stop()
	}
	if s.server != nil {
		_ = s.server.Shutdown(ctx)
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
	if s.socketPath != "" {
		_ = os.Remove(s.socketPath)
	}
	return nil
}

func (s *Server) SocketPath() string {
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
