package quark

// These fixtures pin the login protocol to imeiming/quark-drive commit
// 6976f165ee2f3bc19ec1496276d13723803f4722 (2026-05-11):
// references/auth.md (blob 811c75d57189a46cd5b6174c81457f1b089f3c67) and
// scripts/quark_cli.py (blob aac9e84eb4b598a2918181bd117514fa972e2eb4).
// The endpoints are undocumented; a drift fails these tests or the cookie
// verification step before any configuration is written.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type recordingLoginUI struct {
	urls   []string
	cancel context.CancelFunc
	err    error
}

func (u *recordingLoginUI) ShowChallenge(url string) error {
	u.urls = append(u.urls, url)
	if u.cancel != nil {
		u.cancel()
	}
	return u.err
}

type loginServer struct {
	mu          sync.Mutex
	tokenCalls  int
	ticketCalls int
	tickets     map[string][]int
	sortStatus  int
	sortCode    int
	setCookies  bool
	redirect    bool
	tokenError  bool
}

func (s *loginServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/cas/ajax/getTokenForQrcodeLogin", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.tokenCalls++
		call := s.tokenCalls
		broken := s.tokenError
		s.mu.Unlock()
		if r.URL.Query().Get("client_id") != loginQRClientID {
			t.Errorf("client_id = %q, want %q", r.URL.Query().Get("client_id"), loginQRClientID)
		}
		if r.URL.Query().Get("v") != loginQRVersion {
			t.Errorf("v = %q, want %q", r.URL.Query().Get("v"), loginQRVersion)
		}
		if r.URL.Query().Get("request_id") == "" {
			t.Error("request_id is empty")
		}
		if broken {
			fmt.Fprint(w, `{"status":500,"message":"no"}`)
			return
		}
		fmt.Fprintf(w, `{"status":2000000,"message":"ok","data":{"members":{"token":"tok-%d"}}}`, call)
	})
	mux.HandleFunc("/cas/ajax/getServiceTicketByQrcodeToken", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.ticketCalls++
		token := r.URL.Query().Get("token")
		statuses := s.tickets[token]
		status := qrStatusWaiting
		if len(statuses) > 0 {
			status = statuses[0]
			s.tickets[token] = statuses[1:]
		}
		s.mu.Unlock()
		if r.URL.Query().Get("client_id") != loginQRClientID {
			t.Errorf("ticket client_id = %q", r.URL.Query().Get("client_id"))
		}
		switch status {
		case qrStatusOK:
			fmt.Fprint(w, `{"status":2000000,"message":"ok","data":{"members":{"service_ticket":"ticket-1"}}}`)
		default:
			fmt.Fprintf(w, `{"status":%d,"message":"state"}`, status)
		}
	})
	mux.HandleFunc("/account/info", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("st") != "ticket-1" {
			t.Errorf("st = %q, want ticket-1", r.URL.Query().Get("st"))
		}
		if r.URL.Query().Get("lw") != "scan" {
			t.Errorf("lw = %q, want scan", r.URL.Query().Get("lw"))
		}
		if s.redirect {
			http.Redirect(w, r, "/account/info/final", http.StatusFound)
			return
		}
		s.writeLoginCookies(w)
	})
	mux.HandleFunc("/account/info/final", func(w http.ResponseWriter, r *http.Request) {
		s.writeLoginCookies(w)
	})
	mux.HandleFunc("/file/sort", func(w http.ResponseWriter, r *http.Request) {
		status := s.sortStatus
		if status == 0 {
			status = 200
		}
		fmt.Fprintf(w, `{"status":%d,"code":%d,"message":"sort"}`, status, s.sortCode)
	})
	return httptest.NewServer(mux)
}

func (s *loginServer) writeLoginCookies(w http.ResponseWriter) {
	if !s.setCookies {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "__pus", Value: "pus", Domain: ".quark.cn", Path: "/"})
	http.SetCookie(w, &http.Cookie{Name: "__kps", Value: "kps", Domain: "quark.cn", Path: "/"})
	http.SetCookie(w, &http.Cookie{Name: "__uid", Value: "42", Domain: ".quark.cn", Path: "/"})
	http.SetCookie(w, &http.Cookie{Name: "other", Value: "x", Domain: "example.com", Path: "/"})
	w.WriteHeader(http.StatusOK)
}

func loginTestOptions(server *httptest.Server) LoginOptions {
	return LoginOptions{
		UOPBaseURL:    server.URL,
		PanBaseURL:    server.URL,
		DriverBaseURL: server.URL,
		DriverV2URL:   server.URL,
		PollInterval:  time.Millisecond,
		MaxRenewals:   2,
		HTTPClient:    server.Client(),
	}
}

