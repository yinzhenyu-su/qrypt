package httputil

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestDefaultTransport(t *testing.T) {
	tr := DefaultTransport(15 * time.Second)
	if tr.ResponseHeaderTimeout != 15*time.Second {
		t.Errorf("ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, 15*time.Second)
	}
	if tr.MaxIdleConns != 100 {
		t.Errorf("MaxIdleConns = %d, want 100", tr.MaxIdleConns)
	}
	if tr.MaxIdleConnsPerHost != 20 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 20", tr.MaxIdleConnsPerHost)
	}
	if tr.IdleConnTimeout != 90*time.Second {
		t.Errorf("IdleConnTimeout = %v, want %v", tr.IdleConnTimeout, 90*time.Second)
	}
	if tr.TLSHandshakeTimeout != 10*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v, want %v", tr.TLSHandshakeTimeout, 10*time.Second)
	}
}

func TestNewClientWithTimeout(t *testing.T) {
	c := NewClient(5*time.Second, 10*time.Second)
	if c.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want %v", c.Timeout, 5*time.Second)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport is not *http.Transport")
	}
	if tr.ResponseHeaderTimeout != 10*time.Second {
		t.Errorf("ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, 10*time.Second)
	}
}

func TestNewClientWithoutTimeout(t *testing.T) {
	c := NewClient(0, 10*time.Second)
	if c.Timeout != 0 {
		t.Errorf("Timeout = %v, want 0 (no client-level timeout)", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport is not *http.Transport")
	}
	if tr.ResponseHeaderTimeout != 10*time.Second {
		t.Errorf("ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, 10*time.Second)
	}
}

func TestNewClientReadsFromDefaultTransport(t *testing.T) {
	// Two clients created from the same transport factory should NOT share
	// the same transport instance (each call returns a fresh Transport).
	c1 := NewClient(0, 30*time.Second)
	c2 := NewClient(0, 30*time.Second)
	if c1.Transport == c2.Transport {
		t.Error("clients share the same transport instance")
	}
}

func TestCachedDialerFallsBackToCachedIPOnDNSFailure(t *testing.T) {
	var calls []string
	dialer := newCachedDialer(func(_ context.Context, _, address string) (net.Conn, error) {
		calls = append(calls, address)
		switch address {
		case "download.example:443":
			return fakeConn{remote: &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 443}}, nil
		case "203.0.113.7:443":
			return fakeConn{remote: &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 443}}, nil
		default:
			return nil, &net.DNSError{Name: "download.example", Err: "no such host"}
		}
	})

	first, err := dialer.DialContext(context.Background(), "tcp", "download.example:443")
	if err != nil {
		t.Fatalf("first dial failed: %v", err)
	}
	_ = first.Close()

	dialer.dial = func(_ context.Context, _, address string) (net.Conn, error) {
		calls = append(calls, address)
		if address == "203.0.113.7:443" {
			return fakeConn{remote: &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 443}}, nil
		}
		return nil, &net.DNSError{Name: "download.example", Err: "no such host"}
	}
	second, err := dialer.DialContext(context.Background(), "tcp", "download.example:443")
	if err != nil {
		t.Fatalf("fallback dial failed: %v", err)
	}
	_ = second.Close()

	wantLast := "203.0.113.7:443"
	if got := calls[len(calls)-1]; got != wantLast {
		t.Fatalf("last dial address = %q, want %q; calls=%v", got, wantLast, calls)
	}
}

type fakeConn struct {
	remote net.Addr
}

func (fakeConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (fakeConn) Write([]byte) (int, error)        { return 0, io.ErrClosedPipe }
func (fakeConn) Close() error                     { return nil }
func (fakeConn) LocalAddr() net.Addr              { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (c fakeConn) RemoteAddr() net.Addr           { return c.remote }
func (fakeConn) SetDeadline(time.Time) error      { return nil }
func (fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (fakeConn) SetWriteDeadline(time.Time) error { return nil }
