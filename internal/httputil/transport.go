package httputil

import (
	"net"
	"net/http"
	"time"
)

// DefaultTransport creates an http.Transport with sensible defaults for
// cloud-drive API calls: keep-alive, HTTP/2, and configurable
// ResponseHeaderTimeout.
func DefaultTransport(responseHeaderTimeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
	}
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
