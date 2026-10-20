package offline

import (
	"context"
	"errors"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// TestWaitWithReconnectTimeoutBoundsStalledRequest verifies a non-terminal wait
// with a local timeout cannot hang on a server that accepts the connection and
// never answers.
func TestWaitWithReconnectTimeoutBoundsStalledRequest(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })

	cmd := &cobra.Command{Use: "wait"}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cmd.SetContext(context.WithValue(ctx, endpointContextKey{}, server.URL))

	type result struct {
		err error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		_, err := waitWithReconnect(cmd, "offline-stalled", 100*time.Millisecond, newOfflineProgress(&discardWriter{}, i18n.New(i18n.LangEN)))
		done <- result{err: err}
	}()

	select {
	case got := <-done:
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("wait returned after %s, want bounded by the timeout", elapsed)
		}
		var offline *drive.OfflineError
		if !errors.As(got.err, &offline) || offline.Code != drive.OfflineErrWaitTimeout {
			t.Fatalf("error = %v, want offline_wait_timeout", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitWithReconnect hung on a stalled control request")
	}
}

type discardWriter struct{}

func (*discardWriter) Write(p []byte) (int, error) { return len(p), nil }
