package render

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRenderMarkdownSupportsOnlyAlphaFormatting(t *testing.T) {
	t.Parallel()

	source := "Hello *careful* **world** 👋\n\n" +
		"- one\n- two\n\n" +
		"1. first\n2. second\n\n" +
		"> quote\n\n" +
		"`inline`\n\n" +
		"```go\nif x < y {}\n```\n\n" +
		"[local](/bb/topics/1) [external](https://example.org/read)\n"
	rendered, err := RenderMarkdown(source)
	if err != nil {
		t.Fatalf("RenderMarkdown() returned error: %v", err)
	}
	html, version, err := rendered.PersistenceValues()
	if err != nil {
		t.Fatalf("PersistenceValues() returned error: %v", err)
	}
	for _, required := range []string{
		`<p>Hello <em>careful</em> <strong>world</strong> 👋</p>`,
		"<ul>", "<li>one</li>", "<li>two</li>", "<blockquote>", "<p>quote</p>",
		"<ol>", "<li>first</li>", "<li>second</li>",
		"<p><code>inline</code></p>", "<pre><code>if x &lt; y {}\n</code></pre>",
		`<a href="/bb/topics/1" rel="nofollow noreferrer">local</a>`,
		`<a href="https://example.org/read" rel="nofollow noreferrer">external</a>`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("rendered HTML lacks %q: %s", required, html)
		}
	}
	if version != RendererVersion {
		t.Fatalf("renderer version = %q, want %q", version, RendererVersion)
	}
	var output bytes.Buffer
	if err := rendered.TrustedHTML().Component().Render(context.Background(), &output); err != nil {
		t.Fatalf("render trusted Markdown: %v", err)
	}
	if output.String() != html {
		t.Fatalf("trusted HTML = %q, want persisted %q", output.String(), html)
	}
}

func TestRenderMarkdownDisablesRawHTMLAndUnsafeLinks(t *testing.T) {
	t.Parallel()

	rendered, err := RenderMarkdown("safe <script>alert(1)</script> text\n\n[javascript](javascript:alert(2)) [data](data:text/html,boom)")
	if err != nil {
		t.Fatalf("RenderMarkdown() returned error: %v", err)
	}
	html, _, err := rendered.PersistenceValues()
	if err != nil {
		t.Fatalf("PersistenceValues() returned error: %v", err)
	}
	for _, forbidden := range []string{"<script", "javascript:", "data:text", "href=\"\""} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("rendered HTML contains %q: %s", forbidden, html)
		}
	}
	for _, required := range []string{"safe", "alert(1)", "text", "javascript", "data"} {
		if !strings.Contains(html, required) {
			t.Fatalf("rendered HTML lost safe text %q: %s", required, html)
		}
	}
}

func TestRenderMarkdownRejectsInvalidSource(t *testing.T) {
	t.Parallel()

	invalidUTF8 := string([]byte{'o', 'k', 0xff})
	if utf8.ValidString(invalidUTF8) {
		t.Fatal("test input unexpectedly valid UTF-8")
	}
	for _, source := range []string{
		"",
		" \n\t ",
		invalidUTF8,
		strings.Repeat("x", MaximumMarkdownBytes+1),
		"<script>alert(1)</script>",
	} {
		source := source
		t.Run("invalid", func(t *testing.T) {
			t.Parallel()
			if got, err := RenderMarkdown(source); err == nil || got.valid() {
				t.Fatalf("RenderMarkdown(invalid) = (%+v, %v), want invalid/error", got, err)
			}
		})
	}
}

func TestRenderMarkdownAcceptsMaximumSource(t *testing.T) {
	t.Parallel()

	rendered, err := RenderMarkdown(strings.Repeat("x", MaximumMarkdownBytes))
	if err != nil || !rendered.valid() {
		t.Fatalf("RenderMarkdown(maximum) = (%+v, %v), want valid/nil", rendered, err)
	}
	html, version, err := rendered.PersistenceValues()
	if err != nil || len(html) > MaximumRenderedHTMLBytes || version != RendererVersion {
		t.Fatalf("maximum persistence = (HTML bytes %d, version %q, error %v)", len(html), version, err)
	}
}

func TestRenderMarkdownRejectsExpandedOutputBeyondSchemaBound(t *testing.T) {
	t.Parallel()

	source := strings.Repeat("*\n", MaximumMarkdownBytes/2)
	rendered, err := RenderMarkdown(source)
	if err == nil || rendered.valid() {
		html, _, _ := rendered.PersistenceValues()
		t.Fatalf("expanded RenderMarkdown() = (HTML bytes %d, %v), want invalid/error", len(html), err)
	}
}

func TestRenderedMarkdownZeroValueCannotPersistAndRendersEmpty(t *testing.T) {
	t.Parallel()

	var rendered RenderedMarkdown
	if html, version, err := rendered.PersistenceValues(); err == nil || html != "" || version != "" {
		t.Fatalf("zero PersistenceValues() = (%q, %q, %v), want empty/empty/error", html, version, err)
	}
	var output bytes.Buffer
	if err := rendered.TrustedHTML().Component().Render(context.Background(), &output); err != nil || output.Len() != 0 {
		t.Fatalf("zero trusted HTML = (%q, %v), want empty/nil", output.String(), err)
	}
}

func TestRenderMarkdownIsConcurrent(t *testing.T) {
	for index := range 64 {
		index := index
		t.Run(string(rune('A'+index%26)), func(t *testing.T) {
			t.Parallel()
			source := strings.Repeat("text ", index+1) + "**safe** <script>bad</script>"
			rendered, err := RenderMarkdown(source)
			if err != nil {
				t.Fatalf("RenderMarkdown() returned error: %v", err)
			}
			html, _, err := rendered.PersistenceValues()
			if err != nil || strings.Contains(html, "script") || !strings.Contains(html, "<strong>safe</strong>") {
				t.Fatalf("concurrent result = (%q, %v)", html, err)
			}
		})
	}
}
