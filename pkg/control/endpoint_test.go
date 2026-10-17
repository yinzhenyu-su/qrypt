package control

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestParseEndpointRecognizesNamedPipe(t *testing.T) {
	got, err := parseEndpoint(`pipe:\\.\pipe\qrypt-test-debug`)
	if err != nil {
		t.Fatalf("parse named pipe: %v", err)
	}
	if got.network != "pipe" {
		t.Fatalf("network = %q, want pipe", got.network)
	}
	if got.address != `\\.\pipe\qrypt-test-debug` {
		t.Fatalf("address = %q", got.address)
	}
}

func TestParseEndpointExpandsUnixHome(t *testing.T) {
	got, err := parseEndpoint("unix:~/qrypt.sock")
	if err != nil {
		t.Fatalf("parse unix endpoint: %v", err)
	}
	if strings.HasPrefix(got.address, "~") {
		t.Fatalf("address was not expanded: %q", got.address)
	}
}

func TestLocalEndpointRejectsForeignPlatformKind(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Unix implementation is not built on Windows")
	}
	if _, err := listenLocalEndpoint("pipe", `\\.\pipe\qrypt-test`); err == nil {
		t.Fatal("expected named pipe to be rejected on Unix")
	}
	if _, err := dialLocalEndpoint(context.Background(), "pipe", `\\.\pipe\qrypt-test`); err == nil {
		t.Fatal("expected named pipe dial to be rejected on Unix")
	}
}

func TestNewClientRejectsNonLoopbackHTTP(t *testing.T) {
	if _, err := NewClient("http://0.0.0.0:19090"); err == nil {
		t.Fatal("expected non-loopback HTTP endpoint to be rejected")
	}
}

func TestServerSocketPathPreservesNamedPipeEndpoint(t *testing.T) {
	server := &Server{network: "pipe", address: `\\.\pipe\qrypt-test`}
	if got := server.SocketPath(); got != `pipe:\\.\pipe\qrypt-test` {
		t.Fatalf("socket path = %q", got)
	}
}
