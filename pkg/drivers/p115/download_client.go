package p115

import (
	"net/http"
	"time"
)

// downloadHTTPClient derives the content-download client from the SDK's
// shared client and gives the SDK the same bounded transport. The SDK's API
// calls (GetFile, DownloadWithUA) do not accept a context, so a missing
// response header timeout there can outlive the read layer's body timeout.
// ResponseHeaderTimeout bounds the first-packet phase for both the API and the
// content request; the read layer still owns the body idle bound.
func downloadHTTPClient(base *http.Client, headerTimeout time.Duration) *http.Client {
	transport := func(base *http.Transport) *http.Transport {
		clone := base.Clone()
		if clone.ResponseHeaderTimeout <= 0 {
			clone.ResponseHeaderTimeout = headerTimeout
		}
		return clone
	}
	if base == nil {
		fallback, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return &http.Client{Transport: http.DefaultTransport}
		}
		return &http.Client{Transport: transport(fallback)}
	}
	client := *base
	switch existing := client.Transport.(type) {
	case *http.Transport:
		if existing != nil {
			client.Transport = transport(existing)
		} else if fallback, ok := http.DefaultTransport.(*http.Transport); ok {
			client.Transport = transport(fallback)
		}
	case nil:
		if fallback, ok := http.DefaultTransport.(*http.Transport); ok {
			client.Transport = transport(fallback)
		}
	}
	// Give the SDK the same bound: its API calls ignore the caller's context.
	if bounded, ok := client.Transport.(*http.Transport); ok {
		base.Transport = bounded
	}
	return &client
}
