package drive

import (
	"context"
	"testing"
)

func TestUploadProfileContext(t *testing.T) {
	if _, ok := UploadProfileFromContext(context.Background()); ok {
		t.Fatal("background context unexpectedly carries an upload profile")
	}
	ctx := WithUploadProfile(context.Background(), UploadProfile{ChunkSize: 8 << 20, Concurrency: 3})
	profile, ok := UploadProfileFromContext(ctx)
	if !ok || profile.ChunkSize != 8<<20 || profile.Concurrency != 3 {
		t.Fatalf("profile = %+v ok=%v", profile, ok)
	}
}

func TestUploadProfileWithDefaults(t *testing.T) {
	def := UploadProfile{ChunkSize: 16 << 20, Concurrency: 2}
	if got := (UploadProfile{}).WithDefaults(def); got != def {
		t.Fatalf("empty profile defaults = %+v, want %+v", got, def)
	}
	if got := (UploadProfile{ChunkSize: 4 << 20}).WithDefaults(def); got.ChunkSize != 4<<20 || got.Concurrency != 2 {
		t.Fatalf("partial profile = %+v, want chunk 4MiB and default concurrency", got)
	}
}
