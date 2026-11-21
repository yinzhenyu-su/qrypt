package core

import (
	"errors"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestNotFoundHintIncludesNamespacePath(t *testing.T) {
	err := notFoundHint("/cloud/notes/notes.txt", "note.txt")
	if !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("hint does not wrap ErrNotFound: %v", err)
	}
	message := err.Error()
	if !strings.Contains(message, "/cloud/notes/notes.txt") {
		t.Fatalf("hint message = %q, want the full namespace path", message)
	}
	if !strings.Contains(message, `did you mean "note.txt"`) {
		t.Fatalf("hint message = %q, want the closest sibling", message)
	}
}

func TestNotFoundHintWithoutSuggestion(t *testing.T) {
	err := notFoundHint("/cloud/notes/missing.txt", "")
	if got, want := err.Error(), "drive: not found: /cloud/notes/missing.txt"; got != want {
		t.Fatalf("hint message = %q, want %q", got, want)
	}
}

func TestClosestName(t *testing.T) {
	entries := []drive.Entry{{Name: "archive"}, {Name: "note.txt"}, {Name: "._note.txt"}}
	if got := closestName("notes.txt", entries); got != "note.txt" {
		t.Fatalf("closestName = %q, want note.txt", got)
	}
	if got := closestName("NOTES.TXT", entries); got != "note.txt" {
		t.Fatalf("case-insensitive closestName = %q, want note.txt", got)
	}
	if got := closestName("unrelated-name.txt", entries); got != "" {
		t.Fatalf("closestName = %q, want no suggestion", got)
	}
}
