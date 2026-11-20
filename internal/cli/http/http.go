// Package httpcli exposes the read-only HTTP API of a resident Core. The
// gateway is a thin client: it talks to the Core over the local control
// endpoint and never lets the Core listen on TCP itself.
package httpcli

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	clicore "github.com/yinzhenyu/qrypt/internal/cli/core"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/internal/httpapi"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

const (
	defaultListen     = "127.0.0.1:8800"
	tokenEnv          = "QRYPT_HTTP_TOKEN"
	shutdownTimeout   = 10 * time.Second
	readHeaderTimeout = 10 * time.Second
)

// NewCommand exposes the HTTP gateway.
func NewCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "http",
		Short: "Serve the read-only HTTP API for a resident Core",
		Long: `Serve a read-only HTTP API backed by the resident Core.

The gateway runs as its own process and reaches the Core through the local
control endpoint, so the Core never listens on a network port. Every request is
authorized with a bearer token; browser clients use the same token from a CORS
allowlist. Bind to loopback by default; a non-loopback address requires TLS.`,
		Example: `  qrypt http serve
  qrypt http serve --listen 127.0.0.1:9000
  qrypt http serve --listen 0.0.0.0:8443 --tls-cert ./cert.pem --tls-key ./key.pem`,
		Args: cliruntime.CommandGroupArgs(rt, nil),
		RunE: cliruntime.ShowHelp,
	}
	rt.WithPersistentConfigFlag(cmd)
	cmd.AddCommand(newServeCommand(rt))
	return cmd
}

type serveFlags struct {
	listen       string
	allowOrigins []string
	allowHosts   []string
	token        string
	certFile     string
	keyFile      string
}

func newServeCommand(rt cliruntime.Runtime) *cobra.Command {
	flags := serveFlags{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HTTP gateway",
		Long: `Run the HTTP gateway for the configured state directory.

The command lazily starts the resident Core when none is running, then serves
the read-only v1 API until SIGINT/SIGTERM. Without --token a random bearer token
is generated and printed once. The Core keeps running after the gateway exits
unless it was started by this command and is idle.`,
		Example: `  qrypt http serve
  qrypt http serve --token "$QRYPT_HTTP_TOKEN"`,
		Args: cliruntime.NoArgs(rt),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(rt, cmd, flags)
		},
	}
	cmd.Flags().StringVar(&flags.listen, "listen", defaultListen, "address to listen on (host:port)")
	cmd.Flags().StringArrayVar(&flags.allowOrigins, "allow-origin", nil, "allowed browser origin (repeatable)")
	cmd.Flags().StringArrayVar(&flags.allowHosts, "allow-host", nil, "extra Host header value accepted for non-loopback listeners")
	cmd.Flags().StringVar(&flags.token, "token", "", "bearer token (default: $"+tokenEnv+", otherwise generated)")
	cmd.Flags().StringVar(&flags.certFile, "tls-cert", "", "TLS certificate file")
	cmd.Flags().StringVar(&flags.keyFile, "tls-key", "", "TLS key file")
	return cmd
}

func runServe(rt cliruntime.Runtime, cmd *cobra.Command, flags serveFlags) error {
	state, err := rt.CommandConfig(cmd)
	if err != nil {
		return err
	}
	if state.Cfg == nil {
		return rt.ConfigNotFoundError()
	}
	listen, err := normalizeListen(flags.listen)
	if err != nil {
		return err
	}
	if (flags.certFile == "") != (flags.keyFile == "") {
		return fmt.Errorf("http: --tls-cert and --tls-key must be provided together")
	}
	useTLS := flags.certFile != "" && flags.keyFile != ""
	if !listenIsLoopback(listen) && !useTLS {
		return fmt.Errorf("http: refusing to listen on %s without TLS; use --tls-cert and --tls-key", listen)
	}
	token := flags.token
	if token == "" {
		token = os.Getenv(tokenEnv)
	}
	generated := false
	if token == "" {
		token, err = randomToken()
		if err != nil {
			return fmt.Errorf("http: generate bearer token: %w", err)
		}
		generated = true
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	client, err := clicore.EnsureClient(ctx, rt, state)
	if err != nil {
		return err
	}
	gateway := httpapi.New(coreAPI{client: client}, httpapi.Options{
		Token:        token,
		AllowOrigins: flags.allowOrigins,
		AllowedHosts: flags.allowHosts,
	})
	server := &http.Server{
		Handler:           gateway.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("http: listen on %s: %w", listen, err)
	}
	if useTLS {
		certificate, loadErr := tls.LoadX509KeyPair(flags.certFile, flags.keyFile)
		if loadErr != nil {
			_ = listener.Close()
			return fmt.Errorf("http: load TLS key pair: %w", loadErr)
		}
		server.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS12,
		}
	}
	if generated {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "http: generated bearer token: %s\n", token)
	}
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "http: listening on %s://%s\n", scheme, listener.Addr().String())
	logging.L.Infof("[HTTP] gateway: listening on %s://%s", scheme, listener.Addr().String())

	serveErr := make(chan error, 1)
	go func() {
		var serve error
		if useTLS {
			serve = server.ServeTLS(listener, "", "")
		} else {
			serve = server.Serve(listener)
		}
		if errors.Is(serve, http.ErrServerClosed) {
			serve = nil
		}
		serveErr <- serve
	}()

	shutdownCtx, stopSignals := rt.ShutdownContext(cmd)
	defer stopSignals()
	select {
	case serveErrValue := <-serveErr:
		return serveErrValue
	case <-shutdownCtx.Done():
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer closeCancel()
	if err := server.Shutdown(closeCtx); err != nil {
		_ = server.Close()
	}
	logging.L.Infof("[HTTP] gateway: stopped")
	return nil
}

// coreAPI adapts the control client to the gateway's Core interface.
type coreAPI struct {
	client *control.Client
}

func (c coreAPI) RuntimeStatus(ctx context.Context) (control.RuntimeStatus, error) {
	return c.client.RuntimeStatus(ctx)
}

func (c coreAPI) ListMounts(ctx context.Context) ([]control.MountStatus, error) {
	return c.client.ListMounts(ctx)
}

func (c coreAPI) FSStat(ctx context.Context, requestPath string) (control.FSEntry, error) {
	return c.client.FSStat(ctx, requestPath)
}

func (c coreAPI) FSList(ctx context.Context, requestPath string) ([]control.FSEntry, error) {
	return c.client.FSList(ctx, requestPath)
}

func (c coreAPI) FSReadStream(ctx context.Context, requestPath string, offset, size int64, raw bool) (io.ReadCloser, error) {
	return c.client.FSReadStream(ctx, requestPath, offset, size, raw)
}

func (c coreAPI) FSPendingUploads(ctx context.Context) ([]vfs.PendingUpload, error) {
	return c.client.FSPendingUploads(ctx)
}

func normalizeListen(listen string) (string, error) {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return "", fmt.Errorf("http: --listen must not be empty")
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("http: invalid --listen address %q: %w", listen, err)
	}
	if port == "" {
		return "", fmt.Errorf("http: --listen requires a port")
	}
	if host == "" {
		return net.JoinHostPort("0.0.0.0", port), nil
	}
	return net.JoinHostPort(host, port), nil
}

func listenIsLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func randomToken() (string, error) {
	buffer := make([]byte, 20)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}
