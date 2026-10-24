package control

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	shareapi "github.com/yinzhenyu/qrypt/pkg/share"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

const shareBasePath = "/v1/control/share"

// requestJSON sends one JSON request with an explicit method. The helper exists
// because delete and patch carry no body and the client otherwise only exposes
// get/post.
func (c *Client) requestJSON(ctx context.Context, method, path string, value any) ([]byte, error) {
	var reader io.Reader
	if value != nil {
		body, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), reader)
	if err != nil {
		return nil, err
	}
	if value != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(req, path)
}

// CreateShareLink publishes one link through the running Core.
func (c *Client) CreateShareLink(ctx context.Context, req shareapi.LinkCreateRequest) (drive.Share, error) {
	body, err := c.PostJSON(ctx, shareBasePath+"/links", req)
	if err != nil {
		return drive.Share{}, err
	}
	var created drive.Share
	if err := json.Unmarshal(body, &created); err != nil {
		return drive.Share{}, err
	}
	return created, nil
}

// ListShareLinks returns one page of the acting account's links.
func (c *Client) ListShareLinks(ctx context.Context, query shareapi.LinkListRequest) (shareapi.LinkList, error) {
	params := url.Values{}
	if query.Mount != "" {
		params.Set("mount", query.Mount)
	}
	if query.Cursor != "" {
		params.Set("cursor", query.Cursor)
	}
	if query.Limit > 0 {
		params.Set("limit", strconv.Itoa(query.Limit))
	}
	if query.IncludeExpired {
		params.Set("all", "true")
	}
	path := shareBasePath + "/links"
	if encoded := params.Encode(); encoded != "" {
		path += "?" + encoded
	}
	body, err := c.Get(ctx, path)
	if err != nil {
		return shareapi.LinkList{}, err
	}
	var listed shareapi.LinkList
	if err := json.Unmarshal(body, &listed); err != nil {
		return shareapi.LinkList{}, err
	}
	return listed, nil
}

// GetShareLink resolves a link by native id or link id.
func (c *Client) GetShareLink(ctx context.Context, mount, id string) (drive.Share, error) {
	path := shareBasePath + "/links/" + url.PathEscape(id)
	if mount != "" {
		path += "?mount=" + url.QueryEscape(mount)
	}
	body, err := c.Get(ctx, path)
	if err != nil {
		return drive.Share{}, err
	}
	var share drive.Share
	if err := json.Unmarshal(body, &share); err != nil {
		return drive.Share{}, err
	}
	return share, nil
}

// UpdateShareLink changes one link's policy.
func (c *Client) UpdateShareLink(ctx context.Context, req shareapi.LinkUpdateRequest) (drive.Share, error) {
	path := shareBasePath + "/links/" + url.PathEscape(req.ID)
	body, err := c.requestJSON(ctx, http.MethodPatch, path, req)
	if err != nil {
		return drive.Share{}, err
	}
	var updated drive.Share
	if err := json.Unmarshal(body, &updated); err != nil {
		return drive.Share{}, err
	}
	return updated, nil
}

// RemoveShareLink deletes one link by link id or native id.
func (c *Client) RemoveShareLink(ctx context.Context, mount, id string) error {
	path := shareBasePath + "/links/" + url.PathEscape(id)
	if mount != "" {
		path += "?mount=" + url.QueryEscape(mount)
	}
	_, err := c.requestJSON(ctx, http.MethodDelete, path, nil)
	return err
}

// ResolveShareLink reports which mounts can act on a link.
func (c *Client) ResolveShareLink(ctx context.Context, req shareapi.ResolveRequest) (shareapi.ResolveResult, error) {
	body, err := c.PostJSON(ctx, shareBasePath+"/resolve", req)
	if err != nil {
		return shareapi.ResolveResult{}, err
	}
	var result shareapi.ResolveResult
	if err := json.Unmarshal(body, &result); err != nil {
		return shareapi.ResolveResult{}, err
	}
	return result, nil
}

// InspectShareLink browses one share directory without creating a task.
func (c *Client) InspectShareLink(ctx context.Context, req shareapi.InspectRequest) (shareapi.InspectResult, error) {
	body, err := c.PostJSON(ctx, shareBasePath+"/inspect", req)
	if err != nil {
		return shareapi.InspectResult{}, err
	}
	var preview shareapi.InspectResult
	if err := json.Unmarshal(body, &preview); err != nil {
		return shareapi.InspectResult{}, err
	}
	return preview, nil
}

// CreateShareTask submits a receive as a durable task.
func (c *Client) CreateShareTask(ctx context.Context, req shareapi.ReceiveRequest) (task.Task, error) {
	body, err := c.PostJSON(ctx, shareBasePath+"/tasks", req)
	if err != nil {
		return task.Task{}, err
	}
	var created task.Task
	if err := json.Unmarshal(body, &created); err != nil {
		return task.Task{}, err
	}
	return created, nil
}

// ListShareTasks returns the receive task list for a query.
func (c *Client) ListShareTasks(ctx context.Context, query shareapi.ReceiveListRequest) ([]task.Task, error) {
	params := url.Values{}
	if query.All {
		params.Set("all", "true")
	}
	if query.State != "" {
		params.Set("state", query.State)
	}
	if query.Mount != "" {
		params.Set("mount", query.Mount)
	}
	if query.Limit > 0 {
		params.Set("limit", strconv.Itoa(query.Limit))
	}
	path := shareBasePath + "/tasks"
	if encoded := params.Encode(); encoded != "" {
		path += "?" + encoded
	}
	body, err := c.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	var listed shareapi.TaskList
	if err := json.Unmarshal(body, &listed); err != nil {
		return nil, err
	}
	return listed.Tasks, nil
}

// GetShareTask resolves any receive task id.
func (c *Client) GetShareTask(ctx context.Context, id string) (task.Task, error) {
	body, err := c.Get(ctx, shareBasePath+"/tasks/"+url.PathEscape(id))
	if err != nil {
		return task.Task{}, err
	}
	var item task.Task
	if err := json.Unmarshal(body, &item); err != nil {
		return task.Task{}, err
	}
	return item, nil
}

// CancelShareTask requests cancellation of a receive task.
func (c *Client) CancelShareTask(ctx context.Context, id string) error {
	_, err := c.PostJSON(ctx, shareBasePath+"/tasks/"+url.PathEscape(id)+"/cancel", map[string]string{"id": id})
	return err
}

// RetryShareTask retries a failed or canceled receive task.
func (c *Client) RetryShareTask(ctx context.Context, id string) error {
	_, err := c.PostJSON(ctx, shareBasePath+"/tasks/"+url.PathEscape(id)+"/retry", map[string]string{"id": id})
	return err
}

// WaitShareTask observes a task to a terminal state. The request context bounds
// the wait: canceling it stops observing without canceling the task.
func (c *Client) WaitShareTask(ctx context.Context, req shareapi.WaitRequest) (task.Task, error) {
	body, err := c.PostJSON(ctx, shareBasePath+"/wait", req)
	if err != nil {
		return task.Task{}, err
	}
	var current task.Task
	if err := json.Unmarshal(body, &current); err != nil {
		return task.Task{}, err
	}
	return current, nil
}

// shareapi.TaskDetail reads the durable receive metadata from a task snapshot.
