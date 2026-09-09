package core

import (
	"context"
	"errors"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

func TestTaskRequestForOperationMapsUploadPolicyToInternalType(t *testing.T) {
	preferDirect, err := taskRequestForOperation(task.OperationRequest{
		Operation:    task.OperationUpload,
		Items:        []task.Item{{SourcePath: "token", DestPath: "/file.txt"}},
		UploadPolicy: task.UploadPolicyPreferDirect,
	})
	if err != nil {
		t.Fatal(err)
	}
	if preferDirect.Type != task.TypeUploadStreamDirect {
		t.Fatalf("prefer_direct type = %q, want %q", preferDirect.Type, task.TypeUploadStreamDirect)
	}

	stagingOnly, err := taskRequestForOperation(task.OperationRequest{
		Operation:    task.OperationUpload,
		Items:        []task.Item{{SourcePath: "token", DestPath: "/file.txt"}},
		UploadPolicy: task.UploadPolicyStagingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stagingOnly.Type != task.TypeUploadStreamBatch {
		t.Fatalf("staging_only type = %q, want %q", stagingOnly.Type, task.TypeUploadStreamBatch)
	}
}

func TestCreateOperationRejectsInvalidRequestBeforeCoreAccess(t *testing.T) {
	_, err := (&Core{}).CreateOperation(context.Background(), task.OperationRequest{})
	if !errors.Is(err, task.ErrInvalidOperation) {
		t.Fatalf("CreateOperation() error = %v, want %v", err, task.ErrInvalidOperation)
	}
}

func TestOperationFingerprintDoesNotIncludeIdempotencyKey(t *testing.T) {
	first := task.OperationRequest{
		Operation:    task.OperationUpload,
		Items:        []task.Item{{DestPath: "/file.txt"}},
		UploadPolicy: task.UploadPolicyStagingOnly,
		Idempotency:  "first-key",
	}
	second := first
	second.Idempotency = "second-key"

	firstFingerprint, err := operationFingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	secondFingerprint, err := operationFingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstFingerprint != secondFingerprint {
		t.Fatalf("fingerprints differ for idempotency key only: %q != %q", firstFingerprint, secondFingerprint)
	}
}
