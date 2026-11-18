package contracttest

import (
	"context"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestRemoveFixtureTreeDeletesChildrenThenDirectory(t *testing.T) {
	ctx := context.Background()
	driver := localfs.New(t.TempDir())
	if err := driver.Init(ctx); err != nil {
		t.Fatal(err)
	}
	fx, err := NewFixture(ctx, driver, "cleanup")
	if err != nil {
		t.Fatal(err)
	}
	child, err := driver.Mkdir(ctx, fx.TestDir.ID, "child")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.PutSource(ctx, drive.UploadRequest{
		ParentID: child.ID,
		Name:     "payload.txt",
		Source:   drive.NewBytesReadOnlyFileSource([]byte("payload")),
	}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFixtureTree(ctx, driver, fx); err != nil {
		t.Fatalf("remove fixture tree: %v", err)
	}
	entries, err := driver.List(ctx, fx.RootID())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name == fx.Name() {
			t.Fatalf("fixture %q still present after cleanup", fx.Name())
		}
	}
}
