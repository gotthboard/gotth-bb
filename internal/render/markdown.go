package render

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
)

const (
	MaximumMarkdownBytes     = 65_536
	MaximumRenderedHTMLBytes = 262_144
	RendererVersion          = "goldmark-v1.8.5-bluemonday-v1.0.27-p1"
)

var commonMarkRenderer = goldmark.New()

// RenderedMarkdown is one validated, rendered, and sanitized forum body. Its
// private representation prevents callers from forging persistence or trusted
// presentation values without crossing RenderMarkdown. The zero value is safe
// for presentation but cannot be persisted.
type RenderedMarkdown struct {
	html string
}

// RenderMarkdown validates bounded canonical source, renders plain CommonMark
// with Goldmark's raw-HTML/unsafe-link protections left enabled, and applies
// the forum's narrow sanitizer before returning any persistence value.
//
// Complexity: for n <= 65,536 source bytes and h rendered bytes, time is
// O(n+h), Omega(1), and auxiliary/returned space is O(n+h), Omega(1), owned by
// Goldmark's AST, the render buffer, and sanitizer output. Work is locally
// bounded; no I/O, retry, cache mutation, or background work occurs.
func RenderMarkdown(source string) (RenderedMarkdown, error) {
	if len(source) == 0 || len(source) > MaximumMarkdownBytes || !utf8.ValidString(source) || strings.TrimSpace(source) == "" {
		return RenderedMarkdown{}, fmt.Errorf("Markdown source has an invalid size, encoding, or content")
	}
	var rendered bytes.Buffer
	if err := commonMarkRenderer.Convert([]byte(source), &rendered); err != nil {
		return RenderedMarkdown{}, fmt.Errorf("render Markdown: %w", err)
	}
	sanitized := SanitizeHTML(rendered.String()).html
	if len(sanitized) == 0 || len(sanitized) > MaximumRenderedHTMLBytes || strings.TrimSpace(sanitized) == "" {
		return RenderedMarkdown{}, fmt.Errorf("rendered Markdown has an invalid size or content")
	}
	return RenderedMarkdown{html: sanitized}, nil
}

// PersistenceValues returns the inseparable sanitized HTML and renderer
// version only for a value produced by RenderMarkdown.
//
// Complexity: time and auxiliary space are tight Theta(1); returned strings
// share their immutable backing storage and are not copied.
func (rendered RenderedMarkdown) PersistenceValues() (string, string, error) {
	if !rendered.valid() {
		return "", "", fmt.Errorf("rendered Markdown is not initialized")
	}
	return rendered.html, RendererVersion, nil
}

// TrustedHTML converts already-sanitized renderer output to the opaque
// presentation type without repeating parsing or allocation. A zero value
// remains safe and renders empty.
//
// Complexity: time and auxiliary space are tight Theta(1); the immutable HTML
// string header is copied but its backing bytes are shared.
func (rendered RenderedMarkdown) TrustedHTML() TrustedHTML {
	return TrustedHTML{html: rendered.html}
}

// valid reports whether this private value can be persisted.
//
// Complexity: time and auxiliary space are tight Theta(1).
func (rendered RenderedMarkdown) valid() bool {
	return rendered.html != ""
}
