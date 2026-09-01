package httpui

import (
	"fmt"
	"net/url"
	"strings"

	"git.dannyhunn.com/agents/gotth-bb/internal/config"
)

// URLBuilder owns the canonical browser-facing path prefix.
type URLBuilder struct {
	basePath string
}

// NewURLBuilder constructs a builder only from a valid canonical base path.
//
// Complexity: for n base-path bytes, construction is O(n) worst-case, Ω(1)
// on an early validation failure, and Θ(n) for valid non-empty input.
// Auxiliary space is O(n), delegated to config.ParseBasePath validation.
func NewURLBuilder(basePath string) (URLBuilder, error) {
	canonical, err := config.ParseBasePath(basePath)
	if err != nil {
		return URLBuilder{}, err
	}

	return URLBuilder{basePath: canonical}, nil
}

// Path builds a browser-facing path from individually escaped route segments.
// Callers pass path segments, never a preassembled or absolute URL.
//
// Complexity: for k segments containing n total bytes, construction is
// O(k+n) worst-case, Ω(1) for an immediately invalid segment, and Θ(k+n)
// for valid input. Auxiliary space is O(k+n). url.PathEscape is linear in each
// segment and may expand every byte to a three-byte percent escape.
func (builder URLBuilder) Path(segments ...string) (string, error) {
	for index, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("path segment %d is empty or ambiguous", index)
		}
	}

	var path strings.Builder
	path.Grow(len(builder.basePath) + 1 + escapedPathCapacity(segments))
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

// escapedPathCapacity returns a conservative lower-bound capacity hint; the
// builder grows normally when percent escaping expands the output.
//
// Complexity: for k segments, this is Θ(k) time because Go string length is
// constant-time; auxiliary space is Θ(1).
func escapedPathCapacity(segments []string) int {
	capacity := 0
	for _, segment := range segments {
		capacity += len(segment)
	}
	if len(segments) > 1 {
		capacity += len(segments) - 1
	}
	return capacity
}
