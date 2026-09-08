package render

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gotthboard/gotth-bb/internal/abuse"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

const (
	MaximumMarkdownBytes     = 65_536
	MaximumRenderedHTMLBytes = 262_144
	RendererVersion          = "goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2"
	LegacyRendererVersion    = "goldmark-v1.8.5-bluemonday-v1.0.27-p1"
	SearchProjectionVersion  = "search-v1-pg17-simple-u15-p2"
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
	html       string
	searchText string
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
	return renderMarkdown(source, nil)
}

// RenderMarkdownForPublication validates one explicit destination policy,
// parses the admitted GFM document once, checks every resolved link, image,
// and automatic-link destination, then renders that same AST.
//
// Complexity: for n <= 65,536 source bytes, h rendered bytes, d destination
// bytes, and the fixed policy maximum r <= 256, time is O(n+h+d*r), Omega(1),
// and auxiliary/returned space is O(n+h+d), Omega(1). No I/O, DNS, network,
// retry, cache mutation, or background work occurs.
func RenderMarkdownForPublication(source string, policy abuse.DestinationPolicy) (RenderedMarkdown, error) {
	if !policy.Valid() {
		return RenderedMarkdown{}, fmt.Errorf("Markdown destination policy is invalid")
	}
	return renderMarkdown(source, &policy)
}

func renderMarkdown(source string, destinationPolicy *abuse.DestinationPolicy) (RenderedMarkdown, error) {
	if err := validateMarkdownSource(source); err != nil {
		return RenderedMarkdown{}, fmt.Errorf("Markdown source has an invalid size, encoding, or content")
	}
	sourceBytes := []byte(source)
	document := commonMarkRenderer.Parser().Parse(text.NewReader(sourceBytes))
	if destinationPolicy != nil {
		if err := ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			var destination []byte
			switch typed := node.(type) {
			case *ast.Link:
				destination = typed.Destination
			case *ast.Image:
				destination = typed.Destination
			case *ast.AutoLink:
				if typed.AutoLinkType == ast.AutoLinkURL {
					destination = typed.URL(sourceBytes)
				}
			}
			if destination == nil {
				return ast.WalkContinue, nil
			}
			return ast.WalkContinue, destinationPolicy.Check(destination)
		}); err != nil {
			return RenderedMarkdown{}, err
		}
	}
	var rendered bytes.Buffer
	if err := commonMarkRenderer.Renderer().Render(&rendered, sourceBytes, document); err != nil {
		return RenderedMarkdown{}, fmt.Errorf("render Markdown: %w", err)
	}
	sanitized := SanitizeHTML(rendered.String())
	if len(sanitized.html) > MaximumRenderedHTMLBytes {
		return RenderedMarkdown{}, fmt.Errorf("%w: %d bytes exceeds %d", ErrRenderedHTMLTooLarge, len(sanitized.html), MaximumRenderedHTMLBytes)
	}
	if len(sanitized.html) == 0 || strings.TrimSpace(sanitized.html) == "" {
		return RenderedMarkdown{}, fmt.Errorf("rendered Markdown has an invalid size or content")
	}
	return RenderedMarkdown{html: sanitized.html, searchText: sanitized.VisibleText()}, nil
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

// SearchProjectionValues returns the inseparable visible text and exact
// projection version only for a value produced by RenderMarkdown.
//
// Complexity: time and auxiliary space are tight Theta(1); returned strings
// share their immutable backing storage and are not copied.
func (rendered RenderedMarkdown) SearchProjectionValues() (string, string, error) {
	if !rendered.valid() {
		return "", "", fmt.Errorf("rendered Markdown is not initialized")
	}
	return rendered.searchText, SearchProjectionVersion, nil
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
