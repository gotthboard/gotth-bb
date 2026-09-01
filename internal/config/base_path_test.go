package config

import "testing"

func TestParseBasePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "root deployment", raw: "", want: ""},
		{name: "single segment", raw: "/bb", want: "/bb"},
		{name: "nested prefix", raw: "/community/board", want: "/community/board"},
		{name: "unicode segment", raw: "/café", want: "/café"},
		{name: "unicode replacement character", raw: "/�", want: "/�"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseBasePath(test.raw)
			if err != nil {
				t.Fatalf("ParseBasePath(%q) returned error: %v", test.raw, err)
			}
			if got != test.want {
				t.Fatalf("ParseBasePath(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func TestParseBasePathRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	invalid := []string{
		string([]byte{'/', 0xff}),
		"bb",
		"/",
		"//bb",
		"/bb/",
		"/bb//admin",
		"/bb/.",
		"/bb/..",
		"/bb/../admin",
		"/bb/%2e%2e/admin",
		"/bb/%2Fadmin",
		"/bb/%5cadmin",
		"/bb\\admin",
		"/bb/%00admin",
		"/bb?debug=true",
		"/bb#fragment",
		"/bb/%zz",
		"/bb\x00admin",
		"/bb\nadmin",
	}

	for _, raw := range invalid {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if got, err := ParseBasePath(raw); err == nil {
				t.Fatalf("ParseBasePath(%q) = %q, want error", raw, got)
			}
		})
	}
}
