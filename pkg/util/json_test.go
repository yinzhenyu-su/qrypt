package util

import (
	"bytes"
	"strings"
	"testing"
)

func TestWritePrettyJSON(t *testing.T) {
	var out bytes.Buffer
	if err := WritePrettyJSON(&out, map[string]string{"html": "<tag>"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "\n  ") {
		t.Fatalf("WritePrettyJSON did not indent output: %q", got)
	}
	if strings.Contains(got, `\u003c`) {
		t.Fatalf("WritePrettyJSON escaped HTML unexpectedly: %q", got)
	}
}
