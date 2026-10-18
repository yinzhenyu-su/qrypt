package control

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

// blockingHandler signals when a request reaches the handler and stays blocked
// until release is called. Both actions are safe to call more than once.
func blockingHandler() (handler http.HandlerFunc, entered <-chan struct{}, release func()) {
	enteredCh := make(chan struct{})
	releaseCh := make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	handler = func(http.ResponseWriter, *http.Request) {
		enteredOnce.Do(func() { close(enteredCh) })
		<-releaseCh
	}
	release = func() { releaseOnce.Do(func() { close(releaseCh) }) }
	return handler, enteredCh, release
}

func waitEntered(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("request never reached the handler")
	}
}

// TestServerCloseHonorsCallerDeadlineWithBlockedHandler pins bounded shutdown:
// a handler that ignores its request context must not make another caller's
// deadline ineffective. The drain itself still finishes once the handler is
// released.
func TestServerCloseHonorsCallerDeadlineWithBlockedHandler(t *testing.T) {
	endpoint := testEndpoint(t)
	server, err := NewServer(endpoint, fakeSnapshotter{})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	handler, entered, release := blockingHandler()
	t.Cleanup(func() {
		release()
		_ = server.Close(context.Background())
	})
	server.server.Handler = handler
	client, err := NewClient(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		_, _ = client.Get(context.Background(), "/v1/state")
	}()
	waitEntered(t, entered)

	closeCtx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = server.Close(closeCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v, want the caller's deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Close ignored the caller's deadline for %s", elapsed)
	}

	release()
	<-requestDone
	if err := server.Close(context.Background()); err != nil {
		t.Fatalf("final Close: %v", err)
	}
}

// TestCommandServerCloseHonorsCallerDeadlineWithBlockedHandler covers the
// mutable command transport used by offline wait.
func TestCommandServerCloseHonorsCallerDeadlineWithBlockedHandler(t *testing.T) {
	endpoint := testEndpoint(t)
	manager := task.NewManager()
	t.Cleanup(manager.Close)
	server, err := NewCommandServer(endpoint, manager)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	handler, entered, release := blockingHandler()
	t.Cleanup(func() {
		release()
		_ = server.Close(context.Background())
	})
	server.server.Handler = handler
	client, err := NewClient(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		_, _ = client.Get(context.Background(), "/")
	}()
	waitEntered(t, entered)

	closeCtx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	err = server.Close(closeCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v, want the caller's deadline", err)
	}

	release()
	<-requestDone
	if err := server.Close(context.Background()); err != nil {
		t.Fatalf("final Close: %v", err)
	}
}
