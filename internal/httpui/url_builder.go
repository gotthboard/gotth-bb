package httpui

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"git.dannyhunn.com/agents/gotth-bb/internal/config"
)

// URLBuilder owns the canonical browser-facing path prefix.
type URLBuilder struct {
	basePath      string
	publicBaseURL url.URL
	initialized   bool
}

// NewURLBuilder constructs one builder only when the public base URL and
// browser path prefix form the same validated immutable authority.
//
// Complexity: for n public-URL bytes and m base-path bytes, construction is
// O(n+m) worst-case, Omega(1) on an early validation failure, and tight
// Theta(n+m) for valid input. Auxiliary space is O(n+m), delegated to URL
// serialization and config.ParsePublicBaseURL validation.
func NewURLBuilder(publicBaseURL url.URL, basePath string) (URLBuilder, error) {
	canonicalPublicBaseURL, err := config.ParsePublicBaseURL(publicBaseURL.String(), basePath, false)
	if err != nil {
		return URLBuilder{}, err
	}

	return URLBuilder{basePath: basePath, publicBaseURL: canonicalPublicBaseURL, initialized: true}, nil
}

// Path builds a browser-facing path from individually escaped route segments.
// Callers pass path segments, never a preassembled or absolute URL.
//
// Complexity: for k segments containing n total bytes, construction is
// O(k+n) worst-case, Ω(1) for an immediately invalid segment, and Θ(k+n)
// for valid input. Auxiliary space is O(k+n). url.PathEscape is linear in each
// segment and may expand every byte to a three-byte percent escape.
func (builder URLBuilder) Path(segments ...string) (string, error) {
	if !builder.initialized {
		return "", fmt.Errorf("URL builder is not initialized")
	}
	for index, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("path segment %d is empty or ambiguous", index)
		}
	}

	var path strings.Builder
	path.WriteString(builder.basePath)
	path.WriteByte('/')

	for index, segment := range segments {
		if index > 0 {
			path.WriteByte('/')
		}
		path.WriteString(url.PathEscape(segment))
	}

	return path.String(), nil
}

// Absolute builds a canonical browser URL from the configured public origin
// and individually escaped route segments. Incoming request headers never
// participate in this authority.
//
// Complexity: for k segments containing n total bytes, time and auxiliary
// space are O(k+n), Omega(1), and tight Theta(k+n) for valid input. Path owns
// segment validation/escaping; url.Parse decodes the internally constructed
// path once so url.URL can preserve encoded separators without double escaping.
func (builder URLBuilder) Absolute(segments ...string) (string, error) {
	browserPath, err := builder.Path(segments...)
	if err != nil {
		return "", err
	}
	parsedPath, err := url.Parse(browserPath)
	if err != nil {
		return "", fmt.Errorf("parse constructed browser path: %w", err)
	}
	absolute := builder.publicBaseURL
	absolute.Path = parsedPath.Path
	absolute.RawPath = parsedPath.RawPath
	return absolute.String(), nil
}

// PathWithQuery builds a browser-facing path and encodes query keys and values
// without allowing either to alter the path, fragment, or public authority.
//
// Complexity: for k segments containing n bytes, q query keys, and v query
// bytes, time is O(k+n+v+q*log(q)), Omega(1); auxiliary space is O(k+n+v+q).
// The q*log(q) term is url.Values.Encode's deterministic key sort.
func (builder URLBuilder) PathWithQuery(segments []string, query url.Values) (string, error) {
	browserPath, err := builder.Path(segments...)
	if err != nil {
		return "", err
	}
	encoded := query.Encode()
	if encoded == "" {
		return browserPath, nil
	}
	return browserPath + "?" + encoded, nil
}

// CookiePath returns the application root with a trailing slash so the session
// cookie matches the configured subtree without also matching sibling paths
// that merely share the BASE_PATH byte prefix.
//
// Complexity: time and auxiliary space are tight Theta(1), delegated to Path
// with no variable route segments.
func (builder URLBuilder) CookiePath() (string, error) {
	return builder.Path()
}

// ValidateReturnPath proves that one untrusted browser return target is a
// canonical path inside this builder's configured application subtree. It
// returns the original bytes only after rejecting absolute/network URLs,
// fragments, traversal or empty segments, encoded separators, noncanonical
// path/query encoding, and values outside the database byte bound.
//
// Complexity: for n input bytes and q query keys, time is O(n+q*log(q)),
// Omega(1), and auxiliary space is O(n+q). Both are bounded by the 2,048-byte
// input limit; url.ParseQuery owns query allocation and deterministic sorting.
func (builder URLBuilder) ValidateReturnPath(raw string) (string, error) {
	if !builder.initialized {
		return "", fmt.Errorf("URL builder is not initialized")
	}
	if len(raw) == 0 || len(raw) > 2048 || !utf8.ValidString(raw) {
		return "", fmt.Errorf("return path has an invalid size or encoding")
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return "", fmt.Errorf("return path is not a valid request URI")
	}
	if parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("return path must not contain an external authority or fragment")
	}
	if parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") || strings.Contains(parsed.Path, `\`) || strings.IndexFunc(parsed.Path, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("return path must be an internal absolute path")
	}
	segments := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	for index, segment := range segments {
		lastTrailingSegment := index == len(segments)-1 && segment == ""
		if (segment == "" && !lastTrailingSegment) || segment == "." || segment == ".." {
			return "", fmt.Errorf("return path contains an ambiguous segment")
		}
	}
	canonicalPath := (&url.URL{Path: parsed.Path}).EscapedPath()
	rawPath, rawQuery, hasQuery := strings.Cut(raw, "?")
	if rawPath != canonicalPath {
		return "", fmt.Errorf("return path encoding is not canonical")
	}
	if hasQuery != (parsed.RawQuery != "" || parsed.ForceQuery) || parsed.ForceQuery {
		return "", fmt.Errorf("return path query is not canonical")
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil || query.Encode() != rawQuery {
		return "", fmt.Errorf("return path query is not canonical")
	}
	for key, values := range query {
		if strings.IndexFunc(key, unicode.IsControl) >= 0 {
			return "", fmt.Errorf("return path query contains a control character")
		}
		for _, value := range values {
			if strings.IndexFunc(value, unicode.IsControl) >= 0 {
				return "", fmt.Errorf("return path query contains a control character")
			}
		}
	}
	if builder.basePath != "" && parsed.Path != builder.basePath && !strings.HasPrefix(parsed.Path, builder.basePath+"/") {
		return "", fmt.Errorf("return path escapes the configured base path")
	}
	return raw, nil
}
