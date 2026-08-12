package debug

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

func NewDebugAIReport(ctx context.Context, command, path string) DebugAIReport {
	return DebugAIReport{
		SchemaVersion: DebugAIReportSchemaVersion,
		GeneratedAt:   time.Now(),
		Command:       command,
		Socket:        debugSocketFromContext(ctx),
		Path:          path,
		Diagnostics:   []DebugAIDiagnostic{},
	}
}

func DebugAIInspectPath(ctx context.Context, path string, eventLimit int, mountNames []string, errors *[]DebugAIError) *DebugAIInspect {
	inspect := &DebugAIInspect{Path: path}
	debugGetJSON(ctx, debugEndpointWithMounts("/v1/resolve?path="+url.QueryEscape(path)+"&include_remote_name=1", mountNames), &inspect.Resolve, errors)
	if path != "/" {
		debugGetJSON(ctx, debugEndpointWithMounts("/v1/cache?path="+url.QueryEscape(path), mountNames), &inspect.Cache, errors)
	}
	debugGetJSON(ctx, debugEndpointWithMounts("/v1/staging?path="+url.QueryEscape(path), mountNames), &inspect.Staging, errors)
	debugGetJSON(ctx, debugEndpointWithMounts("/v1/uploads?history=1&path="+url.QueryEscape(path), mountNames), &inspect.Uploads, errors)
	debugGetJSON(ctx, debugEndpointWithMounts("/v1/reads?path="+url.QueryEscape(path), mountNames), &inspect.Reads, errors)
	debugGetJSON(ctx, "/v1/consistency?path="+url.QueryEscape(path), &inspect.Consistency, errors)
	debugGetJSON(ctx, "/v1/events?level=warn&limit="+url.QueryEscape(fmt.Sprintf("%d", eventLimit))+"&path="+url.QueryEscape(path), &inspect.Events, errors)
	return inspect
}

func debugGetJSON[T any](ctx context.Context, endpoint string, target **T, errors *[]DebugAIError) {
	body, err := debugSocketGet(ctx, endpoint)
	if err != nil {
		*errors = append(*errors, DebugAIError{Endpoint: endpoint, Message: err.Error()})
		return
	}
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		*errors = append(*errors, DebugAIError{Endpoint: endpoint, Message: err.Error()})
		return
	}
	*target = &value
	_ = ctx
}
