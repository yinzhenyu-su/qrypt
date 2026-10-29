package quark

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const shareTaskID = "0123456789abcdef0123456789abcdef"

func newShareTestDriver(t *testing.T, mux *http.ServeMux) *Driver {
	t.Helper()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return New("cookie", Options{BaseURL: server.URL})
}

func TestQuarkShareLinkLifecycle(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/share", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["url_type"].(float64) != quarkSharePublic {
			t.Errorf("url_type = %v, want public", body["url_type"])
		}
		if body["expired_type"].(float64) != 3 {
			t.Errorf("expired_type = %v, want 3 (7d)", body["expired_type"])
		}
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"task_id":"` + shareTaskID + `","task_sync":true,"task_resp":{"data":{"task_id":"` + shareTaskID + `","task_type":8,"status":2,"share_id":"native1"}}}}`))
	})
	mux.HandleFunc("/share/password", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"title":"t","share_id":"native1","pwd_id":"link00000001","share_url":"https://pan.quark.cn/s/link00000001","url_type":1,"expired_at":4102416000000,"file_num":2}}`))
	})
	mux.HandleFunc("/share/mypage/detail", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"list":[{"title":"t","share_id":"native1","pwd_id":"link00000001","share_url":"https://pan.quark.cn/s/link00000001","url_type":1,"expired_at":4102416000000,"file_num":2,"size":60}]},"metadata":{"_total":1}}`))
	})
	d := newShareTestDriver(t, mux)

	share, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{
		Entries: []drive.Entry{{ID: "f1"}, {ID: "f2"}},
		Title:   "t",
		Expiry:  drive.ShareExpiryWeek,
	})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	if share.LinkID != "link00000001" || share.NativeID != "native1" || !share.Public {
		t.Fatalf("share = %+v", share)
	}
	if !share.ExpiresAt.IsZero() {
		t.Fatalf("permanent link reported expiry %v", share.ExpiresAt)
	}

	page, err := d.ListShares(context.Background(), drive.ShareListRequest{})
	if err != nil {
		t.Fatalf("ListShares: %v", err)
	}
	if len(page.Shares) != 1 || page.Shares[0].EntryCount != 2 {
		t.Fatalf("page = %+v", page)
	}
	if _, err := d.GetShare(context.Background(), "link00000001"); err != nil {
		t.Fatalf("GetShare by link id: %v", err)
	}
}

func TestQuarkShareOpenBrowseSave(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/share/sharepage/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["passcode"] != "abcd" {
			t.Errorf("passcode = %v", body["passcode"])
		}
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"stoken":"tok","url_type":2,"expired_at":1792425599000,"title":"fixture","author":{"nick_name":"n","member_type":"SUPER_VIP"}}}`))
	})
	mux.HandleFunc("/share/sharepage/detail", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("stoken") != "tok" {
			t.Errorf("stoken = %q", r.URL.Query().Get("stoken"))
		}
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"is_owner":0,"share":{},"list":[{"fid":"f1","file_name":"a.bin","pdir_fid":"0","size":10,"share_fid_token":"t1","dir":false}]},"metadata":{"_count":1,"_total":1,"_page":1,"_size":100}}`))
	})
	mux.HandleFunc("/share/sharepage/save", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["to_pdir_fid"] != "dest" || body["pwd_id"] != "link00000001" {
			t.Errorf("save body = %v", body)
		}
		if tokens, _ := body["fid_token_list"].([]any); len(tokens) != 1 || tokens[0] != "t1" {
			t.Errorf("fid_token_list = %v", body["fid_token_list"])
		}
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"task_id":"` + shareTaskID + `","task_sync":true,"task_resp":{"data":{"task_id":"` + shareTaskID + `","task_type":17,"status":2,"save_as":{"save_as_top_fids":["n1"],"to_pdir_fid":"dest","save_as_sum_num":1}}}}}`))
	})
	mux.HandleFunc("/file/info", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"fid":"n1","file_name":"a.bin","size":10,"pdir_fid":"dest"}}`))
	})
	mux.HandleFunc("/file/download", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("User-Agent"), "quark-cloud-drive/") {
			t.Errorf("User-Agent = %q, want the desktop client UA", r.Header.Get("User-Agent"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if tokens, _ := body["fids_token"].([]any); len(tokens) != 1 || tokens[0] != "t1" {
			t.Errorf("fids_token = %v", body["fids_token"])
		}
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":[{"fid":"f1","file_name":"a.bin","size":10,"download_url":"https://dl.test/a"}]}`))
	})
	d := newShareTestDriver(t, mux)

	session, err := d.OpenShare(context.Background(), drive.ShareLink{URL: "https://pan.quark.cn/s/link00000001", LinkID: "link00000001", Passcode: "abcd"})
	if err != nil {
		t.Fatalf("OpenShare: %v", err)
	}
	if session.SessionToken != "tok" || session.Share.Public || session.Share.Owner.Name != "n" {
		t.Fatalf("session = %+v", session)
	}

	page, err := d.ListShareEntries(context.Background(), session, drive.SharePath{ID: "0"}, "")
	if err != nil {
		t.Fatalf("ListShareEntries: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Ticket != "t1" || page.Entries[0].Name != "a.bin" {
		t.Fatalf("entries = %+v", page.Entries)
	}

	outcome, err := d.SaveShareEntries(context.Background(), drive.ShareSaveRequest{
		Session: session,
		Entries: page.Entries,
		Dest:    drive.ShareDestination{Entry: drive.Entry{ID: "dest"}},
	})
	if err != nil {
		t.Fatalf("SaveShareEntries: %v", err)
	}
	if outcome.State != drive.SaveStateSucceeded || len(outcome.Items) != 1 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if item := outcome.Items[0]; item.Saved == nil || item.Saved.ID != "n1" || item.Saved.Name != "a.bin" {
		t.Fatalf("item = %+v", item)
	}

	targets, err := d.ShareDownloadTargets(context.Background(), drive.ShareFetchRequest{Session: session, Entries: page.Entries})
	if err != nil {
		t.Fatalf("ShareDownloadTargets: %v", err)
	}
	if len(targets) != 1 || targets[0].URL != "https://dl.test/a" {
		t.Fatalf("targets = %+v", targets)
	}
	if targets[0].Headers["Cookie"] != "cookie" {
		t.Fatalf("download target cookie = %q, want the requesting cookie", targets[0].Headers["Cookie"])
	}
	if targets[0].Headers["User-Agent"] == "" || targets[0].Headers["Referer"] == "" {
		t.Fatalf("download target is missing browser headers: %+v", targets[0].Headers)
	}
}

