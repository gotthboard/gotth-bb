package discovery

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"golang.org/x/text/unicode/norm"
)

const (
	MaximumSearchRawQueryBytes = 2_048
	MaximumSearchQueryBytes    = 256
	MaximumActivityRawBytes    = 256
	EncodedCursorLength        = 103
)

var maximumSearchTime = time.Date(9999, 12, 31, 23, 59, 59, 999_999_000, time.UTC)

type SearchRequest struct {
	Query              string
	AuthorID           int64
	AreaSlug           string
	From               time.Time
	ToExclusive        time.Time
	ToInclusiveMaximum bool
	Page               uint8
}

// ParseSearchRequest closes the complete search query-string grammar before
// session or database work. The returned dates are UTC and the upper endpoint
// is exclusive except for the explicitly marked maximum finite timestamp.
//
// Complexity: for n <= 2,048 raw/decoded bytes, time and auxiliary space are
// O(n), Omega(1); valid nonempty input is tight Theta(n) because URL decoding,
// validation, trimming, and NFC normalization each inspect bounded input.
func ParseSearchRequest(raw string) (SearchRequest, error) {
	if len(raw) == 0 || len(raw) > MaximumSearchRawQueryBytes || !validRawQuery(raw) {
		return SearchRequest{}, fmt.Errorf("invalid search request")
	}
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) == 0 {
		return SearchRequest{}, fmt.Errorf("invalid search request")
	}
	allowed := map[string]bool{"q": true, "author": true, "area": true, "from": true, "to": true, "page": true}
	for key, entries := range values {
		if !allowed[key] || len(entries) != 1 || !validDecoded(entries[0]) {
			return SearchRequest{}, fmt.Errorf("invalid search request")
		}
	}
	request := SearchRequest{Page: 1}
	if query, present := single(values, "q"); present {
		request.Query = norm.NFC.String(trimUnicode15WhiteSpace(query))
		if len(request.Query) == 0 || len(request.Query) > MaximumSearchQueryBytes {
			return SearchRequest{}, fmt.Errorf("invalid search request")
		}
	}
	if author, present := single(values, "author"); present {
		request.AuthorID, err = parseCanonicalPositiveInt64(author)
		if err != nil {
			return SearchRequest{}, fmt.Errorf("invalid search request")
		}
	}
	if request.Query == "" && request.AuthorID == 0 {
		return SearchRequest{}, fmt.Errorf("invalid search request")
	}
	if area, present := single(values, "area"); present {
		if !policy.ValidAreaSlug(area) {
			return SearchRequest{}, fmt.Errorf("invalid search request")
		}
		request.AreaSlug = area
	}
	if from, present := single(values, "from"); present {
		request.From, err = parseSearchDate(from)
		if err != nil {
			return SearchRequest{}, fmt.Errorf("invalid search request")
		}
	}
	if to, present := single(values, "to"); present {
		parsed, parseErr := parseSearchDate(to)
		if parseErr != nil {
			return SearchRequest{}, fmt.Errorf("invalid search request")
		}
		if parsed.Year() == 9999 && parsed.Month() == time.December && parsed.Day() == 31 {
			request.ToExclusive = maximumSearchTime
			request.ToInclusiveMaximum = true
		} else {
			request.ToExclusive = parsed.AddDate(0, 0, 1)
		}
	}
	if !request.From.IsZero() && !request.ToExclusive.IsZero() {
		lastAllowed := request.ToExclusive
		if request.ToInclusiveMaximum {
			lastAllowed = lastAllowed.Add(time.Microsecond)
		}
		if !request.From.Before(lastAllowed) {
			return SearchRequest{}, fmt.Errorf("invalid search request")
		}
	}
	if page, present := single(values, "page"); present {
		parsed, parseErr := parseCanonicalPositiveInt64(page)
		if parseErr != nil || parsed > 2 {
			return SearchRequest{}, fmt.Errorf("invalid search request")
		}
		request.Page = uint8(parsed)
	}
	return request, nil
}

// ParseActivityCursorParameter closes the absent-or-one-cursor grammar. Cursor
// authentication remains the codec's separate responsibility.
//
// Complexity: for n <= 256 bytes, time and auxiliary space are O(n), Omega(1),
// and tight Theta(n) for a valid cursor because URL decoding inspects it once.
func ParseActivityCursorParameter(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > MaximumActivityRawBytes || !validRawQuery(raw) {
		return "", fmt.Errorf("invalid activity request")
	}
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) != 1 {
		return "", fmt.Errorf("invalid activity request")
	}
	entries, present := values["cursor"]
	if !present || len(entries) != 1 || len(entries[0]) != EncodedCursorLength || !validDecoded(entries[0]) {
		return "", fmt.Errorf("invalid activity request")
	}
	return entries[0], nil
}

// validRawQuery rejects ambiguous separators and wire-level control bytes.
//
// Complexity: for n bytes, time is tight Theta(n) and auxiliary space is
// tight Theta(1).
func validRawQuery(raw string) bool {
	if strings.Contains(raw, ";") || strings.HasPrefix(raw, "&") || strings.HasSuffix(raw, "&") || strings.Contains(raw, "&&") {
		return false
	}
	for _, character := range raw {
		if character == 0 || character < 0x20 || character == 0x7f {
			return false
		}
	}
	return utf8.ValidString(raw)
}

// validDecoded rejects invalid UTF-8 and decoded control characters.
//
// Complexity: for n bytes, time is tight Theta(n) and auxiliary space is
// tight Theta(1).
func validDecoded(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character == 0 || character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

// single returns the sole value after the caller has closed multiplicity.
//
// Complexity: expected time and auxiliary space are tight Theta(1).
func single(values url.Values, key string) (string, bool) {
	entries, present := values[key]
	if !present {
		return "", false
	}
	return entries[0], true
}

// parseCanonicalPositiveInt64 rejects signs, zero, and alternate spellings.
//
// Complexity: for n digits, time is tight Theta(n) and auxiliary space is
// tight Theta(1).
func parseCanonicalPositiveInt64(raw string) (int64, error) {
	if raw == "" || raw[0] == '0' {
		return 0, fmt.Errorf("invalid positive integer")
	}
	for _, character := range raw {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("invalid positive integer")
		}
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid positive integer")
	}
	return parsed, nil
}

// parseSearchDate accepts one exact Gregorian YYYY-MM-DD spelling.
//
// Complexity: time and auxiliary space are tight Theta(1) over ten bytes.
func parseSearchDate(raw string) (time.Time, error) {
	if len(raw) != len("2006-01-02") || raw[4] != '-' || raw[7] != '-' {
		return time.Time{}, fmt.Errorf("invalid date")
	}
	parsed, err := time.Parse("2006-01-02", raw)
	if err != nil || parsed.Format("2006-01-02") != raw {
		return time.Time{}, fmt.Errorf("invalid date")
	}
	return parsed.UTC(), nil
}

// trimUnicode15WhiteSpace trims the contract-pinned Unicode 15 White_Space
// repertoire without depending on host Unicode table drift.
//
// Complexity: for n bytes, time is O(n), Omega(1), and tight Theta(n) when
// both edges contain whitespace; auxiliary space is tight Theta(1).
func trimUnicode15WhiteSpace(value string) string {
	return strings.TrimFunc(value, isUnicode15WhiteSpace)
}

// isUnicode15WhiteSpace recognizes the fixed Unicode 15 White_Space set.
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
