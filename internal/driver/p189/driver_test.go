package p189

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type countingMD5Source struct {
	data  []byte
	sum   [md5.Size]byte
	opens int
}

func newCountingMD5Source(data []byte) *countingMD5Source {
	copied := append([]byte(nil), data...)
	return &countingMD5Source{data: copied, sum: md5.Sum(copied)}
}

func (s *countingMD5Source) Size() int64 {
	return int64(len(s.data))
}

func (s *countingMD5Source) Open(context.Context) (drive.ReadOnlyFile, error) {
	s.opens++
	return countingReadOnlyFile{Reader: bytes.NewReader(s.data)}, nil
}

func (s *countingMD5Source) Hash(algorithm drive.HashAlgorithm) ([]byte, bool) {
	if algorithm != drive.HashMD5 {
		return nil, false
	}
	return s.sum[:], true
}

type countingReadOnlyFile struct {
	*bytes.Reader
}

func (countingReadOnlyFile) Close() error {
	return nil
}

func TestSourceMD5HexUsesSourceMetadata(t *testing.T) {
	source := newCountingMD5Source([]byte("data"))
	got, err := sourceMD5Hex(context.Background(), source, source.Size())
	if err != nil {
		t.Fatal(err)
	}
	if source.opens != 0 {
		t.Fatalf("source opened %d times, want 0", source.opens)
	}
	if want := "8D777F385D3DFEC8815D20F7496026DC"; got != want {
		t.Fatalf("md5 = %s, want %s", got, want)
	}
}

func TestInstallBandwidthLimiter(t *testing.T) {
	drv := &Driver{}
	handled := drv.InstallBandwidthLimiter(drive.NewBandwidthLimiter(drive.BandwidthLimits{
		DownloadBytesPerSecond: 1,
		UploadBytesPerSecond:   1,
	}))
	if handled != drive.BandwidthLimitDownload|drive.BandwidthLimitUpload {
		t.Fatalf("handled directions = %v, want download|upload", handled)
	}
	if drv.limiter == nil {
		t.Fatal("limiter was not installed")
	}
}

func TestResolvePathRootUsesConfiguredRootID(t *testing.T) {
	drv := &Driver{rootID: -11}
	got, err := drv.ResolvePath(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "-11" {
		t.Fatalf("root id = %q, want -11", got)
	}
}

func TestRetryOnAuthErrorSkipsPasswordReloginDuringCooldown(t *testing.T) {
	cl := &client{
		username:                "user",
		password:                "pass",
		passwordReloginFailedAt: time.Now(),
		passwordReloginError:    "login limited",
	}
	calls := 0
	err := cl.retryOnAuthError(context.Background(), func(context.Context) error {
		calls++
		return fmt.Errorf("189: GET https://example.invalid: 400 Bad Request")
	})
	if err == nil {
		t.Fatal("expected cooldown error")
	}
	if calls != 1 {
		t.Fatalf("fn calls = %d, want 1", calls)
	}
	if !strings.Contains(err.Error(), "password re-login skipped") || !strings.Contains(err.Error(), "login limited") {
		t.Fatalf("error = %v, want cooldown context", err)
	}
}

func TestCookieUpdatePersistsState(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	driver := &Driver{
		cl:           newClient("old=1", "", ""),
		cookieSource: "config",
	}
	driver.cl.onCookieUpdate = driver.saveUpdatedCookie
	driver.InstallStateStore(store)
	driver.cl.captureCookies(&http.Response{
		Header: http.Header{"Set-Cookie": []string{"COOKIE_LOGIN_USER=new; Path=/"}},
	})

	var state cookieState
	if err := store.LoadJSON("189_cookie.json", &state); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state.Cookie, "COOKIE_LOGIN_USER=new") {
		t.Fatalf("cookie state = %q, want COOKIE_LOGIN_USER=new", state.Cookie)
	}
	if driver.cookieSource != "response" {
		t.Fatalf("cookieSource = %q, want response", driver.cookieSource)
	}
}

func TestCookieUpdatePreservesExistingCookieKeys(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	driver := &Driver{
		cl:           newClient("apm_key=old; JSESSIONID=old; COOKIE_LOGIN_USER=old-user", "", ""),
		cookieSource: "config",
	}
	driver.cl.onCookieUpdate = driver.saveUpdatedCookie
	driver.InstallStateStore(store)
	driver.cl.captureCookies(&http.Response{
		Header: http.Header{"Set-Cookie": []string{"JSESSIONID=new; Path=/"}},
	})

	var state cookieState
	if err := store.LoadJSON("189_cookie.json", &state); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state.Cookie, "apm_key=old") || !strings.Contains(state.Cookie, "COOKIE_LOGIN_USER=old-user") || !strings.Contains(state.Cookie, "JSESSIONID=new") {
		t.Fatalf("cookie state = %q, want updated JSESSIONID with existing keys preserved", state.Cookie)
	}
}

