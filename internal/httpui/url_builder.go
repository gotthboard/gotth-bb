package httpui

import (
	"fmt"
	"net/url"
	"strings"

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
