package p189

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	apiBase         = "https://cloud.189.cn"
	uploadBase      = "https://upload.cloud.189.cn"
	loginURL        = apiBase + "/api/portal/loginUrl.action?redirectURL=https%3A%2F%2Fcloud.189.cn%2Fmain.action"
	listURL         = apiBase + "/api/open/file/listFiles.action"
	downloadInfoURL = apiBase + "/api/portal/getFileInfo.action"
	mkdirURL        = apiBase + "/api/open/file/createFolder.action"
	renameFileURL   = apiBase + "/api/open/file/renameFile.action"
	renameFolderURL = apiBase + "/api/open/file/renameFolder.action"
	batchTaskURL    = apiBase + "/api/open/batch/createBatchTask.action"
	uploadInitURL   = uploadBase + "/person/initMultiUpload"
	uploadDataURL   = uploadBase + "/person/uploadData"
	uploadCommitURL = uploadBase + "/person/commitMultiUpload"
	spaceURL        = apiBase + "/api/portal/getUserSizeInfo.action"
)

type client struct {
	hc         *http.Client
	cookie     string
	username   string
	password   string
	sessionKey string
	pubKey     string
	pkID       string
	keyExpire  int64
}

func newClient(cookie, username, password string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{
		hc:       &http.Client{Jar: jar},
		cookie:   cookie,
		username: username,
		password: password,
	}
}

func (c *client) isLoggedIn() bool {
	if c.cookie != "" {
		return true
	}
	return c.username != "" && c.password != ""
}

func (c *client) loginInit(ctx context.Context) error {
	if c.username != "" {
		return c.loginWithPassword(ctx)
	}
	if c.cookie != "" {
		return c.loginWithCookie(ctx)
	}
	return fmt.Errorf("189: no credentials")
}

func (c *client) loginWithCookie(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://cloud.189.cn/")
	req.Header.Set("Cookie", c.cookie)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("189: cookie login: %s", resp.Status)
	}
	c.extractSessionKey()
	return nil
}

