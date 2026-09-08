package render

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/a-h/templ"
	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
	"golang.org/x/text/unicode/norm"
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

// VisibleText derives the sole search projection input from sanitized HTML.
// Text nodes contribute in render order; the contract's block elements add
// boundaries; attributes, comments, and all markup contribute no text.
// Unicode 15 White_Space runs collapse to one ASCII space before NFC.
//
// Complexity: for n HTML bytes, time and returned/auxiliary space are O(n),
// Omega(1), and tight Theta(n) when text survives. The tokenizer and builder
// perform no I/O, retry, shared mutation, or background work.
func (trusted TrustedHTML) VisibleText() string {
	tokenizer := html.NewTokenizer(strings.NewReader(trusted.html))
	var output strings.Builder
	output.Grow(len(trusted.html))
	wroteText := false
	pendingSpace := false
	for {
		tokenType := tokenizer.Next()
		if tokenType == html.ErrorToken {
			return norm.NFC.String(output.String())
		}
		switch tokenType {
		case html.TextToken:
			for _, character := range tokenizer.Token().Data {
				if isUnicode15WhiteSpace(character) {
					pendingSpace = wroteText
					continue
				}
				if pendingSpace {
					output.WriteByte(' ')
					pendingSpace = false
				}
				output.WriteRune(character)
				wroteText = true
			}
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			name, _ := tokenizer.TagName()
			if isVisibleTextBoundary(string(name)) {
				pendingSpace = wroteText
			}
		}
	}
}

// isVisibleTextBoundary pins the exact HTML elements that separate visible
// search text. Inline and unknown elements deliberately add no boundary.
//
// Complexity: time and auxiliary space are tight Theta(1).
func isVisibleTextBoundary(name string) bool {
	switch name {
	case "p", "h1", "h2", "h3", "h4", "h5", "h6", "hr", "ul", "ol", "li", "blockquote", "pre", "br", "table", "thead", "tbody", "tr", "th", "td":
		return true
	default:
		return false
	}
}

// isUnicode15WhiteSpace recognizes the frozen Unicode 15 White_Space
// property rather than inheriting future behavior from unicode.IsSpace.
//
// Complexity: time and auxiliary space are tight Theta(1).
func isUnicode15WhiteSpace(character rune) bool {
	switch character {
	case '\u0009', '\u000a', '\u000b', '\u000c', '\u000d', '\u0020', '\u0085', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000':
		return true
	default:
		return character >= '\u2000' && character <= '\u200a'
	}
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
				if validTaskListInput(rawInput) {
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

// validTaskListInput accepts only the three admitted task-list spellings.
// Comparing the raw token is deliberate: the HTML tokenizer normalizes and
// deduplicates attributes, which would otherwise erase evidence that persisted
// input was not emitted by the renderer.
//
// Complexity: time is O(n), bounded by the tokenizer's maximum raw tag;
// auxiliary space is tight Theta(1).
func validTaskListInput(raw []byte) bool {
	return bytes.Equal(raw, []byte(`<input disabled="" type="checkbox">`)) ||
		bytes.Equal(raw, []byte(`<input type="checkbox" disabled="">`)) ||
		bytes.Equal(raw, []byte(`<input checked="" disabled="" type="checkbox">`))
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
