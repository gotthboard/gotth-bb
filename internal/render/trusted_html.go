package render

import (
	"github.com/a-h/templ"
	"github.com/microcosm-cc/bluemonday"
)

var trustedHTMLPolicy = newTrustedHTMLPolicy()

// TrustedHTML is HTML that has crossed the sole persisted-content sanitizer
// boundary. Its representation is private so arbitrary strings cannot opt out
// of Templ escaping. The zero value is valid and renders no content.
type TrustedHTML struct {
	html string
}

// newTrustedHTMLPolicy constructs the immutable post-render sanitizer used for
// persisted forum content. The returned policy is fully configured before it
// is published and Bluemonday documents completed policies as safe for
// concurrent sanitization.
//
// Complexity: construction is O(1) time and space because the allowlists are
// fixed. It performs no I/O and starts no background work.
func newTrustedHTMLPolicy() *bluemonday.Policy {
	policy := bluemonday.NewPolicy()
	policy.AllowElements(
		"p",
		"em",
		"strong",
		"ul",
		"ol",
		"li",
		"a",
		"blockquote",
		"pre",
		"code",
		"br",
	)
	policy.AllowAttrs("href").OnElements("a")
	policy.AllowRelativeURLs(true)
	policy.AllowURLSchemes("http", "https")
	policy.RequireNoFollowOnLinks(true)
	policy.RequireNoReferrerOnLinks(true)
	return policy
}

// SanitizeHTML converts arbitrary persisted renderer output into the only type
// permitted to bypass Templ escaping. It deliberately sanitizes again at read
// time so a corrupt row or obsolete renderer cannot inject active markup.
//
// Complexity: for n input bytes, time and returned space are O(n), Omega(1).
// Bluemonday owns the tokenizer and output allocation. No retry, I/O, or
// background work occurs.
func SanitizeHTML(raw string) TrustedHTML {
	return TrustedHTML{html: trustedHTMLPolicy.Sanitize(raw)}
}

// Component exposes trusted content only as a Templ component; it does not
// reveal a string that callers could confuse with untrusted renderer output.
//
// Complexity: rendering n sanitized bytes takes O(n) time and O(1) auxiliary
// space beyond the caller's writer. No retry, I/O beyond that writer, or
// background work occurs.
func (trusted TrustedHTML) Component() templ.Component {
	return templ.Raw(trusted.html)
}
