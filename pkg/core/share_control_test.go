package core

import (
	"errors"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	shareapi "github.com/yinzhenyu/qrypt/pkg/share"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

func TestClassifyShareControlError(t *testing.T) {
	t.Parallel()
	if err := classifyShareControlError(nil); err != nil {
		t.Fatalf("nil error = %v", err)
	}
	for _, usage := range []error{
		ErrSharePathsRequired,
		ErrShareMountRequired,
		ErrShareUnsupported,
		task.ErrInvalidOperation,
	} {
		err := classifyShareControlError(usage)
		var reqErr *shareapi.RequestError
		if !errors.As(err, &reqErr) || reqErr.Class != shareapi.ErrorUsage || !errors.Is(err, usage) {
			t.Fatalf("usage error %v classified as %#v", usage, err)
		}
	}
	notFound := classifyShareControlError(task.ErrNotFound)
	var reqErr *shareapi.RequestError
	if !errors.As(notFound, &reqErr) || reqErr.Class != shareapi.ErrorNotFound {
		t.Fatalf("not-found classified as %#v", notFound)
	}
	provider := drive.NewShareError(drive.ShareErrUnsupported, "", "no share")
	if got := classifyShareControlError(provider); got != provider {
		t.Fatalf("provider share error = %v, want passthrough", got)
	}
	request := &shareapi.RequestError{Class: shareapi.ErrorNotFound, Message: "gone"}
	if got := classifyShareControlError(request); got != request {
		t.Fatalf("request error = %v, want passthrough", got)
	}
	plain := errors.New("boom")
	if got := classifyShareControlError(plain); got != plain {
		t.Fatalf("plain error = %v, want passthrough", got)
	}
}
