package render

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

const (
	MaximumMarkdownBytes     = 65_536
	MaximumRenderedHTMLBytes = 262_144
	RendererVersion          = "goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2"
	LegacyRendererVersion    = "goldmark-v1.8.5-bluemonday-v1.0.27-p1"
)

// ErrRenderedHTMLTooLarge identifies the sole render failure for which the
// release migration may preserve an exact, independently verified p1 result.
// All other render failures remain fatal.
var ErrRenderedHTMLTooLarge = errors.New("rendered Markdown exceeds the persistence limit")

var commonMarkRenderer = goldmark.New(goldmark.WithExtensions(
	extension.NewLinkify(extension.WithLinkifyAllowedProtocols([]string{"http:", "https:"})),
	extension.NewTable(extension.WithTableCellAlignMethod(extension.TableCellAlignNone)),
	extension.Strikethrough,
	extension.TaskList,
))

var legacyCommonMarkRenderer = goldmark.New()

// RenderedMarkdown is one validated, rendered, and sanitized forum body. Its
// private representation prevents callers from forging persistence or trusted
// presentation values without crossing RenderMarkdown. The zero value is safe
// for presentation but cannot be persisted.
type RenderedMarkdown struct {
	html string
}

// RenderMarkdown validates bounded canonical source, renders plain CommonMark
// as GitHub Flavored Markdown with Goldmark's raw-HTML/unsafe-link protections
// left enabled, and applies the forum's narrow sanitizer before returning any
// persistence value.
//
// Complexity: for n <= 65,536 source bytes and h rendered bytes, time is
// O(n+h), Omega(1), and auxiliary/returned space is O(n+h), Omega(1), owned by
// Goldmark's AST, the render buffer, and sanitizer output. Work is locally
// bounded; no I/O, retry, cache mutation, or background work occurs.
func RenderMarkdown(source string) (RenderedMarkdown, error) {
	if err := validateMarkdownSource(source); err != nil {
		return RenderedMarkdown{}, fmt.Errorf("Markdown source has an invalid size, encoding, or content")
	}
	var rendered bytes.Buffer
	if err := commonMarkRenderer.Convert([]byte(source), &rendered); err != nil {
		return RenderedMarkdown{}, fmt.Errorf("render Markdown: %w", err)
	}
	sanitized := SanitizeHTML(rendered.String()).html
	if len(sanitized) > MaximumRenderedHTMLBytes {
		return RenderedMarkdown{}, fmt.Errorf("%w: %d bytes exceeds %d", ErrRenderedHTMLTooLarge, len(sanitized), MaximumRenderedHTMLBytes)
	}
	if len(sanitized) == 0 || strings.TrimSpace(sanitized) == "" {
		return RenderedMarkdown{}, fmt.Errorf("rendered Markdown has an invalid size or content")
	}
	return RenderedMarkdown{html: sanitized}, nil
}

// ValidateLegacyRenderedHTML proves that source and persisted HTML are the
// exact application output of the admitted p1 Goldmark/Bluemonday policy. It
// exists only for the release migration's content-preserving compatibility
// path; it does not manufacture a persistence value or renderer marker.
//
// Complexity: for n <= 65,536 source bytes and h <= 262,144 rendered bytes,
// time and auxiliary space are O(n+h), Omega(1). No I/O or shared mutation
// occurs.
func ValidateLegacyRenderedHTML(source, persistedHTML string) error {
	if err := validateMarkdownSource(source); err != nil {
		return fmt.Errorf("legacy Markdown source is not application-valid: %w", err)
	}
	var rendered bytes.Buffer
	if err := legacyCommonMarkRenderer.Convert([]byte(source), &rendered); err != nil {
		return fmt.Errorf("render legacy Markdown: %w", err)
	}
	want := sanitizeLegacyHTML(rendered.String())
	if len(want) == 0 || len(want) > MaximumRenderedHTMLBytes || strings.TrimSpace(want) == "" {
		return fmt.Errorf("legacy rendered Markdown is not application-valid")
	}
	if persistedHTML != want {
		return fmt.Errorf("persisted legacy HTML does not match the admitted p1 renderer")
	}
	return nil
}

func validateMarkdownSource(source string) error {
	if len(source) == 0 || len(source) > MaximumMarkdownBytes || !utf8.ValidString(source) || strings.TrimSpace(source) == "" {
		return fmt.Errorf("invalid size, encoding, or content")
	}
	return nil
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
