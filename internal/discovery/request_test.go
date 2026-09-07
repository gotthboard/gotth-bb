package discovery

import (
	"strings"
	"testing"
	"time"
)

func TestParseSearchRequestCanonicalizesBoundedGrammar(t *testing.T) {
	t.Parallel()

	got, err := ParseSearchRequest("q=%E3%80%80Cafe%CC%81+OR+tea%E3%80%80&author=42&area=public-square&from=0001-01-01&to=9999-12-31&page=2")
	if err != nil {
		t.Fatalf("ParseSearchRequest() returned error: %v", err)
	}
	if got.Query != "Café OR tea" || got.AuthorID != 42 || got.AreaSlug != "public-square" || got.Page != 2 {
		t.Fatalf("ParseSearchRequest() = %+v", got)
	}
	if !got.From.Equal(time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)) ||
		!got.ToExclusive.Equal(maximumSearchTime) || !got.ToInclusiveMaximum {
		t.Fatalf("date bounds = %s .. %s maximum=%t", got.From, got.ToExclusive, got.ToInclusiveMaximum)
	}
}

func TestParseSearchRequestDefaults(t *testing.T) {
	t.Parallel()

	got, err := ParseSearchRequest("author=7")
	if err != nil {
		t.Fatalf("ParseSearchRequest() returned error: %v", err)
	}
	if got.Page != 1 || got.Query != "" || got.AuthorID != 7 || !got.From.IsZero() || !got.ToExclusive.IsZero() {
		t.Fatalf("ParseSearchRequest() = %+v", got)
	}
}

func TestParseSearchRequestRejectsInvalidWireAndValues(t *testing.T) {
	t.Parallel()

	tests := []string{
		"", "area=public", "unknown=x&q=term", "q=one&q=two", "q=", "q=%00", "q=%01",
		"q=%zz", "q=term;author=1", "author=01", "author=0", "author=9223372036854775808",
		"q=term&area=Upper", "q=term&from=2026-1-01", "q=term&from=2026-02-02&to=2026-02-01",
		"q=term&page=01", "q=term&page=3", "q=" + strings.Repeat("a", 257),
	}
	for _, raw := range tests {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if got, err := ParseSearchRequest(raw); err == nil {
				t.Fatalf("ParseSearchRequest(%q) = %+v, want error", raw, got)
			}
		})
	}
	if got, err := ParseSearchRequest("q=" + strings.Repeat("a", MaximumSearchRawQueryBytes)); err == nil {
		t.Fatalf("overlong raw query = %+v", got)
	}
}

func TestParseActivityCursorParameter(t *testing.T) {
	t.Parallel()

	if got, err := ParseActivityCursorParameter(""); err != nil || got != "" {
		t.Fatalf("first page = %q, %v", got, err)
	}
	valid := "cursor=" + repeatASCII('A', EncodedCursorLength)
	if got, err := ParseActivityCursorParameter(valid); err != nil || len(got) != EncodedCursorLength {
		t.Fatalf("cursor = %q, %v", got, err)
	}
	for _, raw := range []string{"cursor=", "cursor=a&cursor=b", "x=y", "cursor=a;b", "cursor=%zz", "cursor=%00", "cursor=a&", "cursor=" + string(make([]byte, EncodedCursorLength+1))} {
		if got, err := ParseActivityCursorParameter(raw); err == nil {
			t.Fatalf("ParseActivityCursorParameter(%q) = %q, want error", raw, got)
		}
	}
}

func repeatASCII(character byte, count int) string {
	value := make([]byte, count)
	for index := range value {
		value[index] = character
	}
	return string(value)
}
