// Package onedrive implements a Microsoft OneDrive backend driver for qrypt.
package onedrive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/internal/retry"
)

func (d *Driver) refresh(ctx context.Context) error {
	if d.appMode {
		return d.refreshApp(ctx)
	}
	refreshToken := d.currentRefreshToken()
	if refreshToken == "" {
		return fmt.Errorf("onedrive: refresh token is required")
	}
	if d.useOnlineAPI {
		err := d.refreshOnline(ctx, refreshToken)
		if err == nil {
			return nil
		}
		if d.clientID == "" || d.clientSecret == "" {
			return err
		}
	}
	return d.refreshOAuth(ctx, refreshToken)
}

func (d *Driver) refreshApp(ctx context.Context) error {
	if d.clientID == "" || d.clientSecret == "" {
		return fmt.Errorf("onedrive_app: client_id and client_secret are required")
	}
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", d.clientID)
	form.Set("client_secret", d.clientSecret)
	form.Set("resource", d.apiBaseURL+"/")
	form.Set("scope", d.apiBaseURL+"/.default")
	reqBody := strings.NewReader(form.Encode())
	var resp tokenResp
	if err := d.requestNoAuthRaw(ctx, http.MethodPost, d.oauthBaseURL+"/"+url.PathEscape(d.tenantID)+"/oauth2/token", reqBody, "application/x-www-form-urlencoded", &resp); err != nil {
		return fmt.Errorf("onedrive_app: access token: %w", err)
	}
	if resp.AccessToken == "" {
		return fmt.Errorf("onedrive_app: access token returned empty token")
	}
	d.setTokens(resp.AccessToken, "")
	return nil
}

func (d *Driver) refreshOnline(ctx context.Context, refreshToken string) error {
	u, err := url.Parse(d.onlineAPI)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("refresh_ui", refreshToken)
	q.Set("server_use", "true")
	q.Set("driver_txt", "onedrive_pr")
	u.RawQuery = q.Encode()
	var resp onlineTokenResp
	if err := d.requestNoAuthJSON(ctx, http.MethodGet, u.String(), nil, &resp); err != nil {
		return fmt.Errorf("onedrive: refresh token: %w", err)
	}
	if resp.AccessToken == "" || resp.RefreshToken == "" {
		if resp.ErrorMessage != "" {
			return fmt.Errorf("onedrive: refresh token: %s", resp.ErrorMessage)
		}
		return fmt.Errorf("onedrive: refresh token returned empty token")
	}
	d.setTokens(resp.AccessToken, resp.RefreshToken)
	return nil
}

func (d *Driver) refreshOAuth(ctx context.Context, refreshToken string) error {
	if d.clientID == "" || d.clientSecret == "" {
		return fmt.Errorf("onedrive: client_id and client_secret are required when use_online_api=false")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", d.clientID)
	form.Set("client_secret", d.clientSecret)
	form.Set("refresh_token", refreshToken)
	if d.redirectURI != "" {
		form.Set("redirect_uri", d.redirectURI)
	}
	reqBody := strings.NewReader(form.Encode())
	var resp tokenResp
	if err := d.requestNoAuthRaw(ctx, http.MethodPost, d.oauthBaseURL+"/common/oauth2/v2.0/token", reqBody, "application/x-www-form-urlencoded", &resp); err != nil {
		return fmt.Errorf("onedrive: refresh token: %w", err)
	}
	if resp.AccessToken == "" || resp.RefreshToken == "" {
		return fmt.Errorf("onedrive: refresh token returned empty token")
	}
	d.setTokens(resp.AccessToken, resp.RefreshToken)
	return nil
}

func (d *Driver) currentAccessToken() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.accessToken
}

func (d *Driver) currentRefreshToken() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.refreshToken
}

func (d *Driver) setTokens(accessToken, refreshToken string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.accessToken = accessToken
	d.refreshToken = refreshToken
}

