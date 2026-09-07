package render

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/a-h/templ"
	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

var (
	trustedHTMLPolicy = newTrustedHTMLPolicy()
	legacyHTMLPolicy  = newLegacyHTMLPolicy()
)

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
		"h1",
		"h2",
		"h3",
		"h4",
		"h5",
		"h6",
		"hr",
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
		"table",
		"thead",
		"tbody",
		"tr",
		"th",
		"td",
		"del",
		"input",
	)
	policy.AllowAttrs("type").Matching(regexp.MustCompile(`^checkbox$`)).OnElements("input")
	policy.AllowAttrs("disabled", "checked").Matching(regexp.MustCompile(`^$`)).OnElements("input")
	policy.AllowAttrs("href").OnElements("a")
	policy.AllowRelativeURLs(true)
	policy.AllowURLSchemes("http", "https", "mailto")
	policy.RequireNoFollowOnLinks(true)
	policy.RequireNoReferrerOnLinks(true)
	return policy
}

// newLegacyHTMLPolicy reconstructs the exact admitted p1 sanitizer so the
// release migration can verify that a compatibility row preserves authentic
// p1 output rather than blessing arbitrary persisted HTML.
func newLegacyHTMLPolicy() *bluemonday.Policy {
	policy := bluemonday.NewPolicy()
	policy.AllowElements("p", "em", "strong", "ul", "ol", "li", "a", "blockquote", "pre", "code", "br")
	policy.AllowAttrs("href").OnElements("a")
	policy.AllowRelativeURLs(true)
	policy.AllowURLSchemes("http", "https")
	policy.RequireNoFollowOnLinks(true)
	policy.RequireNoReferrerOnLinks(true)
	return policy
}

func sanitizeLegacyHTML(raw string) string {
	return legacyHTMLPolicy.Sanitize(raw)
}

// SanitizeHTML converts arbitrary persisted renderer output into the only type
// permitted to bypass Templ escaping. It deliberately sanitizes again at read
// time so a corrupt row or obsolete renderer cannot inject active markup.
//
// Complexity: for n input bytes, time and returned space are O(n), Omega(1).
// Bluemonday owns the tokenizer and output allocation. No retry, I/O, or
// background work occurs.
func SanitizeHTML(raw string) TrustedHTML {
	return TrustedHTML{html: trustedHTMLPolicy.Sanitize(filterTaskListInputs(raw))}
}

// filterTaskListInputs removes every raw input except Goldmark's exact disabled
// task-list checkbox shape before Bluemonday normalizes individual attributes.
// Bluemonday cannot require an attribute combination, so this narrow pass also
// prevents a corrupt persisted input with extra form attributes from becoming
// apparently valid merely because those extra attributes were stripped.
//
// Complexity: for n sanitized bytes, time is O(n), Omega(1), and tight
// Theta(n); auxiliary/returned space is O(n), Omega(1), and tight Theta(n) for
// the tokenizer and output buffer. No I/O or shared mutation occurs.
func filterTaskListInputs(raw string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(raw))
	var output bytes.Buffer
	output.Grow(len(raw))
	for {
		tokenType := tokenizer.Next()
		if tokenType == html.ErrorToken {
			return output.String()
		}
		if tokenType == html.StartTagToken || tokenType == html.SelfClosingTagToken {
			rawToken := tokenizer.Raw()
			if isRawInputTag(rawToken) {
				rawInput := append([]byte(nil), rawToken...)
				token := tokenizer.Token()
				if !containsASCIIUpper(rawInput) && validTaskListInput(token.Attr) {
					output.Write(rawInput)
				}
				continue
			}
		}
		output.Write(tokenizer.Raw())
	}
}

// isRawInputTag identifies an exact input tag spelling without asking the
// tokenizer to decode attributes. Token() may normalize the tokenizer's raw
// buffer, so non-input tags must bypass it to preserve entity spelling for the
// real sanitizer.
func isRawInputTag(raw []byte) bool {
	const nameLength = len("input")
	if len(raw) < 1+nameLength || raw[0] != '<' || !bytes.EqualFold(raw[1:1+nameLength], []byte("input")) {
		return false
	}
	if len(raw) == 1+nameLength {
		return true
	}
	switch raw[1+nameLength] {
	case ' ', '\t', '\n', '\r', '\f', '/', '>':
		return true
	default:
		return false
	}
}

// containsASCIIUpper detects raw spellings that the HTML tokenizer would
// normalize but that the project renderer never emits. For n bytes, time is
// O(n), Omega(1), and tight Theta(n); auxiliary space is tight Theta(1).
func containsASCIIUpper(value []byte) bool {
	for _, character := range value {
		if character >= 'A' && character <= 'Z' {
			return true
		}
	}
	return false
}

// validTaskListInput accepts exactly one checkbox type, one empty disabled
// attribute, and at most one empty checked attribute in any order.
//
// Complexity: for a <= 3 attributes, time is O(a), Omega(1), and tight
// Theta(a); auxiliary space is tight Theta(1).
func validTaskListInput(attributes []html.Attribute) bool {
	typeSeen := false
	disabledSeen := false
	checkedSeen := false
	for _, attribute := range attributes {
		switch attribute.Key {
		case "type":
			if typeSeen || attribute.Val != "checkbox" {
				return false
			}
			typeSeen = true
		case "disabled":
			if disabledSeen || attribute.Val != "" {
				return false
			}
			disabledSeen = true
		case "checked":
			if checkedSeen || attribute.Val != "" {
				return false
			}
			checkedSeen = true
		default:
			return false
		}
	}
	return typeSeen && disabledSeen
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
