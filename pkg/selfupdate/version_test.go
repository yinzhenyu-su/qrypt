package selfupdate

import "testing"

func TestParseVersionAcceptsReleaseVersions(t *testing.T) {
	t.Parallel()
	cases := map[string]Version{
		"v1.2.3":     {Major: 1, Minor: 2, Patch: 3},
		"1.2.3":      {Major: 1, Minor: 2, Patch: 3},
		"v0.5.3":     {Major: 0, Minor: 5, Patch: 3},
		" v10.0.20 ": {Major: 10, Minor: 0, Patch: 20},
	}
	for in, want := range cases {
		got, err := ParseVersion(in)
		if err != nil {
			t.Fatalf("ParseVersion(%q) error: %v", in, err)
		}
		if got != want {
			t.Fatalf("ParseVersion(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestParseVersionRejectsNonReleases(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "v1.2", "v1.2.3.4", "dev", "v1.x.3", "v1.2.-3", "v0.5.3-10-g9d16c54", "1.2.3-dirty"} {
		if _, err := ParseVersion(in); err == nil {
			t.Fatalf("ParseVersion(%q) succeeded, want an error", in)
		}
	}
}

func TestIsReleaseVersionGate(t *testing.T) {
	t.Parallel()
	if !IsReleaseVersion("v0.5.3") {
		t.Fatal("a clean release version must pass the gate")
	}
	if IsReleaseVersion("v0.5.3-10-g9d16c54") {
		t.Fatal("a git-describe build must not pass the gate")
	}
	if IsReleaseVersion("dev") {
		t.Fatal("a development build must not pass the gate")
	}
}

func TestVersionCompareOrdersReleases(t *testing.T) {
	t.Parallel()
	ascending := []string{"v0.0.1", "v0.5.0", "v0.5.2", "v0.5.3", "v0.9.9", "v1.0.0"}
	for i := 1; i < len(ascending); i++ {
		lower := MustParseVersion(ascending[i-1])
		higher := MustParseVersion(ascending[i])
		if lower.Compare(higher) >= 0 {
			t.Fatalf("%s should sort before %s", lower, higher)
		}
		if higher.Compare(lower) <= 0 {
			t.Fatalf("%s should sort after %s", higher, lower)
		}
	}
	equal := MustParseVersion("v1.2.3")
	if equal.Compare(MustParseVersion("v1.2.3")) != 0 {
		t.Fatal("equal versions must compare equal")
	}
}

func TestVersionStringRoundTrips(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"v0.5.3", "v1.2.3", "v10.0.20"} {
		if got := MustParseVersion(s).String(); got != s {
			t.Fatalf("round trip %q = %q", s, got)
		}
	}
}
