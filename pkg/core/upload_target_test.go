package core

import "testing"

func TestUploadDestinationResolverResolvesRelativePathUnderDefault(t *testing.T) {
	resolver := NewUploadDestinationResolver("cloud", "/Inbox")
	got, err := resolver.resolve("photos/a.jpg", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "/cloud/Inbox/photos/a.jpg" || got.DefaultDir != "/cloud/Inbox" {
		t.Fatalf("destination = %+v, want default path", got)
	}
}

func TestUploadDestinationResolverPreservesAbsolutePath(t *testing.T) {
	resolver := NewUploadDestinationResolver("cloud", "/Inbox")
	got, err := resolver.resolve("/other/path.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "/other/path.txt" || got.DefaultDir != "" {
		t.Fatalf("destination = %+v, want absolute path without default dir", got)
	}
}

func TestUploadDestinationResolverRequiresDefaultForRelativePath(t *testing.T) {
	resolver := NewUploadDestinationResolver("", "")
	_, err := resolver.resolve("relative.txt", "")
	if err == nil {
		t.Fatal("Resolve relative without default err = nil, want error")
	}
}