func TestQuarkLoginSuccess(t *testing.T) {
	fixture := &loginServer{
		tickets:    map[string][]int{"tok-1": {qrStatusWaiting, qrStatusOK}},
		setCookies: true,
		redirect:   true,
	}
	server := fixture.start(t)
	defer server.Close()
	ui := &recordingLoginUI{}

	result, err := loginFlow(context.Background(), loginTestOptions(server), ui)
	if err != nil {
		t.Fatalf("loginFlow: %v", err)
	}
	if result.Account != "42" {
		t.Fatalf("account = %q, want 42", result.Account)
	}
	cookie := result.Params["cookie"]
	for _, want := range []string{"__pus=pus", "__kps=kps", "__uid=42"} {
		if !strings.Contains(cookie, want) {
			t.Fatalf("cookie = %q, want %q", cookie, want)
		}
	}
	if strings.Contains(cookie, "other=") {
		t.Fatalf("cookie = %q, must exclude non-quark domains", cookie)
	}
	if len(ui.urls) != 1 {
		t.Fatalf("challenge calls = %d, want 1", len(ui.urls))
	}
	parsed, err := url.Parse(ui.urls[0])
	if err != nil {
		t.Fatalf("parse challenge: %v", err)
	}
	if parsed.Query().Get("token") != "tok-1" || parsed.Query().Get("client_id") != loginQRClientID {
		t.Fatalf("challenge = %q, want token and client id", ui.urls[0])
	}
	if parsed.Query().Get("uc_biz_str") != loginQRBizStr {
		t.Fatalf("uc_biz_str = %q", parsed.Query().Get("uc_biz_str"))
	}
	if fixture.ticketCalls < 2 {
		t.Fatalf("ticket calls = %d, want at least 2", fixture.ticketCalls)
	}
}

func TestQuarkLoginRenewsExpiredChallenge(t *testing.T) {
	fixture := &loginServer{
		tickets: map[string][]int{
			"tok-1": {qrStatusExpired},
			"tok-2": {qrStatusOK},
		},
		setCookies: true,
	}
	server := fixture.start(t)
	defer server.Close()
	ui := &recordingLoginUI{}

	result, err := loginFlow(context.Background(), loginTestOptions(server), ui)
	if err != nil {
		t.Fatalf("loginFlow: %v", err)
	}
	if result.Account != "42" {
		t.Fatalf("account = %q, want 42", result.Account)
	}
	if len(ui.urls) != 2 {
		t.Fatalf("challenge calls = %d, want 2", len(ui.urls))
	}
	if !strings.Contains(ui.urls[0], "tok-1") || !strings.Contains(ui.urls[1], "tok-2") {
		t.Fatalf("challenges = %v, want tok-1 then tok-2", ui.urls)
	}
	if fixture.tokenCalls != 2 {
		t.Fatalf("token calls = %d, want 2", fixture.tokenCalls)
	}
}

func TestQuarkLoginStopsAfterRenewalLimit(t *testing.T) {
	fixture := &loginServer{
		tickets: map[string][]int{
			"tok-1": {qrStatusExpired},
			"tok-2": {qrStatusExpired},
		},
		setCookies: true,
	}
	server := fixture.start(t)
	defer server.Close()
	opts := loginTestOptions(server)
	opts.MaxRenewals = 1
	ui := &recordingLoginUI{}

	_, err := loginFlow(context.Background(), opts, ui)
	if err == nil {
		t.Fatal("loginFlow succeeded, want renewal error")
	}
	if fixture.tokenCalls != 2 {
		t.Fatalf("token calls = %d, want 2", fixture.tokenCalls)
	}
	if len(ui.urls) != 2 {
		t.Fatalf("challenge calls = %d, want 2", len(ui.urls))
	}
}

func TestQuarkLoginCancelled(t *testing.T) {
	fixture := &loginServer{tickets: map[string][]int{"tok-1": {qrStatusWaiting}}}
	server := fixture.start(t)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ui := &recordingLoginUI{cancel: cancel}

	_, err := loginFlow(ctx, loginTestOptions(server), ui)
	if !errors.Is(err, errLoginCancelled) {
		t.Fatalf("err = %v, want cancelled", err)
	}
}

func TestQuarkLoginTimesOut(t *testing.T) {
	fixture := &loginServer{tickets: map[string][]int{"tok-1": {qrStatusWaiting}}}
	server := fixture.start(t)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	opts := loginTestOptions(server)
	opts.PollInterval = 50 * time.Millisecond

	_, err := loginFlow(ctx, opts, &recordingLoginUI{})
	if !errors.Is(err, errLoginTimeout) {
		t.Fatalf("err = %v, want timeout", err)
	}
	if strings.Contains(err.Error(), "tok-1") {
		t.Fatalf("error %q leaks the challenge token", err)
	}
}

