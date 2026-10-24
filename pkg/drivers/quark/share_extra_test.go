package quark

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// TestQuarkShareCreatePollsWhenTaskIsQueued covers the task_sync=false path.
func TestQuarkShareCreatePollsWhenTaskIsQueued(t *testing.T) {
	mux := http.NewServeMux()
	status := 1
	mux.HandleFunc("/share", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"task_id":"` + shareTaskID + `","task_sync":false}}`))
	})
	mux.HandleFunc("/task", func(w http.ResponseWriter, r *http.Request) {
		if status == 1 {
			status = 2
			_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"task_id":"` + shareTaskID + `","status":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"task_id":"` + shareTaskID + `","status":2,"share_id":"native1"}}`))
	})
	mux.HandleFunc("/share/password", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"share_id":"native1","pwd_id":"link00000001","share_url":"https://pan.quark.cn/s/link00000001","url_type":2,"passcode":"abcd"}}`))
	})
	d := newShareTestDriver(t, mux)

	share, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{
		Entries:  []drive.Entry{{ID: "f1"}},
		Passcode: "abcd",
	})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	if share.LinkID != "link00000001" || share.Public || share.Passcode != "abcd" {
		t.Fatalf("share = %+v", share)
	}
}

func TestQuarkShareUpdateDeleteAndErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/share/mypage/detail", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"list":[{"share_id":"native1","pwd_id":"link00000001","share_url":"u","url_type":1,"expired_at":4102416000000,"file_num":1}]},"metadata":{"_total":1}}`))
	})
	mux.HandleFunc("/share/password", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"share_id":"native1","pwd_id":"link00000001","share_url":"u","url_type":1}}`))
	})
	mux.HandleFunc("/share/update", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["pwd_id"] != "link00000001" || body["expired_type"].(float64) != 3 {
			t.Errorf("update body = %v", body)
		}
		_, _ = w.Write([]byte(`{"status":200,"code":0}`))
	})
	mux.HandleFunc("/share/delete", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		ids, _ := body["share_ids"].([]any)
		if len(ids) != 1 || ids[0] != "native1" {
			t.Errorf("delete body = %v", body)
		}
		_, _ = w.Write([]byte(`{"status":200,"code":0}`))
	})
	mux.HandleFunc("/share/sharepage/save", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":404,"code":41017,"message":"用户禁止转存自己的分享"}`))
	})
	d := newShareTestDriver(t, mux)

	expiry := drive.ShareExpiryWeek
	if _, err := d.UpdateShare(context.Background(), drive.ShareUpdateRequest{ID: "link00000001", Expiry: &expiry}); err != nil {
		t.Fatalf("UpdateShare: %v", err)
	}
	if err := d.DeleteShare(context.Background(), "link00000001"); err != nil {
		t.Fatalf("DeleteShare: %v", err)
	}
	_, err := d.SaveShareEntries(context.Background(), drive.ShareSaveRequest{
		Session: drive.ShareSession{Link: drive.ShareLink{LinkID: "link00000001"}, SessionToken: "tok"},
		Entries: []drive.ShareEntry{{Entry: drive.Entry{ID: "f1"}, Ticket: "t1"}},
		Dest:    drive.ShareDestination{Entry: drive.Entry{ID: "dest"}},
	})
	if !drive.IsShareErrorCode(err, drive.ShareErrOwnSave) {
		t.Fatalf("error = %v, want share_own_save", err)
	}
}

