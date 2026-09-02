package buildinfo

import (
	"reflect"
	"testing"
)

func TestCurrentReturnsExplicitDevelopmentIdentity(t *testing.T) {
	t.Parallel()

	got, err := Current()
	want := Info{Version: "development", Commit: "unknown"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Current() = (%+v, %v), want (%+v, nil)", got, err, want)
	}
}

func TestValidateAcceptsExactReleaseIdentity(t *testing.T) {
	t.Parallel()

	const commit = "0123456789abcdef0123456789abcdef01234567"
	got, err := validate("1.0.0-alpha.1+linux-amd64", commit)
	want := Info{Version: "1.0.0-alpha.1+linux-amd64", Commit: commit}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("validate() = (%+v, %v), want (%+v, nil)", got, err, want)
	}
}

func TestValidateRejectsUntraceableIdentity(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ version, commit string }{
		{version: "development", commit: "0123456789abcdef0123456789abcdef01234567"},
		{version: "1.0.0-alpha.1", commit: "unknown"},
		{version: "", commit: "0123456789abcdef0123456789abcdef01234567"},
		{version: "x.1", commit: "0123456789abcdef0123456789abcdef01234567"},
		{version: "1.0.0_alpha", commit: "0123456789abcdef0123456789abcdef01234567"},
		{version: "1.0.0-alpha.1", commit: "0123456789abcdef0123456789abcdef0123456"},
		{version: "1.0.0-alpha.1", commit: "0123456789abcdef0123456789abcdef0123456g"},
		{version: "1.0.0-alpha.1", commit: "0123456789ABCDEF0123456789abcdef01234567"},
		{version: "1" + string(make([]byte, maximumVersionBytes)), commit: "0123456789abcdef0123456789abcdef01234567"},
	} {
		if got, err := validate(test.version, test.commit); err == nil || got != (Info{}) {
			t.Fatalf("validate(%q, %q) = (%+v, %v)", test.version, test.commit, got, err)
		}
	}
}
