package httpui

import (
	"net/url"
	"strings"
	"testing"
)

func TestURLBuilderPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		basePath string
		segments []string
		want     string
	}{
		{name: "root deployment root", basePath: "", want: "/"},
		{name: "root deployment route", basePath: "", segments: []string{"login"}, want: "/login"},
		{name: "prefixed root", basePath: "/bb", want: "/bb/"},
		{name: "prefixed route", basePath: "/bb", segments: []string{"health", "live"}, want: "/bb/health/live"},
		{name: "alternate prefix", basePath: "/community/board", segments: []string{"topics", "42"}, want: "/community/board/topics/42"},
		{name: "hostile segment escaped", basePath: "/bb", segments: []string{"areas", "staff/../../admin?q=1#x"}, want: "/bb/areas/staff%2F..%2F..%2Fadmin%3Fq=1%23x"},
		{name: "unicode segment escaped", basePath: "/bb", segments: []string{"areas", "café"}, want: "/bb/areas/caf%C3%A9"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			publicBase, parseErr := url.Parse("https://forum.example.test" + test.basePath)
			if parseErr != nil {
				t.Fatalf("url.Parse() returned error: %v", parseErr)
			}
			builder, err := NewURLBuilder(*publicBase, test.basePath)
			if err != nil {
				t.Fatalf("NewURLBuilder(%q) returned error: %v", test.basePath, err)
			}
			got, err := builder.Path(test.segments...)
			if err != nil {
				t.Fatalf("Path(%q) returned error: %v", test.segments, err)
			}
			if got != test.want {
				t.Fatalf("Path(%q) = %q, want %q", test.segments, got, test.want)
			}
		})
	}
}

func TestURLBuilderPathRejectsAmbiguousSegments(t *testing.T) {
	t.Parallel()

	builder, err := NewURLBuilder(url.URL{Scheme: "https", Host: "forum.example.test", Path: "/bb"}, "/bb")
	if err != nil {
		t.Fatalf("NewURLBuilder returned error: %v", err)
	}

	for _, segment := range []string{"", ".", ".."} {
		segment := segment
		t.Run(segment, func(t *testing.T) {
			t.Parallel()

			if got, err := builder.Path("areas", segment); err == nil {
				t.Fatalf("Path(%q) = %q, want error", segment, got)
			}
		})
	}
	if got, err := (URLBuilder{}).Path(); err == nil {
		t.Fatalf("zero-value Path() = %q, want error", got)
	}
}

func TestURLBuilderAbsolute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		publicBase url.URL
		basePath   string
		segments   []string
		want       string
	}{
		{name: "root", publicBase: url.URL{Scheme: "https", Host: "forum.example.test"}, want: "https://forum.example.test/"},
		{name: "prefixed topic", publicBase: url.URL{Scheme: "https", Host: "forum.example.test", Path: "/bb"}, basePath: "/bb", segments: []string{"topics", "01JTEST"}, want: "https://forum.example.test/bb/topics/01JTEST"},
		{name: "escaped segment", publicBase: url.URL{Scheme: "https", Host: "forum.example.test", Path: "/bb"}, basePath: "/bb", segments: []string{"areas", "staff/ops?q=1#x"}, want: "https://forum.example.test/bb/areas/staff%2Fops%3Fq=1%23x"},
		{name: "unicode", publicBase: url.URL{Scheme: "https", Host: "forum.example.test", Path: "/café"}, basePath: "/café", segments: []string{"areas", "déjà vu"}, want: "https://forum.example.test/caf%C3%A9/areas/d%C3%A9j%C3%A0%20vu"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			builder, err := NewURLBuilder(test.publicBase, test.basePath)
			if err != nil {
				t.Fatalf("NewURLBuilder() returned error: %v", err)
			}
			got, err := builder.Absolute(test.segments...)
			if err != nil {
				t.Fatalf("Absolute() returned error: %v", err)
			}
			if got != test.want {
				t.Fatalf("Absolute() = %q, want %q", got, test.want)
			}
		})
	}

	builder, err := NewURLBuilder(url.URL{Scheme: "https", Host: "forum.example.test", Path: "/bb"}, "/bb")
	if err != nil {
		t.Fatalf("NewURLBuilder() returned error: %v", err)
	}
	if got, err := builder.Absolute("topics", ".."); err == nil {
		t.Fatalf("Absolute() = %q, want error", got)
	}
	if got, err := (URLBuilder{basePath: "/\x00", initialized: true}).Absolute(); err == nil {
		t.Fatalf("Absolute() from corrupted builder = %q, want error", got)
	}
	if got, err := (URLBuilder{}).Absolute(); err == nil {
		t.Fatalf("zero-value Absolute() = %q, want error", got)
	}
}