func (c *client) loginWithPassword(ctx context.Context) error {
	req1, err := http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
	if err != nil {
		return err
	}
	req1.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req1.Header.Set("Referer", "https://cloud.189.cn/")
	resp1, err := c.hc.Do(req1)
	if err != nil {
		return err
	}
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		return fmt.Errorf("189: login page: %s", resp1.Status)
	}

	redirectURL := resp1.Request.URL.String()
	if redirectURL == "https://cloud.189.cn/web/main" {
		return nil
	}
	lt := resp1.Request.URL.Query().Get("lt")
	reqID := resp1.Request.URL.Query().Get("reqId")
	appID := resp1.Request.URL.Query().Get("appId")

	loginHeaders := map[string]string{
		"lt":      lt,
		"reqid":   reqID,
		"referer": redirectURL,
		"origin":  "https://open.e.189.cn",
	}

	var appConf struct {
		Result string `json:"result"`
		Msg    string `json:"msg"`
		Data   struct {
			AccountType string `json:"accountType"`
			AppKey      string `json:"appKey"`
			ClientType  int    `json:"clientType"`
			IsOauth2    bool   `json:"isOauth2"`
			ReturnUrl   string `json:"returnUrl"`
			MailSuffix  string `json:"mailSuffix"`
			ParamId     string `json:"paramId"`
		} `json:"data"`
	}
	err = c.doPostWithHeaders(ctx, "https://open.e.189.cn/api/logbox/oauth2/appConf.do", loginHeaders, map[string]string{
		"version": "2.0",
		"appKey":  appID,
	}, &appConf)
	if err != nil {
		return fmt.Errorf("189: app conf: %w", err)
	}
	if appConf.Result != "0" {
		return fmt.Errorf("189: app conf: %s", appConf.Msg)
	}

	var encConf struct {
		Result int `json:"result"`
		Data   struct {
			Pre       string `json:"pre"`
			PreDomain string `json:"preDomain"`
			PubKey    string `json:"pubKey"`
		} `json:"data"`
	}
	err = c.doPostWithHeaders(ctx, "https://open.e.189.cn/api/logbox/config/encryptConf.do", loginHeaders, map[string]string{
		"appId": appID,
	}, &encConf)
	if err != nil {
		return fmt.Errorf("189: encrypt conf: %w", err)
	}
	if encConf.Result != 0 {
		return fmt.Errorf("189: encrypt conf failed")
	}

	encUser := encConf.Data.Pre + rsaEncode([]byte(c.username), encConf.Data.PubKey, true)
	encPass := encConf.Data.Pre + rsaEncode([]byte(c.password), encConf.Data.PubKey, true)

	var loginResp struct {
		Result int    `json:"result"`
		Msg    string `json:"msg"`
	}
	err = c.doPostWithHeaders(ctx, "https://open.e.189.cn/api/logbox/oauth2/loginSubmit.do", loginHeaders, map[string]string{
		"version":         "v2.0",
		"apToken":         "",
		"appKey":          appID,
		"accountType":     appConf.Data.AccountType,
		"userName":        encUser,
		"epd":             encPass,
		"captchaType":     "",
		"validateCode":    "",
		"smsValidateCode": "",
		"captchaToken":    "",
		"returnUrl":       appConf.Data.ReturnUrl,
		"mailSuffix":      appConf.Data.MailSuffix,
		"dynamicCheck":    "FALSE",
		"clientType":      strconv.Itoa(appConf.Data.ClientType),
		"cb_SaveName":     "3",
		"isOauth2":        strconv.FormatBool(appConf.Data.IsOauth2),
		"state":           "",
		"paramId":         appConf.Data.ParamId,
	}, &loginResp)
	if err != nil {
		return fmt.Errorf("189: login submit: %w", err)
	}
	if loginResp.Result != 0 {
		return fmt.Errorf("189: login failed: %s", loginResp.Msg)
	}

	mainReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://cloud.189.cn/web/main", nil)
	if err != nil {
		return err
	}
	mainReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	mainReq.Header.Set("Referer", "https://cloud.189.cn/")
	mainResp, err := c.hc.Do(mainReq)
	if err != nil {
		return fmt.Errorf("189: main page: %w", err)
	}
	mainResp.Body.Close()
	return nil
}

func (c *client) getSessionKey(ctx context.Context) error {
	var resp struct {
		SessionKey string `json:"sessionKey"`
	}
	err := c.doGet(ctx, "https://cloud.189.cn/v2/getUserBriefInfo.action", nil, &resp)
	if err != nil {
		return err
	}
	if resp.SessionKey == "" {
		return fmt.Errorf("189: empty session key")
	}
	c.sessionKey = resp.SessionKey
	return nil
}

func (c *client) extractSessionKey() {
	u, _ := url.Parse("https://cloud.189.cn")
	if u == nil {
		return
	}
	for _, cookie := range c.hc.Jar.Cookies(u) {
		if cookie.Name == "COOKIE_LOGIN_SESSION" || cookie.Name == "JSESSIONID" || cookie.Name == "SESSION" {
			c.sessionKey = cookie.Value
			return
		}
	}
}

