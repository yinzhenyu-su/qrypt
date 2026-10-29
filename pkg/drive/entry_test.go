package drive

import "testing"

func TestEntryExtraWrapperRemoteName(t *testing.T) {
	entry := Entry{
		Name:  "plain.txt",
		Extra: EntryExtraWrapper{RemoteName: "remote-name", Raw: map[string]any{"id": "raw"}},
	}

	got, ok := EntryRemoteName(entry)
	if !ok || got != "remote-name" {
		t.Fatalf("EntryRemoteName = %q, %v; want remote-name, true", got, ok)
	}
}

func TestWrapRemoteNamePreservesPlainNameAndRawExtra(t *testing.T) {
	raw := map[string]any{"id": "raw"}
	entry := WrapRemoteName(Entry{Name: "plain.txt", Extra: raw}, "qrypt~plain.txt")
	if entry.Name != "plain.txt" {
		t.Fatalf("plain name changed to %q", entry.Name)
	}
	if got, ok := EntryRemoteName(entry); !ok || got != "qrypt~plain.txt" {
		t.Fatalf("remote name = %q, %v", got, ok)
	}
	if got, ok := EntryRawExtra(entry).(map[string]any); !ok || got["id"] != "raw" {
		t.Fatalf("raw extra = %#v", EntryRawExtra(entry))
	}
	if got := WrapRemoteName(Entry{Name: "plain.txt"}, "plain.txt"); got.Extra != nil {
		t.Fatalf("unchanged name wrapped extra: %#v", got.Extra)
	}
}

func TestEntryRawExtraRecursivelyUnwraps(t *testing.T) {
	raw := map[string]any{"id": "raw"}
	entry := Entry{
		Extra: EntryExtraWrapper{
			RemoteName: "outer",
			Raw: EntryExtraWrapper{
				RemoteName: "inner",
				Raw:        raw,
			},
		},
	}

	got := EntryRawExtra(entry)
	if gotMap, ok := got.(map[string]any); !ok || gotMap["id"] != "raw" {
		t.Fatalf("EntryRawExtra = %#v, want raw map", got)
	}
}