func TestQuarkSharePasscodeAndPolicy(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/share/sharepage/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":404,"code":41008,"message":"需要提取码"}`))
	})
	d := newShareTestDriver(t, mux)

	_, err := d.OpenShare(context.Background(), drive.ShareLink{URL: "u", LinkID: "link00000001"})
	if !drive.IsShareErrorCode(err, drive.ShareErrPasscodeRequired) {
		t.Fatalf("error = %v, want share_passcode_required", err)
	}

	policy := d.SharePolicy()
	if policy.Passcode != drive.PolicyOptional || !policy.List || !policy.VisitLimit || !policy.Selection {
		t.Fatalf("policy = %+v", policy)
	}
	if !policy.Title || !policy.Public {
		t.Fatalf("quark lost title/public support: %+v", policy)
	}
	if _, ok := any(d).(drive.ShareLister); !ok {
		t.Fatal("quark must keep account-wide link listing")
	}
	if _, ok := any(d).(drive.ShareSavePoller); !ok {
		t.Fatal("quark must keep asynchronous save polling")
	}
	if policy.SupportsRename(drive.RenameAuto) {
		t.Fatal("quark must not claim auto-rename support")
	}
	if quarkExpiredType(drive.ShareExpiryDay) != 2 || quarkExpiredType(0) != 1 {
		t.Fatal("expiry mapping is wrong")
	}
	if quarkExpiredTime(4102416000000).IsZero() != true || quarkExpiredTime(time.Now().UnixMilli()).IsZero() {
		t.Fatal("permanent sentinel handling is wrong")
	}
}
