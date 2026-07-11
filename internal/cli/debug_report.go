package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

func newDebugAIReport(ctx context.Context, command, path string) debugAIReport {
	return debugAIReport{
		SchemaVersion: debugAIReportSchemaVersion,
		GeneratedAt:   time.Now(),
		Command:       command,
		Socket:        debugSocketFromContext(ctx),
		Path:          path,
		Diagnostics:   []debugAIDiagnostic{},
	}
}

func debugAIInspectPath(ctx context.Context, path string, eventLimit int, errors *[]debugAIError) *debugAIInspect {
	inspect := &debugAIInspect{Path: path}
	debugGetJSON(ctx, "/v1/resolve?path="+url.QueryEscape(path)+"&include_remote_name=1", &inspect.Resolve, errors)
	if path != "/" {
		debugGetJSON(ctx, "/v1/cache?path="+url.QueryEscape(path), &inspect.Cache, errors)
	}
	debugGetJSON(ctx, "/v1/staging?path="+url.QueryEscape(path), &inspect.Staging, errors)
	debugGetJSON(ctx, "/v1/uploads?history=1&path="+url.QueryEscape(path), &inspect.Uploads, errors)
	debugGetJSON(ctx, "/v1/reads?path="+url.QueryEscape(path), &inspect.Reads, errors)
	debugGetJSON(ctx, "/v1/consistency?path="+url.QueryEscape(path), &inspect.Consistency, errors)
	debugGetJSON(ctx, "/v1/events?level=warn&limit="+url.QueryEscape(fmt.Sprintf("%d", eventLimit))+"&path="+url.QueryEscape(path), &inspect.Events, errors)
	return inspect
}

func debugGetJSON[T any](ctx context.Context, endpoint string, target **T, errors *[]debugAIError) {
	body, err := debugSocketGet(ctx, endpoint)
	if err != nil {
		*errors = append(*errors, debugAIError{Endpoint: endpoint, Message: err.Error()})
		return
	}
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		*errors = append(*errors, debugAIError{Endpoint: endpoint, Message: err.Error()})
		return
	}
	*target = &value
	_ = ctx
}
