// Package selfupdate replaces the running qrypt executable with a published
// GitHub release. It resolves the newest applicable release, downloads and
// checksum-verifies the matching archive, extracts the binary, and swaps it
// into place atomically on every supported platform.
package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DefaultRepo is the GitHub repository that publishes qrypt releases. It is a
// constant rather than derived from the Go module path, because the module
// path uses a different account (github.com/yinzhenyu/qrypt).
const DefaultRepo = "yinzhenyu-su/qrypt"

// DefaultBaseURL is the GitHub REST API root.
const DefaultBaseURL = "https://api.github.com"

// Client reads releases from a GitHub-compatible REST API. BaseURL, Repo, and
// HTTP are fields so tests can point the client at a local server.
type Client struct {
	BaseURL string
	Repo    string
	HTTP    *http.Client
	Token   string
}

// NewClient returns a client for the default repository, taking a GitHub token
// from the environment when one is present.
func NewClient() *Client {
	return &Client{
		BaseURL: DefaultBaseURL,
		Repo:    DefaultRepo,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
		Token:   TokenFromEnv(),
	}
}

// TokenFromEnv returns a GitHub token from GITHUB_TOKEN or GH_TOKEN, so an
// unauthenticated 60-requests-per-hour API limit can be raised.
func TokenFromEnv() string {
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

// Latest returns the newest stable release.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	var releases []Release
	if err := c.get(ctx, "/repos/"+c.repository()+"/releases?per_page=100", &releases); err != nil {
		return Release{}, err
	}
	return SelectRelease(releases)
}

// ByTag returns the release with the given tag.
func (c *Client) ByTag(ctx context.Context, tag string) (Release, error) {
	var release Release
	err := c.get(ctx, "/repos/"+c.repository()+"/releases/tags/"+url.PathEscape(tag), &release)
	var statusErr *statusError
	if errors.As(err, &statusErr) && statusErr.Code == http.StatusNotFound {
		return Release{}, fmt.Errorf("%w: %s", ErrVersionNotFound, tag)
	}
	if err != nil {
		return Release{}, err
	}
	return release, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	rawURL := c.base() + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &statusError{Code: resp.StatusCode, Path: path, Body: strings.TrimSpace(string(body))}
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return DefaultBaseURL
}

func (c *Client) repository() string {
	if c.Repo != "" {
		return c.Repo
	}
	return DefaultRepo
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// statusError is a non-200 API response. It is a distinct type so callers can
// translate a 404 into a domain error.
type statusError struct {
	Code int
	Path string
	Body string
}

func (e *statusError) Error() string {
	msg := fmt.Sprintf("github api %s: %s", e.Path, http.StatusText(e.Code))
	if e.Code == http.StatusForbidden || e.Code == http.StatusTooManyRequests {
		msg += " (rate limit reached; set GITHUB_TOKEN to raise it)"
	}
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}