// TestQuarkShareListingPagesAndSubdirs covers page cursors and pdir navigation.
func TestQuarkShareListingPagesAndSubdirs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/share/sharepage/detail", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("pdir_fid") == "d1" {
			_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"share":{},"list":[{"fid":"f2","file_name":"b.bin","pdir_fid":"d1","size":5,"share_fid_token":"t2"}]},"metadata":{"_count":1,"_total":1,"_page":1,"_size":100}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"share":{},"list":[{"fid":"d1","file_name":"dir","pdir_fid":"0","size":0,"dir":true,"share_fid_token":"t0"}]},"metadata":{"_count":1,"_total":150,"_page":1,"_size":100}}`))
	})
	d := newShareTestDriver(t, mux)
	session := drive.ShareSession{Link: drive.ShareLink{LinkID: "link00000001"}, SessionToken: "tok"}

	page, err := d.ListShareEntries(context.Background(), session, drive.SharePath{ID: "0"}, "")
	if err != nil {
		t.Fatalf("ListShareEntries: %v", err)
	}
	if page.Next != "2" {
		t.Fatalf("next = %q, want 2", page.Next)
	}
	if len(page.Entries) != 1 || !page.Entries[0].IsDir {
		t.Fatalf("entries = %+v", page.Entries)
	}
	sub, err := d.ListShareEntries(context.Background(), session, drive.SharePath{ID: "d1"}, "")
	if err != nil {
		t.Fatalf("subdir: %v", err)
	}
	if len(sub.Entries) != 1 || sub.Entries[0].Ticket != "t2" || sub.Entries[0].ParentID != "d1" {
		t.Fatalf("subdir entries = %+v", sub.Entries)
	}
}

// TestQuarkShareCreateTimesOut pins the bounded-poll branch: a provider that
// keeps the task queued must surface share_timeout instead of hanging.
func TestQuarkShareCreateTimesOut(t *testing.T) {
	mux := http.NewServeMux()
	polls := 0
	mux.HandleFunc("/share", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"task_id":"` + shareTaskID + `","task_sync":false}}`))
	})
	mux.HandleFunc("/task", func(w http.ResponseWriter, r *http.Request) {
		polls++
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"task_id":"` + shareTaskID + `","status":1}}`))
	})
	d := newShareTestDriver(t, mux)
	d.sharePollTimeout = 120 * time.Millisecond
	d.sharePollInterval = 10 * time.Millisecond

	start := time.Now()
	_, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{Entries: []drive.Entry{{ID: "f1"}}})
	if !drive.IsShareErrorCode(err, drive.ShareErrTimeout) {
		t.Fatalf("error = %v, want share_timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout took %s, want the bounded window", elapsed)
	}
	if polls < 2 {
		t.Fatalf("polls = %d, want repeated polling", polls)
	}
}

// TestQuarkShareSaveAsyncKeepsEveryEntry pins the asynchronous partial-save
// shape: Quark reports only successful destination FIDs, so GetShareSave must
// still return one result per requested entry.
func TestQuarkShareSaveAsyncKeepsEveryEntry(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/share/sharepage/save", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"task_id":"` + shareTaskID + `","task_sync":false,"task_resp":{"data":{"task_id":"` + shareTaskID + `","task_type":17,"status":1},"id":"` + shareTaskID + `"}}}`))
	})
	mux.HandleFunc("/task", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"task_id":"` + shareTaskID + `","status":2,"save_as":{"save_as_top_fids":["n1"],"to_pdir_fid":"dest","save_as_sum_num":1}}}`))
	})
	mux.HandleFunc("/file/info", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"fid":"n1","file_name":"a.bin","size":10,"pdir_fid":"dest"}}`))
	})
	d := newShareTestDriver(t, mux)

	entries := []drive.ShareEntry{
		{Entry: drive.Entry{ID: "f1", Name: "a.bin", Size: 10}, Ticket: "t1"},
		{Entry: drive.Entry{ID: "f2", Name: "b.bin", Size: 20}, Ticket: "t2"},
	}
	outcome, err := d.SaveShareEntries(context.Background(), drive.ShareSaveRequest{
		Session: drive.ShareSession{Link: drive.ShareLink{LinkID: "link00000001"}, SessionToken: "tok"},
		Entries: entries,
		Dest:    drive.ShareDestination{Entry: drive.Entry{ID: "dest"}},
	})
	if err != nil {
		t.Fatalf("SaveShareEntries: %v", err)
	}
	if outcome.State != drive.SaveStateRunning || outcome.Handle == "" {
		t.Fatalf("initial outcome = %+v", outcome)
	}

	done, err := d.GetShareSave(context.Background(), outcome.Handle, nil)
	if err != nil {
		t.Fatalf("GetShareSave: %v", err)
	}
	if done.State != drive.SaveStatePartial {
		t.Fatalf("state = %s, want partial", done.State)
	}
	if len(done.Items) != 2 {
		t.Fatalf("items = %+v, want one per requested entry", done.Items)
	}
	if done.Items[0].Saved == nil || done.Items[0].Saved.ID != "n1" {
		t.Fatalf("saved item = %+v", done.Items[0])
	}
	if done.Items[1].Error == nil || done.Items[1].SourceID != "f2" {
		t.Fatalf("missing entry did not become a failure: %+v", done.Items[1])
	}
}

