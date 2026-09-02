package policy

import (
	"strings"
	"testing"
)

func TestValidAreaSlugMatchesDatabaseGrammar(t *testing.T) {
	t.Parallel()

	for _, slug := range []string{"a", "0", "public", "member-news-2026", strings.Repeat("a", 80)} {
		slug := slug
		t.Run(slug, func(t *testing.T) {
			t.Parallel()
			if !ValidAreaSlug(slug) {
				t.Fatalf("ValidAreaSlug(%q) = false", slug)
			}
		})
	}
	for _, slug := range []string{
		"", strings.Repeat("a", 81), "Uppercase", "with/slash", "with space",
		"-leading", "trailing-", "double--hyphen", "nul\x00byte", "é",
	} {
		slug := slug
		t.Run("invalid/"+strings.ReplaceAll(slug, "/", "slash"), func(t *testing.T) {
			t.Parallel()
			if ValidAreaSlug(slug) {
				t.Fatalf("ValidAreaSlug(%q) = true", slug)
			}
		})
	}
}
