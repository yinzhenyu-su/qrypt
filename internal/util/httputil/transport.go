package httputil

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultTransport creates an http.Transport with sensible defaults for
// cloud-drive API calls: keep-alive, HTTP/2, and configurable
// ResponseHeaderTimeout.
func DefaultTransport(responseHeaderTimeout time.Duration) *http.Transport {
	dialer := newCachedDialer((&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext)
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
	}
}

type cachedDialer struct {
	dial func(context.Context, string, string) (net.Conn, error)
	mu   sync.RWMutex
	ips  map[string][]string
}

func newCachedDialer(dial func(context.Context, string, string) (net.Conn, error)) *cachedDialer {
	return &cachedDialer{dial: dial, ips: map[string][]string{}}
}

func (d *cachedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := d.dial(ctx, network, address)
	if err == nil {
		d.cacheRemoteIP(address, conn.RemoteAddr())
		return conn, nil
	}
	if !isNameResolutionError(err) {
		return nil, err
	}
	host, port, splitErr := net.SplitHostPort(address)
	if splitErr != nil || net.ParseIP(host) != nil {
		return nil, err
	}
	for _, ip := range d.cachedIPs(host) {
		conn, dialErr := d.dial(ctx, network, net.JoinHostPort(ip, port))
		if dialErr == nil {
			return conn, nil
		}
	}
	return nil, err
}

func (d *cachedDialer) cacheRemoteIP(address string, addr net.Addr) {
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) != nil {
		return
	}
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp.IP == nil {
		return
	}
	key := strings.ToLower(host)
	ip := tcp.IP.String()
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, existing := range d.ips[key] {
		if existing == ip {
			return
		}
	}
	d.ips[key] = append([]string{ip}, d.ips[key]...)
}

func (d *cachedDialer) cachedIPs(host string) []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	ips := d.ips[strings.ToLower(host)]
	return append([]string(nil), ips...)
}

func isNameResolutionError(err error) bool {
	var dnsErr *net.DNSError
	if strings.Contains(strings.ToLower(err.Error()), "no such host") {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "lookup ") || errors.As(err, &dnsErr)
}

// NewClient returns an http.Client using DefaultTransport.  If timeout > 0 it
// is set as the client-level timeout; otherwise the transport-level
// ResponseHeaderTimeout applies.
func NewClient(timeout, responseHeaderTimeout time.Duration) *http.Client {
	c := &http.Client{Transport: DefaultTransport(responseHeaderTimeout)}
	if timeout > 0 {
		c.Timeout = timeout
	}
	return c
}
