package config

import "testing"

func TestParsePublicBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		basePath   string
		production bool
		want       string
	}{
		{
			name:       "production prefix",
			raw:        "https://alhstudios.com/bb",
			basePath:   "/bb",
			production: true,
			want:       "https://alhstudios.com/bb",
		},
		{
			name:       "development root",
			raw:        "http://127.0.0.1:8080",
			basePath:   "",
			production: false,
			want:       "http://127.0.0.1:8080",
		},
		{
			name:       "development alternate prefix",
			raw:        "http://localhost:8080/forum",
			basePath:   "/forum",
			production: false,
			want:       "http://localhost:8080/forum",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParsePublicBaseURL(test.raw, test.basePath, test.production)
			if err != nil {
				t.Fatalf("ParsePublicBaseURL(%q, %q) returned error: %v", test.raw, test.basePath, err)
			}
			if got.String() != test.want {
				t.Fatalf("ParsePublicBaseURL(%q, %q) = %q, want %q", test.raw, test.basePath, got.String(), test.want)
			}
		})
	}
}

func TestParsePublicBaseURLRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		basePath   string
		production bool
	}{
		{name: "empty URL", raw: "", basePath: "/bb"},
		{name: "malformed escape", raw: "https://example.com/%zz", basePath: "/bb", production: true},
		{name: "relative URL", raw: "/bb", basePath: "/bb"},
		{name: "missing host", raw: "https:///bb", basePath: "/bb", production: true},
		{name: "empty hostname", raw: "https://:443/bb", basePath: "/bb", production: true},
		{name: "unsupported scheme", raw: "ftp://example.com/bb", basePath: "/bb"},
		{name: "HTTP production URL", raw: "http://example.com/bb", basePath: "/bb", production: true},
		{name: "credentials", raw: "https://user:pass@example.com/bb", basePath: "/bb", production: true},
		{name: "query", raw: "https://example.com/bb?debug=true", basePath: "/bb", production: true},
		{name: "empty query marker", raw: "https://example.com/bb?", basePath: "/bb", production: true},
		{name: "fragment", raw: "https://example.com/bb#top", basePath: "/bb", production: true},
		{name: "path mismatch", raw: "https://example.com/forum", basePath: "/bb", production: true},
		{name: "trailing slash mismatch", raw: "https://example.com/bb/", basePath: "/bb", production: true},
		{name: "unsafe base path", raw: "https://example.com/bb", basePath: "/bb/..", production: true},
		{name: "encoded separator", raw: "https://example.com/%2Fbb", basePath: "//bb", production: true},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got, err := ParsePublicBaseURL(test.raw, test.basePath, test.production); err == nil {
				t.Fatalf("ParsePublicBaseURL(%q, %q) = %q, want error", test.raw, test.basePath, got.String())
			}
		})
	}
}
