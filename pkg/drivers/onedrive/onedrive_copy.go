package onedrive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

// Copy implements drive.ServerSideCopier: Microsoft Graph driveItem: copy
// (POST /items/{id}/copy) — an async long-running action that returns 202
// with a Location header to poll until completion, then the copied item is
// located by name under the destination parent. Directory copies are
// rejected per the contract.
func (d *Driver) Copy(ctx context.Context, src drive.Entry, dstParentID, dstName string) (drive.Entry, error) {
	if src.IsDir {
		return drive.Entry{}, drive.ErrUnsupported
	}
	body, err := json.Marshal(map[string]any{
		"parentReference": map[string]any{"id": d.resolveID(dstParentID)},
		"name":            dstName,
	})
	if err != nil {
		return drive.Entry{}, err
	}
	copyURL := d.apiPath(fmt.Sprintf("/items/%s/copy", url.PathEscape(src.ID)))

	location, err := d.copyRequest(ctx, copyURL, body)
	if err != nil {
		return drive.Entry{}, err
	}
	if location != "" {
		if err := d.waitForCopyJob(ctx, location); err != nil {
			return drive.Entry{}, err
		}
	}
	// The copied item is located by name (Graph does not return the new id
	// directly; the async action may also complete synchronously).
	item, err := d.itemByChildName(ctx, d.resolveID(dstParentID), dstName)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("onedrive: copy locate: %w", err)
	}
	return item.entry(dstParentID), nil
}

// copyRequest POSTs the copy body and returns the long-running action
// Location header (empty when the copy completed synchronously). A 401
// refreshes the token once and retries, matching requestJSON.
func (d *Driver) copyRequest(ctx context.Context, copyURL string, body []byte) (string, error) {
	do := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, copyURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		return d.client.Do(req)
	}
	resp, err := do(d.currentAccessToken())
	if err != nil {
		return "", fmt.Errorf("onedrive: copy: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if refreshErr := d.refresh(ctx); refreshErr != nil {
			return "", refreshErr
		}
		resp, err = do(d.currentAccessToken())
		if err != nil {
			return "", fmt.Errorf("onedrive: copy: %w", err)
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("onedrive: copy: status %d: %s", resp.StatusCode, string(respBody))
	}
	return resp.Header.Get("Location"), nil
}

// waitForCopyJob polls the long-running action URL until the copy finishes
// or the deadline expires.
func (d *Driver) waitForCopyJob(ctx context.Context, location string) error {
	deadline := time.Now().Add(30 * time.Second)
	attempt := 0
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+d.currentAccessToken())
		resp, err := d.client.Do(req)
		if err != nil {
			return fmt.Errorf("onedrive: copy job poll: %w", err)
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
			return nil
		}
		if resp.StatusCode == http.StatusUnauthorized {
			if refreshErr := d.refresh(ctx); refreshErr != nil {
				return refreshErr
			}
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("onedrive: copy job timed out: %s", string(respBody))
		}
		if err := util.WaitExponential(ctx, attempt); err != nil {
			return err
		}
		attempt++
	}
}
