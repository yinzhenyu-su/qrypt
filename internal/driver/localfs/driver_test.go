package localfs

import (
	"context"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestDriverSpace(t *testing.T) {
	ctx := context.Background()
	driver := New(t.TempDir())
	if err := driver.Init(ctx); err != nil {
		t.Fatal(err)
	}

	space, err := driver.Space(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if space.Total <= 0 {
		t.Fatalf("expected positive total space, got %d", space.Total)
	}
	if space.Free <= 0 {
		t.Fatalf("expected positive free space, got %d", space.Free)
	}
	if space.Free > space.Total {
		t.Fatalf("free space %d exceeds total space %d", space.Free, space.Total)
	}
}

var _ drive.SpaceQuerier = (*Driver)(nil)
