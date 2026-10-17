package contracttest

import "testing"

func TestNormalizeMountName(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "quark-test", want: "quark-test"},
		{input: "/quark-test", want: "quark-test"},
		{input: " /quark-test/ ", want: "quark-test"},
		{input: "///", want: ""},
	} {
		if got := NormalizeMountName(test.input); got != test.want {
			t.Fatalf("NormalizeMountName(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}