func TestLoadCookieStateMergesWithConfigCookie(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("189_cookie.json", cookieState{
		Cookie:    "JSESSIONID=stored",
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	driver := &Driver{
		cl:           newClient("config=1; JSESSIONID=config; COOKIE_LOGIN_USER=config-user", "", ""),
		cookieSource: "config",
	}
	driver.InstallStateStore(store)
	driver.loadCookieState()
	got := driver.cl.cookieValue()
	if !strings.Contains(got, "config=1") || !strings.Contains(got, "COOKIE_LOGIN_USER=config-user") || !strings.Contains(got, "JSESSIONID=stored") {
		t.Fatalf("cookie = %q, want merged config and state cookie", got)
	}
	if driver.cookieSource != "state" {
		t.Fatalf("cookieSource = %q, want state", driver.cookieSource)
	}
}

func TestPasswordReloginCooldownPersistsState(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	driver := &Driver{
		cl:           newClient("old=1", "user", "pass"),
		cookieSource: "config",
	}
	driver.cl.onPasswordReloginState = driver.savePasswordReloginState
	driver.InstallStateStore(store)
	driver.cl.rememberPasswordReloginFailure(fmt.Errorf("login limited"))

	var state cookieState
	if err := store.LoadJSON("189_cookie.json", &state); err != nil {
		t.Fatal(err)
	}
	if state.PasswordReloginFailedAt.IsZero() {
		t.Fatal("expected persisted password relogin failure time")
	}
	if state.PasswordReloginError != "login limited" {
		t.Fatalf("password relogin error = %q, want login limited", state.PasswordReloginError)
	}

	reloaded := &Driver{
		cl:           newClient("old=1", "user", "pass"),
		cookieSource: "config",
	}
	reloaded.InstallStateStore(store)
	reloaded.loadCookieState()
	calls := 0
	err := reloaded.cl.retryOnAuthError(context.Background(), func(context.Context) error {
		calls++
		return fmt.Errorf("189: GET https://example.invalid: 400 Bad Request")
	})
	if err == nil {
		t.Fatal("expected cooldown error")
	}
	if calls != 1 {
		t.Fatalf("fn calls = %d, want 1", calls)
	}
	if !strings.Contains(err.Error(), "password re-login skipped") || !strings.Contains(err.Error(), "login limited") {
		t.Fatalf("error = %v, want persisted cooldown context", err)
	}
}

func TestPasswordReloginFailureRestoresPreviousCookieBeforePersist(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	driver := &Driver{
		cl:           newClient("apm_key=old; JSESSIONID=old; COOKIE_LOGIN_USER=old-user", "user", "pass"),
		cookieSource: "config",
	}
	driver.cl.onPasswordReloginState = driver.savePasswordReloginState
	driver.InstallStateStore(store)

	previous := driver.cl.cookieValue()
	driver.cl.clearCookie()
	driver.cl.mergeCookieHeader("JSESSIONID=partial")
	driver.cl.restoreCookieAfterReloginFailure(previous)
	driver.cl.rememberPasswordReloginFailure(fmt.Errorf("login limited"))

	var state cookieState
	if err := store.LoadJSON("189_cookie.json", &state); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state.Cookie, "apm_key=old") || !strings.Contains(state.Cookie, "COOKIE_LOGIN_USER=old-user") || !strings.Contains(state.Cookie, "JSESSIONID=old") {
		t.Fatalf("cookie state = %q, want previous full cookie preserved", state.Cookie)
	}
}

func TestSourceSliceMD5HexReadsOnlySlice(t *testing.T) {
	source := newCountingMD5Source(bytes.Repeat([]byte("x"), sliceMD5Size+10))
	got, err := sourceSliceMD5Hex(context.Background(), source, source.Size())
	if err != nil {
		t.Fatal(err)
	}
	if source.opens != 1 {
		t.Fatalf("source opened %d times, want 1", source.opens)
	}
	wantSum := md5.Sum(bytes.Repeat([]byte("x"), sliceMD5Size))
	if want := stringUpperHex(wantSum[:]); got != want {
		t.Fatalf("slice md5 = %s, want %s", got, want)
	}
}

func TestSourceSliceMD5HexSmallFileUsesSourceMetadata(t *testing.T) {
	source := newCountingMD5Source([]byte("small"))
	got, err := sourceSliceMD5Hex(context.Background(), source, source.Size())
	if err != nil {
		t.Fatal(err)
	}
	if source.opens != 0 {
		t.Fatalf("source opened %d times, want 0", source.opens)
	}
	if want := "EB5C1399A871211C7E7ED732D15E3A8B"; got != want {
		t.Fatalf("slice md5 = %s, want %s", got, want)
	}
}

func TestUploadSliceMD5UsesFileMD5ThroughTenMB(t *testing.T) {
	fileMD5 := "0123456789ABCDEF0123456789ABCDEF"
	if got := uploadSliceMD5(fileMD5, "different", int64(uploadPartSize)); got != fileMD5 {
		t.Fatalf("slice md5 = %s, want file md5 for <=10MB", got)
	}
	if got := uploadSliceMD5(fileMD5, "different", int64(uploadPartSize)+1); got != "different" {
		t.Fatalf("slice md5 = %s, want computed slice md5 above 10MB", got)
	}
}

func TestNonRetryableUploadError(t *testing.T) {
	if !nonRetryableUploadError(http.StatusForbidden, []byte(`{"code":"SliceMd5DoesNotMatch"}`)) {
		t.Fatal("SliceMd5DoesNotMatch should be non-retryable")
	}
	if !nonRetryableUploadError(http.StatusForbidden, []byte(`{"code":"InvalidPartSize"}`)) {
		t.Fatal("InvalidPartSize should be non-retryable")
	}
	if nonRetryableUploadError(http.StatusServiceUnavailable, []byte(`{"code":"SliceMd5DoesNotMatch"}`)) {
		t.Fatal("5xx should remain retryable")
	}
}

func TestBatchTaskInfosIncludesEscapedFileName(t *testing.T) {
	got, err := batchTaskInfos(batchTaskInfo{
		FileID:   123,
		FileName: `未命名 "文件夹"`,
		IsFolder: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"fileId":123,"fileName":"未命名 \"文件夹\"","isFolder":1}]`
	if got != want {
		t.Fatalf("taskInfos = %s, want %s", got, want)
	}
}

func TestBatchTaskResponseError(t *testing.T) {
	if err := batchTaskResponseError("DELETE", BatchTaskResp{ResCode: 0}); err != nil {
		t.Fatalf("success response returned error: %v", err)
	}
	err := batchTaskResponseError("DELETE", BatchTaskResp{ResCode: 123, ResMessage: "删除失败"})
	if err == nil || !strings.Contains(err.Error(), "删除失败") {
		t.Fatalf("error = %v, want response message", err)
	}
}

func TestBatchTaskTraceResponse(t *testing.T) {
	got := batchTaskTraceResponse("DELETE", BatchTaskResp{
		ResCode:    123,
		ResMessage: "删除失败",
		TaskID:     "task-1",
	})
	if got["task_type"] != "DELETE" || got["res_code"] != 123 || got["res_message"] != "删除失败" || got["task_id"] != "task-1" {
		t.Fatalf("trace response = %#v", got)
	}
}

func TestSourceMD5HexFallbackStreamsSource(t *testing.T) {
	source := plainReadOnlySource{data: []byte("fallback")}
	got, err := sourceMD5Hex(context.Background(), source, source.Size())
	if err != nil {
		t.Fatal(err)
	}
	if want := "4CCB1142EBDD7CA505D88C28DF648283"; got != want {
		t.Fatalf("md5 = %s, want %s", got, want)
	}
}

var _ drive.ReadOnlyFile = countingReadOnlyFile{}
var _ io.Reader = countingReadOnlyFile{}

type plainReadOnlySource struct {
	data []byte
}

func (s plainReadOnlySource) Size() int64 {
	return int64(len(s.data))
}

func (s plainReadOnlySource) Open(context.Context) (drive.ReadOnlyFile, error) {
	return countingReadOnlyFile{Reader: bytes.NewReader(s.data)}, nil
}

func stringUpperHex(data []byte) string {
	const digits = "0123456789ABCDEF"
	out := make([]byte, len(data)*2)
	for i, b := range data {
		out[i*2] = digits[b>>4]
		out[i*2+1] = digits[b&0x0f]
	}
	return string(out)
}
