package vfs

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestPaginateEntriesWithDuplicateNames(t *testing.T) {
	// Entries that share a name but have different ids must not be dropped
	// across page boundaries. Cursor encodes (name, id).
	entries := []drive.Entry{
		{Name: "photo.jpg", ID: "id-3"},
		{Name: "a.txt", ID: "id-1"},
		{Name: "photo.jpg", ID: "id-2"},
		{Name: "photo.jpg", ID: "id-1"},
		{Name: "z.txt", ID: "id-9"},
	}
	page1 := paginateEntries(entries, "", 2)
	if len(page1.Entries) != 2 || page1.Entries[0].Name != "a.txt" || page1.Entries[1].Name != "photo.jpg" || page1.Entries[1].ID != "id-1" {
		t.Fatalf("page1 = %+v, want sorted a.txt, photo.jpg(id-1)", page1.Entries)
	}
	page2 := paginateEntries(entries, page1.NextCursor, 2)
	if len(page2.Entries) != 2 || page2.Entries[0].ID != "id-2" || page2.Entries[1].ID != "id-3" {
		t.Fatalf("page2 = %+v, want photo.jpg(id-2), photo.jpg(id-3)", page2.Entries)
	}
	page3 := paginateEntries(entries, page2.NextCursor, 2)
	if len(page3.Entries) != 1 || page3.Entries[0].Name != "z.txt" || page3.NextCursor != "" {
		t.Fatalf("page3 = %+v, want only z.txt and no cursor", page3.Entries)
	}
	// Collected entries must cover all five entries exactly once.
	seen := map[string]bool{}
	for _, page := range [][]drive.Entry{page1.Entries, page2.Entries, page3.Entries} {
		for _, entry := range page {
			key := entry.Name + ":" + entry.ID
			if seen[key] {
				t.Fatalf("duplicate entry %q across pages", key)
			}
			seen[key] = true
		}
	}
	if len(seen) != 5 {
		t.Fatalf("pages cover %d entries, want 5", len(seen))
	}
}

func TestPaginateEntriesInvalidCursorStartsFromBeginning(t *testing.T) {
	entries := []drive.Entry{
		{Name: "a.txt", ID: "id-1"},
		{Name: "b.txt", ID: "id-2"},
	}
	page := paginateEntries(entries, "not-a-valid-cursor", 10)
	if len(page.Entries) != 2 || page.Entries[0].Name != "a.txt" {
		t.Fatalf("page = %+v, want full sorted listing from start", page.Entries)
	}
}
