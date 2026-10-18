package control

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

const offlineBasePath = "/v1/control/offline"

// CreateOfflineTask submits provider-neutral offline work through the running
// Core and returns the created task snapshot.
func (c *Client) CreateOfflineTask(ctx context.Context, req OfflineCreateRequest) (task.Task, error) {
	body, err := c.PostJSON(ctx, offlineBasePath+"/tasks", req)
	if err != nil {
		return task.Task{}, err
	}
	var created task.Task
	if err := json.Unmarshal(body, &created); err != nil {
		return task.Task{}, err
	}
	return created, nil
}

// ListOfflineTasks returns the offline task list for a query.
func (c *Client) ListOfflineTasks(ctx context.Context, query OfflineListRequest) ([]task.Task, error) {
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
	path := offlineBasePath + "/tasks"
	if encoded := params.Encode(); encoded != "" {
		path += "?" + encoded
	}
	body, err := c.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	var listed OfflineTaskList
	if err := json.Unmarshal(body, &listed); err != nil {
		return nil, err
	}
	return listed.Tasks, nil
}

// GetOfflineTask resolves any offline task id.
func (c *Client) GetOfflineTask(ctx context.Context, id string) (task.Task, error) {
	body, err := c.Get(ctx, offlineBasePath+"/tasks/"+url.PathEscape(id))
	if err != nil {
		return task.Task{}, err
	}
	var item task.Task
	if err := json.Unmarshal(body, &item); err != nil {
		return task.Task{}, err
	}
	return item, nil
}

// CancelOfflineTask requests provider-side removal of an offline task.
func (c *Client) CancelOfflineTask(ctx context.Context, id string) error {
	_, err := c.PostJSON(ctx, offlineBasePath+"/tasks/"+url.PathEscape(id)+"/cancel", map[string]string{"id": id})
	return err
}

// RetryOfflineTask retries a failed or canceled offline task.
func (c *Client) RetryOfflineTask(ctx context.Context, id string) error {
	_, err := c.PostJSON(ctx, offlineBasePath+"/tasks/"+url.PathEscape(id)+"/retry", map[string]string{"id": id})
	return err
}

// InspectOffline previews a source without creating a task.
func (c *Client) InspectOffline(ctx context.Context, req OfflineInspectRequest) (OfflinePreviewResult, error) {
	body, err := c.PostJSON(ctx, offlineBasePath+"/inspect", req)
	if err != nil {
		return OfflinePreviewResult{}, err
	}
	var preview OfflinePreviewResult
	if err := json.Unmarshal(body, &preview); err != nil {
		return OfflinePreviewResult{}, err
	}
	return preview, nil
}

// WaitOfflineTask observes a task to a terminal state. The request context
// bounds the wait: canceling it stops observing without canceling the task.
func (c *Client) WaitOfflineTask(ctx context.Context, req OfflineWaitRequest) (task.Task, error) {
	body, err := c.PostJSON(ctx, offlineBasePath+"/wait", req)
	if err != nil {
		return task.Task{}, err
	}
	var current task.Task
	if err := json.Unmarshal(body, &current); err != nil {
		return task.Task{}, err
	}
	return current, nil
}

// OfflineTaskDetail reads the durable offline metadata from a task snapshot for
// display. It returns the raw detail map under the stable offline key.
func OfflineTaskDetail(item task.Task) map[string]any {
	if item.Detail == nil {
		return nil
	}
	value, ok := item.Detail[task.OfflineDetailKey]
	if !ok {
		return nil
	}
	detail, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return detail
}

// OfflineDetailString reads a string field from the offline detail map.
func OfflineDetailString(detail map[string]any, key string) string {
	if detail == nil {
		return ""
	}
	value, _ := detail[key].(string)
	return strings.TrimSpace(value)
}

// OfflineDetailBool reads a bool field from the offline detail map.
func OfflineDetailBool(detail map[string]any, key string) bool {
	if detail == nil {
		return false
	}
	value, _ := detail[key].(bool)
	return value
}

// OfflineDetailFloat reads a numeric field from the offline detail map.
func OfflineDetailFloat(detail map[string]any, key string) float64 {
	if detail == nil {
		return 0
	}
	switch value := detail[key].(type) {
	case float64:
		return value
	case int64:
		return float64(value)
	default:
		return 0
	}
}