func (d *Driver) metaURL(p string) string {
	p = cleanOneDrivePath(p)
	if p == "/" {
		return d.apiPath("/root")
	}
	return d.apiPath("/root:" + escapeDrivePath(p) + ":")
}

func (d *Driver) apiPath(suffix string) string {
	if d.appMode {
		return d.apiBaseURL + "/v1.0/users/" + url.PathEscape(d.email) + "/drive" + suffix
	}
	if d.isSharepoint {
		return d.apiBaseURL + "/v1.0/sites/" + url.PathEscape(d.siteID) + "/drive" + suffix
	}
	return d.apiBaseURL + "/v1.0/me/drive" + suffix
}

func (d *Driver) driveURL() string {
	if d.appMode {
		return d.apiBaseURL + "/v1.0/users/" + url.PathEscape(d.email) + "/drive"
	}
	if d.isSharepoint {
		return d.apiBaseURL + "/v1.0/sites/" + url.PathEscape(d.siteID) + "/drive"
	}
	return d.apiBaseURL + "/v1.0/me/drive"
}

func (d *Driver) applyCustomHost(rawURL string) string {
	if d.customHost == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.Host = d.customHost
	return u.String()
}

func (d *Driver) requestJSON(ctx context.Context, method, rawURL string, body, result any) error {
	err := d.requestJSONNoRefresh(ctx, method, rawURL, body, result)
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.Code == "InvalidAuthenticationToken" {
		if refreshErr := d.refresh(ctx); refreshErr != nil {
			return refreshErr
		}
		return d.requestJSONNoRefresh(ctx, method, rawURL, body, result)
	}
	return err
}

func (d *Driver) requestJSONNoRefresh(ctx context.Context, method, rawURL string, body, result any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	return d.requestRaw(ctx, method, rawURL, reader, "application/json", result)
}

func (d *Driver) requestRaw(ctx context.Context, method, rawURL string, body io.Reader, contentType string, result any) error {
	return d.requestRawWithAuth(ctx, method, rawURL, body, contentType, result, d.currentAccessToken())
}

func (d *Driver) requestNoAuthJSON(ctx context.Context, method, rawURL string, body, result any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	return d.requestNoAuthRaw(ctx, method, rawURL, reader, "application/json", result)
}

func (d *Driver) requestNoAuthRaw(ctx context.Context, method, rawURL string, body io.Reader, contentType string, result any) error {
	return d.requestRawWithAuth(ctx, method, rawURL, body, contentType, result, "")
}

func (d *Driver) requestRawWithAuth(ctx context.Context, method, rawURL string, body io.Reader, contentType string, result any, accessToken string) error {
	var lastErr error
	for attempt := 0; attempt < oneDriveRequestAttempts; attempt++ {
		err := d.requestRawOnce(ctx, method, rawURL, body, contentType, result, accessToken)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryableOneDriveError(ctx, err) || attempt == oneDriveRequestAttempts-1 {
			return err
		}
		if waitErr := retry.WaitExponential(ctx, attempt); waitErr != nil {
			return waitErr
		}
	}
	return lastErr
}

func (d *Driver) requestRawOnce(ctx context.Context, method, rawURL string, body io.Reader, contentType string, result any, accessToken string) error {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return err
	}
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	if contentType != "" && body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	start := time.Now()
	resp, err := d.client.Do(req)
	d.recordHTTP(ctx, method, method, rawURL, start, respStatus(resp), err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var graphErr graphErrorResp
		_ = json.Unmarshal(data, &graphErr)
		if graphErr.Error.Code != "" || graphErr.Error.Message != "" {
			return &apiError{Status: resp.StatusCode, Code: graphErr.Error.Code, Message: graphErr.Error.Message}
		}
		return &apiError{Status: resp.StatusCode, Message: strings.TrimSpace(string(data))}
	}
	if result != nil && len(data) > 0 {
		if err := json.Unmarshal(data, result); err != nil {
			return err
		}
	}
	return nil
}