// TestQuarkShareSaveRestartUsesPassedEntries pins restart recovery: after the
// driver's in-memory binding is gone, Core passes the same selection again and
// the task response (which lists only successes) still yields one result per
// requested entry.
func TestQuarkShareSaveRestartUsesPassedEntries(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/task", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"task_id":"` + shareTaskID + `","status":2,"save_as":{"save_as_top_fids":["n1"],"to_pdir_fid":"dest","save_as_sum_num":1}}}`))
	})
	mux.HandleFunc("/file/info", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"fid":"n1","file_name":"a.bin","size":10,"pdir_fid":"dest"}}`))
	})
	d := newShareTestDriver(t, mux)

	entries := []drive.ShareEntry{
		{Entry: drive.Entry{ID: "f1", Name: "a.bin", Size: 10}, Ticket: "t1"},
		{Entry: drive.Entry{ID: "f2", Name: "b.bin", Size: 20}, Ticket: "t2"},
	}
	done, err := d.GetShareSave(context.Background(), shareTaskID, entries)
	if err != nil {
		t.Fatalf("GetShareSave: %v", err)
	}
	if done.State != drive.SaveStatePartial {
		t.Fatalf("state = %s, want partial", done.State)
	}
	if len(done.Items) != 2 {
		t.Fatalf("items = %+v, want one per selected entry", done.Items)
	}
	if done.Items[1].SourceID != "f2" || done.Items[1].Error == nil {
		t.Fatalf("missing entry did not become a failure: %+v", done.Items[1])
	}
}

// TestQuarkShareSavePollErrorClassification pins the resubmit safety rule:
// only an explicit provider task-missing code means the handle is gone.
// Temporary API failures and unparseable responses stay provider errors.
func TestQuarkShareSavePollErrorClassification(t *testing.T) {
	cases := []struct {
		name string
		body string
		want drive.ShareErrorCode
	}{
		{
			name: "temporary api error",
			body: `{"status":500,"code":500,"message":"temporary"}`,
			want: drive.ShareErrProvider,
		},
		{
			name: "malformed response",
			body: `not-json`,
			want: drive.ShareErrProvider,
		},
		{
			name: "task missing code",
			body: `{"status":404,"code":14013,"message":"task not found"}`,
			want: drive.ShareErrNotFound,
		},
		{
			name: "generic not found code",
			body: `{"status":404,"code":23001,"message":"not found"}`,
			want: drive.ShareErrNotFound,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/task", func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			})
			d := newShareTestDriver(t, mux)
			_, err := d.GetShareSave(context.Background(), shareTaskID, []drive.ShareEntry{{Entry: drive.Entry{ID: "f1", Name: "a.bin"}}})
			if err == nil {
				t.Fatalf("GetShareSave succeeded, want %s", tc.want)
			}
			if !drive.IsShareErrorCode(err, tc.want) {
				t.Fatalf("error = %v, want %s", err, tc.want)
			}
		})
	}
}
