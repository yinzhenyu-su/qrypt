package mobile

import (
	"encoding/json"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

func CreateUploadTaskJSON(coreID, requestRaw string, deadlineMS int) string {
	return createLegacyOperationJSON(coreID, requestRaw, task.UploadPolicyStagingOnly, task.OperationUpload, deadlineMS)
}

func CreateDirectUploadTaskJSON(coreID, requestRaw string, deadlineMS int) string {
	return createLegacyOperationJSON(coreID, requestRaw, task.UploadPolicyPreferDirect, task.OperationUpload, deadlineMS)
}

func CreateDownloadTaskJSON(coreID, requestRaw string, deadlineMS int) string {
	return createLegacyOperationJSON(coreID, requestRaw, "", task.OperationDownload, deadlineMS)
}

func createLegacyOperationJSON(coreID, requestRaw string, policy task.UploadPolicy, operation task.OperationKind, deadlineMS int) string {
	var request task.Request
	if err := json.Unmarshal([]byte(requestRaw), &request); err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return CreateOperationJSON(coreID, mustMarshalOperationRequest(task.OperationRequest{
		Operation:      operation,
		Scope:          request.Scope,
		Items:          request.Items,
		Options:        request.Options,
		UploadPolicy:   policy,
		IdempotencyKey: request.IdempotencyKey,
	}), deadlineMS)
}

func mustMarshalOperationRequest(request task.OperationRequest) string {
	data, err := json.Marshal(request)
	if err != nil {
		panic(err)
	}
	return string(data)
}
