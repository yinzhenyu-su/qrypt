//go:build windows

package control

import (
	"io"
	"strconv"
	"testing"
	"time"
)

func TestWindowsNamedPipeRoundTrip(t *testing.T) {
	name := `\\.\pipe\qrypt-test-` + strconv.FormatInt(time.Now().UnixNano(), 10)
	listener, err := listenLocalEndpoint("pipe", name)
	if err != nil {
		t.Fatalf("listen named pipe: %v", err)
	}
	defer listener.Close()

	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		var request [4]byte
		if _, err := io.ReadFull(conn, request[:]); err != nil {
			serverDone <- err
			return
		}
		_, err = conn.Write([]byte("pong"))
		serverDone <- err
	}()

	conn, err := dialLocalEndpoint(t.Context(), "pipe", name)
	if err != nil {
		t.Fatalf("dial named pipe: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write named pipe: %v", err)
	}
	var response [4]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		t.Fatalf("read named pipe: %v", err)
	}
	if string(response[:]) != "pong" {
		t.Fatalf("response = %q", response)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server named pipe: %v", err)
	}
}