func TestQuarkLoginRejectsUnverifiedCookie(t *testing.T) {
	fixture := &loginServer{
		tickets:    map[string][]int{"tok-1": {qrStatusOK}},
		setCookies: true,
		sortStatus: 403,
		sortCode:   31001,
	}
	server := fixture.start(t)
	defer server.Close()

	result, err := loginFlow(context.Background(), loginTestOptions(server), &recordingLoginUI{})
	if err == nil {
		t.Fatal("loginFlow succeeded, want verification error")
	}
	if !strings.Contains(err.Error(), "verify cookie") {
		t.Fatalf("err = %v, want verification context", err)
	}
	if result.Params != nil || result.Account != "" {
		t.Fatalf("result = %+v, want empty on verification failure", result)
	}
	if strings.Contains(err.Error(), "__pus") || strings.Contains(err.Error(), "ticket-1") {
		t.Fatalf("error %q leaks credential material", err)
	}
}

func TestQuarkLoginRequiresCookies(t *testing.T) {
	fixture := &loginServer{
		tickets:    map[string][]int{"tok-1": {qrStatusOK}},
		setCookies: false,
	}
	server := fixture.start(t)
	defer server.Close()

	_, err := loginFlow(context.Background(), loginTestOptions(server), &recordingLoginUI{})
	if err == nil || !strings.Contains(err.Error(), "no quark cookies") {
		t.Fatalf("err = %v, want no-cookie error", err)
	}
}

func TestQuarkLoginProviderAccount(t *testing.T) {
	provider := loginProvider{}
	if account, ok := provider.Account(drive.Params{"cookie": "a=1; __uid=9"}); !ok || account != "9" {
		t.Fatalf("Account = (%q, %v), want (9, true)", account, ok)
	}
	if account, ok := provider.Account(drive.Params{"cookie": "a=1"}); ok || account != "" {
		t.Fatalf("Account = (%q, %v), want unknown", account, ok)
	}
	if _, ok := drive.LoginProviderFor("quark"); !ok {
		t.Fatal("quark login provider is not registered")
	}
}

func TestQuarkCookieDomainRule(t *testing.T) {
	for _, domain := range []string{"quark.cn", ".quark.cn", "pan.quark.cn", ".pan.quark.cn"} {
		if !isQuarkCookieDomain(domain) {
			t.Errorf("isQuarkCookieDomain(%q) = false, want true", domain)
		}
	}
	for _, domain := range []string{"example.com", "notquark.cn", "quark.cn.example.com"} {
		if isQuarkCookieDomain(domain) {
			t.Errorf("isQuarkCookieDomain(%q) = true, want false", domain)
		}
	}
}

func TestRecordingJarHostOnlyCookie(t *testing.T) {
	jar := newRecordingJar()
	hostOnly := &url.URL{Scheme: "https", Host: "pan.quark.cn", Path: "/account/info"}
	jar.SetCookies(hostOnly, []*http.Cookie{
		{Name: "__uid", Value: "7", Path: "/"},
		{Name: "external", Value: "x", Domain: "example.com", Path: "/"},
	})
	header := jar.header()
	if !strings.Contains(header, "__uid=7") {
		t.Fatalf("header = %q, want host-only __uid", header)
	}
	if strings.Contains(header, "external=") {
		t.Fatalf("header = %q, must exclude other domains", header)
	}
}

func TestQuarkLoginProviderFailure(t *testing.T) {
	fixture := &loginServer{tickets: map[string][]int{"tok-1": {qrStatusFailed}}}
	server := fixture.start(t)
	defer server.Close()

	_, err := loginFlow(context.Background(), loginTestOptions(server), &recordingLoginUI{})
	if err == nil || !strings.Contains(err.Error(), "login failure") {
		t.Fatalf("err = %v, want provider login failure", err)
	}
}

func TestQuarkLoginProviderCancellation(t *testing.T) {
	fixture := &loginServer{tickets: map[string][]int{"tok-1": {qrStatusCancelled}}}
	server := fixture.start(t)
	defer server.Close()

	_, err := loginFlow(context.Background(), loginTestOptions(server), &recordingLoginUI{})
	if !errors.Is(err, errLoginCancelled) {
		t.Fatalf("err = %v, want cancellation", err)
	}
}

type failingLoginUI struct{}

func (failingLoginUI) ShowChallenge(challenge string) error {
	return fmt.Errorf("terminal cannot show %s", challenge)
}

func TestQuarkLoginRedactsChallengeFromUIError(t *testing.T) {
	fixture := &loginServer{}
	server := fixture.start(t)
	defer server.Close()

	_, err := loginFlow(context.Background(), loginTestOptions(server), failingLoginUI{})
	if err == nil {
		t.Fatal("loginFlow succeeded, want UI error")
	}
	text := err.Error()
	for _, secret := range []string{"tok-1", "su.quark.cn", "token="} {
		if strings.Contains(text, secret) {
			t.Fatalf("err = %q, leaks %q", text, secret)
		}
	}
	if !strings.Contains(text, "<redacted>") {
		t.Fatalf("err = %q, want a redaction marker", text)
	}
}
