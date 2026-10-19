package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func releaseListJSON(t *testing.T, v any) []byte {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestClientLatestUsesOverriddenRepo(t *testing.T) {
	t.Parallel()
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(releaseListJSON(t, []Release{
			{Tag: "v0.5.2"},
			{Tag: "v0.6.0", Prerelease: true},
			{Tag: "v0.5.3"},
		}))
	}))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL, Repo: "testowner/testrepo", HTTP: srv.Client()}
	release, err := client.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest error: %v", err)
	}
	if release.Tag != "v0.5.3" {
		t.Fatalf("Latest picked %q, want v0.5.3", release.Tag)
	}
	if gotPath != "/repos/testowner/testrepo/releases" {
		t.Fatalf("client requested %q, want the overridden repo path", gotPath)
	}
}

func TestClientSendsToken(t *testing.T) {
	t.Parallel()
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(releaseListJSON(t, []Release{{Tag: "v0.5.3"}}))
	}))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL, Repo: "o/r", HTTP: srv.Client(), Token: "secret"}
	if _, err := client.Latest(context.Background()); err != nil {
		t.Fatalf("Latest error: %v", err)
	}
	if auth != "Bearer secret" {
		t.Fatalf("Authorization = %q, want Bearer secret", auth)
	}
}

func TestClientByTag(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/releases/tags/v0.4.0" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(releaseListJSON(t, Release{Tag: "v0.4.0"}))
	}))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL, Repo: "o/r", HTTP: srv.Client()}
	release, err := client.ByTag(context.Background(), "v0.4.0")
	if err != nil {
		t.Fatalf("ByTag error: %v", err)
	}
	if release.Tag != "v0.4.0" {
		t.Fatalf("ByTag returned %q", release.Tag)
	}
	if _, err := client.ByTag(context.Background(), "v9.9.9"); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("ByTag(missing) error = %v, want ErrVersionNotFound", err)
	}
}

func TestClientRateLimitMessage(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("rate limited"))
	}))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL, Repo: "o/r", HTTP: srv.Client()}
	_, err := client.Latest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rate limit") || !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Fatalf("rate limit error = %v", err)
	}
}
