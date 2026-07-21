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