func (c *client) getResKey(ctx context.Context) (string, string, error) {
	now := time.Now().UnixMilli()
	if c.keyExpire > now && c.pubKey != "" && c.pkID != "" {
		return c.pubKey, c.pkID, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://cloud.189.cn/api/security/generateRsaKey.action", nil)
	if err != nil {
		return "", "", err
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://cloud.189.cn/")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("189: get res key: %s", resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", "", fmt.Errorf("189: empty res key response")
	}
	if trimmed[0] == '<' {
		var xmlResp struct {
			XMLName xml.Name `xml:"keyPair"`
			Result  int    `xml:"result"`
			PubKey  string `xml:"pubKey"`
			PkID    string `xml:"pkId"`
			Expire  int64  `xml:"expire"`
		}
		if err := xml.Unmarshal(trimmed, &xmlResp); err != nil {
			return "", "", fmt.Errorf("189: res key xml decode: %w", err)
		}
		if xmlResp.Result != 0 {
			return "", "", fmt.Errorf("189: res key failed")
		}
		c.pubKey = xmlResp.PubKey
		c.pkID = xmlResp.PkID
		c.keyExpire = xmlResp.Expire
	} else {
		var jsonResp struct {
			Result int    `json:"result"`
			PubKey string `json:"pubKey"`
			PkID   string `json:"pkId"`
			Expire int64  `json:"expire"`
		}
		if err := json.Unmarshal(trimmed, &jsonResp); err != nil {
			return "", "", fmt.Errorf("189: res key json decode: %w", err)
		}
		c.pubKey = jsonResp.PubKey
		c.pkID = jsonResp.PkID
		c.keyExpire = jsonResp.Expire
	}
	return c.pubKey, c.pkID, nil
}

func (c *client) uploadRequest(ctx context.Context, uri string, form map[string]string) ([]byte, error) {
	if c.sessionKey == "" {
		if err := c.getSessionKey(ctx); err != nil {
			return nil, fmt.Errorf("189: get session key: %w", err)
		}
	}
	pubKey, pkID, err := c.getResKey(ctx)
	if err != nil {
		return nil, err
	}
	headers, params, err := generateUploadHeaders(c.sessionKey, uri, form, pubKey, pkID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://upload.cloud.189.cn"+uri+"?params="+params, nil)
	if err != nil {
		return nil, err
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://cloud.189.cn/")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("189: upload request %s: %s", uri, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func (c *client) doPostRaw(ctx context.Context, u string, form map[string]string) ([]byte, error) {
	vals := url.Values{}
	for k, v := range form {
		vals.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(vals.Encode()))
	if err != nil {
		return nil, err
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://cloud.189.cn/")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("189: %s %s: %s", req.Method, req.URL, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func (c *client) doPostWithHeaders(ctx context.Context, u string, headers map[string]string, form map[string]string, result any) error {
	vals := url.Values{}
	for k, v := range form {
		vals.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(vals.Encode()))
	if err != nil {
		return err
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if lt, ok := headers["lt"]; ok {
		req.Header.Set("lt", lt)
	}
	if reqid, ok := headers["reqid"]; ok {
		req.Header.Set("reqid", reqid)
	}
	if referer, ok := headers["referer"]; ok {
		req.Header.Set("Referer", referer)
	}
	if origin, ok := headers["origin"]; ok {
		req.Header.Set("Origin", origin)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("189: %s: %s", u, resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if result != nil {
		if err := json.Unmarshal(raw, result); err != nil {
			return fmt.Errorf("189: decode: %w", err)
		}
	}
	return nil
}

func (c *client) doGet(ctx context.Context, u string, query map[string]string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://cloud.189.cn/")
	q := req.URL.Query()
	q.Set("noCache", noCache())
	for k, v := range query {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()
	return c.doReq(req, result)
}

func (c *client) doPost(ctx context.Context, u string, form map[string]string, result any) error {
	vals := url.Values{}
	for k, v := range form {
		vals.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(vals.Encode()))
	if err != nil {
		return err
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://cloud.189.cn/")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	q := req.URL.Query()
	q.Set("noCache", noCache())
	req.URL.RawQuery = q.Encode()
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("189: %s %s: %s", req.Method, req.URL, resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("189: read response: %w", err)
	}
	if len(raw) == 0 || result == nil {
		return nil
	}
	trimmed := bytes.TrimSpace(raw)
	if trimmed[0] == '<' {
		return xml.Unmarshal(trimmed, result)
	}
	return json.Unmarshal(trimmed, result)
}

func (c *client) doReq(req *http.Request, result any) error {
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("189: %s %s: %s", req.Method, req.URL, resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("189: read response: %w", err)
	}
	if len(raw) == 0 {
		return nil
	}
	if result != nil {
		if err := json.Unmarshal(raw, result); err != nil {
			return fmt.Errorf("189: decode response from %s: %w (body: %s)", req.URL, err, string(raw[:min(len(raw), 200)]))
		}
	}
	return nil
}

func (c *client) retryOnAuthError(ctx context.Context, fn func(context.Context) error) error {
	err := fn(ctx)
	if err != nil && c.username != "" && isAuthError(err) {
		c.cookie = ""
		if loginErr := c.loginWithPassword(ctx); loginErr == nil {
			if keyErr := c.getSessionKey(ctx); keyErr == nil {
				err = fn(ctx)
			}
		}
	}
	return err
}

func isAuthError(err error) bool {
	s := err.Error()
	return strings.Contains(s, "400") || strings.Contains(s, "InvalidSessionKey") || strings.Contains(s, "401") || strings.Contains(s, "403")
}

func (c *client) listFiles(ctx context.Context, folderID int64) (folders []Folder, files []File, err error) {
	err = c.retryOnAuthError(ctx, func(ctx context.Context) error {
		body, e := c.doPostRaw(ctx, listURL, map[string]string{
			"pageSize":   "9999",
			"pageNum":    "1",
			"mediaType":  "0",
			"folderId":   strconv.FormatInt(folderID, 10),
			"iconOption": "5",
			"orderBy":    "lastOpTime",
			"descending": "true",
		})
		if e != nil {
			return e
		}
		trimmed := bytes.TrimSpace(body)
		if len(trimmed) == 0 {
			return nil
		}
		if trimmed[0] == '<' {
			var xmlResp xmlListFiles
			if e := xml.Unmarshal(trimmed, &xmlResp); e != nil {
				return fmt.Errorf("189: list xml decode: %w", e)
			}
			folders = make([]Folder, 0, len(xmlResp.FileList.Folders))
			for _, f := range xmlResp.FileList.Folders {
				folders = append(folders, Folder{
					ID:   f.ID,
					Name: f.Name,
					LastOpTime: f.CreateDate,
				})
			}
			files = make([]File, 0, len(xmlResp.FileList.Files))
			for _, f := range xmlResp.FileList.Files {
				files = append(files, File{
					ID:   f.ID,
					Name: f.Name,
					Size: f.Size,
					LastOpTime: f.LastOpTime,
				})
			}
			return nil
		}
		var resp ListResp
		if e := json.Unmarshal(trimmed, &resp); e != nil {
			return fmt.Errorf("189: list json decode: %w", e)
		}
		if resp.ResCode != 0 {
			return fmt.Errorf("189: list: %s", resp.ResMessage)
		}
		folders = resp.FileListAO.FolderList
		files = resp.FileListAO.FileList
		return nil
	})
	return
}

func (c *client) withRetry(ctx context.Context, fn func(context.Context) error) error {
	return c.retryOnAuthError(ctx, fn)
}

func (c *client) getDownloadURL(ctx context.Context, fileID int64) (downloadURL string, err error) {
	err = c.retryOnAuthError(ctx, func(ctx context.Context) error {
		var resp DownResp
		if e := c.doGet(ctx, downloadInfoURL, map[string]string{
			"fileId": strconv.FormatInt(fileID, 10),
		}, &resp); e != nil {
			return e
		}
		if resp.ResCode != 0 {
			return fmt.Errorf("189: get download url: %s", resp.ResMessage)
		}
		downloadURL = resp.FileDownloadURL
		return nil
	})
	return
}



func (c *client) createFolder(ctx context.Context, parentID int64, name string) (id int64, err error) {
	err = c.retryOnAuthError(ctx, func(ctx context.Context) error {
		var resp MkdirResp
		if e := c.doPost(ctx, mkdirURL, map[string]string{
			"parentFolderId": strconv.FormatInt(parentID, 10),
			"folderName":     name,
		}, &resp); e != nil {
			return e
		}
		id = resp.ID
		return nil
	})
	return
}

func (c *client) rename(ctx context.Context, fileID int64, name string, isDir bool) error {
	return c.retryOnAuthError(ctx, func(ctx context.Context) error {
		u := renameFileURL
		if isDir {
			u = renameFolderURL
		}
		return c.doPost(ctx, u, map[string]string{
			"fileId":   strconv.FormatInt(fileID, 10),
			"destName": name,
		}, nil)
	})
}

func (c *client) batchTask(ctx context.Context, taskType string, taskInfos string, targetFolderID string) error {
	return c.retryOnAuthError(ctx, func(ctx context.Context) error {
		form := map[string]string{
			"type":      taskType,
			"taskInfos": taskInfos,
		}
		if targetFolderID != "" {
			form["targetFolderId"] = targetFolderID
		}
		return c.doPost(ctx, batchTaskURL, form, nil)
	})
}

func (c *client) initUpload(ctx context.Context, parentID int64, name string, size int64, fileMd5, sliceMd5 string) (string, string, error) {
	raw, err := c.uploadRequest(ctx, "/person/initMultiUpload", map[string]string{
		"parentFolderId": strconv.FormatInt(parentID, 10),
		"fileName":       name,
		"fileSize":       strconv.FormatInt(size, 10),
		"fileMd5":        fileMd5,
		"sliceMd5":       sliceMd5,
	})
	if err != nil {
		return "", "", err
	}
	var result struct {
		Code string `json:"code"`
		ID   string `json:"id"`
		Data struct {
			UploadFileID string `json:"uploadFileId"`
			FileData     string `json:"fileData"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", "", fmt.Errorf("189: init upload decode: %w", err)
	}
	if result.Code != "SUCCESS" {
		return "", "", fmt.Errorf("189: init upload: %s", result.Code)
	}
	return result.Data.UploadFileID, result.Data.FileData, nil
}

func (c *client) uploadData(ctx context.Context, uploadFileID string, partCount int) (map[string]Part, error) {
	raw, err := c.uploadRequest(ctx, "/person/uploadData", map[string]string{
		"uploadFileId": uploadFileID,
		"partCount":    strconv.Itoa(partCount),
		"partEtag":     "",
	})
	if err != nil {
		return nil, err
	}
	var resp UploadUrlsResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("189: upload data decode: %w", err)
	}
	if resp.Code != "SUCCESS" {
		return nil, fmt.Errorf("189: upload data: %s", resp.Code)
	}
	return resp.UploadUrls, nil
}

func (c *client) commitUpload(ctx context.Context, uploadFileID string) (int64, error) {
	raw, err := c.uploadRequest(ctx, "/person/commitMultiUpload", map[string]string{
		"uploadFileId": uploadFileID,
	})
	if err != nil {
		return 0, err
	}
	var resp UploadCommitResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return 0, fmt.Errorf("189: commit upload decode: %w", err)
	}
	if resp.ResCode != 0 {
		return 0, fmt.Errorf("189: commit upload: %s", resp.ResMessage)
	}
	return resp.ID, nil
}

func (c *client) getCapacity(ctx context.Context) (*CapacityResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spaceURL, nil)
	if err != nil {
		return nil, err
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://cloud.189.cn/")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("189: space: %s", resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("189: read space: %w", err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("189: empty space response")
	}
	if trimmed[0] == '<' {
		var xmlCap xmlCapacity
		if err := xml.Unmarshal(trimmed, &xmlCap); err != nil {
			return nil, fmt.Errorf("189: space xml decode: %w", err)
		}
		return &CapacityResp{
			CloudCapacityInfo: struct {
				TotalSize int64 `json:"totalSize"`
				FreeSize  int64 `json:"freeSize"`
				UsedSize  int64 `json:"usedSize"`
			}{
				TotalSize: xmlCap.CloudCapacityInfo.TotalSize,
				FreeSize:  xmlCap.CloudCapacityInfo.FreeSize,
				UsedSize:  xmlCap.CloudCapacityInfo.UsedSize,
			},
		}, nil
	}
	var capResp CapacityResp
	if err := json.Unmarshal(trimmed, &capResp); err != nil {
		return nil, fmt.Errorf("189: space json decode: %w", err)
	}
	return &capResp, nil
}

func noCache() string {
	return strconv.FormatInt(rand.Int63n(100000000000000000), 10)
}
