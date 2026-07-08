package s3

import (
	"context"
	"testing"
)

func TestDebugSnapshot(t *testing.T) {
	d := New(Options{
		Bucket:   "my-bucket",
		Endpoint: "https://s3.us-east-1.amazonaws.com",
		Region:   "us-east-1",
		RootPath: "/data",
	})
	snapshot, err := d.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Driver != "s3" {
		t.Fatalf("driver = %q, want s3", snapshot.Driver)
	}
	if snapshot.Health != "ok" {
		t.Fatalf("health = %q, want ok", snapshot.Health)
	}
	if snapshot.Stats["root_path"] != "data" {
		t.Fatalf("unexpected stats: %+v", snapshot.Stats)
	}
	if snapshot.Stats["bucket"] != "my-bucket" {
		t.Fatalf("unexpected stats: %+v", snapshot.Stats)
	}
	if snapshot.Extra["credential_source"] != "config" {
		t.Fatalf("credential_source = %v, want config", snapshot.Extra["credential_source"])
	}
}