func TestURLBuilderPathWithQuery(t *testing.T) {
	t.Parallel()

	builder, err := NewURLBuilder(url.URL{Scheme: "https", Host: "forum.example.test", Path: "/bb"}, "/bb")
	if err != nil {
		t.Fatalf("NewURLBuilder() returned error: %v", err)
	}
	tests := []struct {
		name     string
		segments []string
		query    url.Values
		want     string
	}{
		{name: "empty", segments: []string{"search"}, want: "/bb/search"},
		{name: "sorted and escaped", segments: []string{"search"}, query: url.Values{"q": {"staff & ops"}, "area": {"announcements/general"}}, want: "/bb/search?area=announcements%2Fgeneral&q=staff+%26+ops"},
		{name: "repeated values", segments: []string{"search"}, query: url.Values{"area": {"news", "staff"}}, want: "/bb/search?area=news&area=staff"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := builder.PathWithQuery(test.segments, test.query)
			if err != nil {
				t.Fatalf("PathWithQuery() returned error: %v", err)
			}
			if got != test.want {
				t.Fatalf("PathWithQuery() = %q, want %q", got, test.want)
			}
		})
	}
	if got, err := builder.PathWithQuery([]string{"search", ".."}, url.Values{"q": {"test"}}); err == nil {
		t.Fatalf("PathWithQuery() = %q, want error", got)
	}
}

func TestURLBuilderAbsoluteWithQuery(t *testing.T) {
	t.Parallel()

	builder, err := NewURLBuilder(url.URL{Scheme: "https", Host: "forum.example.test", Path: "/bb"}, "/bb")
	if err != nil {
		t.Fatalf("NewURLBuilder() returned error: %v", err)
	}
	for _, test := range []struct {
		name     string
		segments []string
		query    url.Values
		want     string
	}{
		{name: "empty", segments: []string{"areas", "public"}, want: "https://forum.example.test/bb/areas/public"},
		{name: "page", segments: []string{"areas", "members"}, query: url.Values{"page": {"2"}}, want: "https://forum.example.test/bb/areas/members?page=2"},
		{name: "sorted and escaped", segments: []string{"search"}, query: url.Values{"q": {"staff & ops"}, "area": {"news/general"}}, want: "https://forum.example.test/bb/search?area=news%2Fgeneral&q=staff+%26+ops"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := builder.AbsoluteWithQuery(test.segments, test.query)
			if err != nil || got != test.want {
				t.Fatalf("AbsoluteWithQuery() = (%q, %v), want (%q, nil)", got, err, test.want)
			}
		})
	}
	if got, err := builder.AbsoluteWithQuery([]string{"areas", ".."}, url.Values{"page": {"2"}}); err == nil {
		t.Fatalf("AbsoluteWithQuery() = %q, want error", got)
	}
	if got, err := (URLBuilder{}).AbsoluteWithQuery(nil, url.Values{"page": {"2"}}); err == nil {
		t.Fatalf("zero-value AbsoluteWithQuery() = %q, want error", got)
	}
	if got, err := (URLBuilder{basePath: "/\x00", initialized: true}).AbsoluteWithQuery(nil, url.Values{"page": {"2"}}); err == nil {
		t.Fatalf("AbsoluteWithQuery() from corrupted builder = %q, want error", got)
	}
}

func TestURLBuilderCookiePathUsesNarrowApplicationRoot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		basePath string
		want     string
	}{
		{want: "/"},
		{basePath: "/bb", want: "/bb/"},
		{basePath: "/community/board", want: "/community/board/"},
	}
	for _, test := range tests {
		publicBase, err := url.Parse("https://forum.example.test" + test.basePath)
		if err != nil {
			t.Fatalf("url.Parse() returned error: %v", err)
		}
		builder, err := NewURLBuilder(*publicBase, test.basePath)
		if err != nil {
			t.Fatalf("NewURLBuilder() returned error: %v", err)
		}
		got, err := builder.CookiePath()
		if err != nil || got != test.want {
			t.Fatalf("CookiePath() = (%q, %v), want (%q, nil)", got, err, test.want)
		}
	}
	if got, err := (URLBuilder{}).CookiePath(); err == nil {
		t.Fatalf("zero-value CookiePath() = %q, want error", got)
	}
}

func TestURLBuilderValidateReturnPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		basePath   string
		returnPath string
	}{
		{name: "root deployment root", returnPath: "/"},
		{name: "root deployment route", returnPath: "/login?return=%2F"},
		{name: "prefix without slash", basePath: "/bb", returnPath: "/bb"},
		{name: "prefix root", basePath: "/bb", returnPath: "/bb/"},
		{name: "prefixed route", basePath: "/bb", returnPath: "/bb/topics/42"},
		{name: "canonical query", basePath: "/bb", returnPath: "/bb/search?area=news&q=hello+world"},
		{name: "unicode prefix and route", basePath: "/café", returnPath: "/caf%C3%A9/areas/d%C3%A9j%C3%A0"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			publicBase, err := url.Parse("https://forum.example.test" + test.basePath)
			if err != nil {
				t.Fatalf("url.Parse() returned error: %v", err)
			}
			builder, err := NewURLBuilder(*publicBase, test.basePath)
			if err != nil {
				t.Fatalf("NewURLBuilder() returned error: %v", err)
			}
			got, err := builder.ValidateReturnPath(test.returnPath)
			if err != nil || got != test.returnPath {
				t.Fatalf("ValidateReturnPath(%q) = (%q, %v)", test.returnPath, got, err)
			}
		})
	}
}

func TestURLBuilderValidateReturnPathRejectsUnsafeOrNoncanonicalValues(t *testing.T) {
	t.Parallel()

	builder, err := NewURLBuilder(url.URL{Scheme: "https", Host: "forum.example.test", Path: "/bb"}, "/bb")
	if err != nil {
		t.Fatalf("NewURLBuilder() returned error: %v", err)
	}
	for _, returnPath := range []string{
		"",
		"/" + strings.Repeat("a", 2048),
		"relative",
		"//evil.example/path",
		"https://evil.example/path",
		`/bb\\escape`,
		"/bb/path#fragment",
		"/bbish",
		"/other",
		"/bb//topics",
		"/bb/./topics",
		"/bb/%2e%2e/admin",
		"/bb/%2Fadmin",
		"/bb/%5cadmin",
		"/bb/%00admin",
		"/bb/%61reas",
		"/bb/café",
		"/bb/search?q=hello%20world",
		"/bb/search?%0A=value",
		"/bb/search?q=%0A",
		"/bb?",
		"/bb/%zz",
	} {
		returnPath := returnPath
		t.Run(returnPath, func(t *testing.T) {
			t.Parallel()
			if got, err := builder.ValidateReturnPath(returnPath); err == nil {
				t.Fatalf("ValidateReturnPath(%q) = %q, want error", returnPath, got)
			}
		})
	}
	if got, err := (URLBuilder{}).ValidateReturnPath("/"); err == nil {
		t.Fatalf("zero-value ValidateReturnPath() = %q, want error", got)
	}
}

func TestNewURLBuilderRejectsInvalidBasePath(t *testing.T) {
	t.Parallel()

	if _, err := NewURLBuilder(url.URL{Scheme: "https", Host: "forum.example.test", Path: "/bb/.."}, "/bb/.."); err == nil {
		t.Fatal("NewURLBuilder accepted an unsafe base path")
	}
}

func TestNewURLBuilderRejectsInconsistentOrUnsafePublicBase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		publicBase url.URL
		basePath   string
	}{
		{name: "zero URL", basePath: "/bb"},
		{name: "mismatched path", publicBase: url.URL{Scheme: "https", Host: "forum.example.test", Path: "/other"}, basePath: "/bb"},
		{name: "credentials", publicBase: url.URL{Scheme: "https", Host: "forum.example.test", User: url.UserPassword("user", "secret"), Path: "/bb"}, basePath: "/bb"},
		{name: "query", publicBase: url.URL{Scheme: "https", Host: "forum.example.test", Path: "/bb", RawQuery: "debug=true"}, basePath: "/bb"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewURLBuilder(test.publicBase, test.basePath); err == nil {
				t.Fatal("NewURLBuilder accepted an unsafe or inconsistent public base")
			}
		})
	}
}
