package core

import "testing"

// TestOfflineMergeKeepsIdentityPaths pins that a thin later observation (same
// stable RemoteIDs and names, but no paths) refreshes metadata without
// rewriting each identity's previously resolved staged path.
func TestOfflineMergeKeepsIdentityPaths(t *testing.T) {
	t.Parallel()

	existing := []offlineResultFile{
		{Index: 0, Name: "same.bin", Path: "one/same.bin", RemoteID: "id-one", Size: 3},
		{Index: 1, Name: "same.bin", Path: "two/same.bin", RemoteID: "id-two", Size: 4},
	}
	incoming := []offlineResultFile{
		{Index: 0, Name: "same.bin", RemoteID: "id-one", Size: 3},
		{Index: 1, Name: "same.bin", RemoteID: "id-two", Size: 4},
	}
	merged := mergeOfflineResultFiles(existing, incoming)
	if len(merged) != 2 {
		t.Fatalf("merged = %+v, want two files", merged)
	}
	for index := range merged {
		if merged[index].Path != existing[index].Path {
			t.Errorf("identity %s changed path: got %q want %q", merged[index].RemoteID, merged[index].Path, existing[index].Path)
		}
	}
}

// TestOfflineMergeNameFallbackOnlyWhenUnambiguous verifies a name shared by
// several retained identities is never used to copy one file's path onto
// another.
func TestOfflineMergeNameFallbackOnlyWhenUnambiguous(t *testing.T) {
	t.Parallel()

	existing := []offlineResultFile{
		{Name: "same.bin", Path: "one/same.bin", RemoteID: "id-one"},
		{Name: "same.bin", Path: "two/same.bin", RemoteID: "id-two"},
		{Name: "unique.bin", Path: "unique/unique.bin", RemoteID: "id-unique"},
	}
	incoming := []offlineResultFile{
		{Name: "same.bin"},
		{Name: "unique.bin"},
	}
	merged := mergeOfflineResultFiles(existing, incoming)
	if merged[0].Path != "" {
		t.Fatalf("ambiguous name inherited path %q", merged[0].Path)
	}
	if merged[0].RemoteID != "" {
		t.Fatalf("ambiguous name inherited identity %q", merged[0].RemoteID)
	}
	if merged[1].Path != "unique/unique.bin" || merged[1].RemoteID != "id-unique" {
		t.Fatalf("unique name did not inherit its identity: %+v", merged[1])
	}
}
