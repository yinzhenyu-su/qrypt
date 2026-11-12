package p115

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDownloadHTTPClientBoundsResponseHeaderWait(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)

	client := downloadHTTPClient(nil, 50*time.Millisecond)
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("download client returned before the provider sent headers")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("header timeout took %s, want it bounded near 50ms", elapsed)
	}
}

func TestDownloadHTTPClientClonesSharedTransport(t *testing.T) {
	base := &http.Client{Transport: &http.Transport{}}
	derived := downloadHTTPClient(base, time.Minute)
	baseTransport, ok := base.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("base transport = %T", base.Transport)
	}
	if baseTransport.ResponseHeaderTimeout != time.Minute {
		t.Fatalf("SDK transport ResponseHeaderTimeout = %s, want 1m (API calls have no context)", baseTransport.ResponseHeaderTimeout)
	}
	derivedTransport, ok := derived.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("derived transport = %T", derived.Transport)
	}
	if derivedTransport.ResponseHeaderTimeout != time.Minute {
		t.Fatalf("derived ResponseHeaderTimeout = %s, want 1m", derivedTransport.ResponseHeaderTimeout)
	}
}

func TestReadProfileOverridesFetchTimeouts(t *testing.T) {
	profile := (&Driver{}).ReadProfile()
	if profile.FetchIdleTimeout != 90*time.Second {
		t.Fatalf("FetchIdleTimeout = %s, want 90s", profile.FetchIdleTimeout)
	}
	if profile.FetchTotalTimeout != 30*time.Minute {
		t.Fatalf("FetchTotalTimeout = %s, want 30m", profile.FetchTotalTimeout)
	}
}
