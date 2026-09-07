package render

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRenderMarkdownSupportsGFM(t *testing.T) {
	t.Parallel()

	source := "Hello *careful* **world** 👋\n\n" +
		"# Heading one\n\n###### Heading six\n\n---\n\n" +
		"- one\n- two\n\n" +
		"1. first\n2. second\n\n" +
		"> quote\n\n" +
		"`inline`\n\n" +
		"```go\nif x < y {}\n```\n\n" +
		"[local](/bb/topics/1) [external](https://example.org/read)\n\n" +
		"~~removed~~ https://example.org/automatic\n\n" +
		"| Left | Right |\n| --- | --- |\n| one | two |\n\n" +
		"- [ ] pending\n- [x] complete\n"
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
		"<h1>Heading one</h1>", "<h6>Heading six</h6>", "<hr>",
		"<ul>", "<li>one</li>", "<li>two</li>", "<blockquote>", "<p>quote</p>",
		"<ol>", "<li>first</li>", "<li>second</li>",
		"<p><code>inline</code></p>", "<pre><code>if x &lt; y {}\n</code></pre>",
		`<a href="/bb/topics/1" rel="nofollow noreferrer">local</a>`,
		`<a href="https://example.org/read" rel="nofollow noreferrer">external</a>`,
		`<del>removed</del>`,
		`<a href="https://example.org/automatic" rel="nofollow noreferrer">https://example.org/automatic</a>`,
		"<table>", "<thead>", "<tbody>", "<th>Left</th>", "<td>two</td>",
		`<input disabled="" type="checkbox"> pending`,
		`<input checked="" disabled="" type="checkbox"> complete`,
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

func TestRenderMarkdownPreservesCommonMarkHeadingsAndThematicBreaksSafely(t *testing.T) {
	t.Parallel()

	rendered, err := RenderMarkdown("# One <script>bad</script>\n\nTwo\n---\n\n***\n\n### Three {#not-an-id}\n\n#### Four\n\n##### Five\n\n###### Six\n")
	if err != nil {
		t.Fatalf("RenderMarkdown() returned error: %v", err)
	}
	html, _, err := rendered.PersistenceValues()
	if err != nil {
		t.Fatalf("PersistenceValues() returned error: %v", err)
	}
	for _, required := range []string{"<h1>One bad</h1>", "<h2>Two</h2>", "<hr>", "<h3>Three {#not-an-id}</h3>", "<h4>Four</h4>", "<h5>Five</h5>", "<h6>Six</h6>"} {
		if !strings.Contains(html, required) {
			t.Fatalf("rendered HTML lacks %q: %s", required, html)
		}
	}
	for _, forbidden := range []string{"<script", " id=", "onclick", "style="} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("rendered heading HTML contains %q: %s", forbidden, html)
		}
	}
}

func TestRenderMarkdownPreservesAdaptiveToolbarCodeContent(t *testing.T) {
	t.Parallel()

	rendered, err := RenderMarkdown("`` a`b ``\n\n````\nbefore\n```\nafter\n````\n")
	if err != nil {
		t.Fatalf("RenderMarkdown() returned error: %v", err)
	}
	html, _, err := rendered.PersistenceValues()
	if err != nil {
		t.Fatalf("PersistenceValues() returned error: %v", err)
	}
	for _, required := range []string{"<p><code>a`b</code></p>", "<pre><code>before\n```\nafter\n</code></pre>"} {
		if !strings.Contains(html, required) {
			t.Fatalf("adaptive code rendering lacks %q: %s", required, html)
		}
	}
}

func TestRenderMarkdownAcceptsToolbarBoundaryMarkdown(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		source   string
		required string
	}{
		{name: "mid-line fenced block", source: "before \n``````\ncode\n``````\n after", required: "<pre><code>code\n</code></pre>"},
		{name: "mid-line table", source: "before \n| cell | Column 2 |\n| ---- | ---- |\n| Cell 1 | Cell 2 |\n after", required: "<th>cell</th>"},
		{name: "all-space inline code", source: "` `", required: "<p><code> </code></p>"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rendered, err := RenderMarkdown(test.source)
			if err != nil {
				t.Fatalf("RenderMarkdown() returned error: %v", err)
			}
			html, _, err := rendered.PersistenceValues()
			if err != nil || !strings.Contains(html, test.required) {
				t.Fatalf("toolbar boundary rendering = (%q, %v), want containing %q", html, err, test.required)
			}
		})
	}
}

func TestRenderMarkdownAcceptsInlineCodeBesideEscapedAuthoredBackticks(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		source   string
		required string
	}{
		{name: "right", source: "` text `\\`", required: "<p><code>text</code>`</p>"},
		{name: "left", source: "\\`` text `", required: "<p>`<code>text</code></p>"},
		{name: "both", source: "x\\`` text `\\`y", required: "<p>x`<code>text</code>`y</p>"},
		{name: "multiple", source: "x\\`\\`\\`` text `\\`\\`y", required: "<p>x```<code>text</code>``y</p>"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rendered, err := RenderMarkdown(test.source)
			if err != nil {
				t.Fatalf("RenderMarkdown() returned error: %v", err)
			}
			html, _, err := rendered.PersistenceValues()
			if err != nil || !strings.Contains(html, test.required) {
				t.Fatalf("adjacent inline-code rendering = (%q, %v), want containing %q", html, err, test.required)
			}
		})
	}
}

func TestRenderMarkdownAcceptsToolbarStarComposition(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		source   string
		required string
	}{
		{name: "combined", source: "***word***", required: "<p><em><strong>word</strong></em></p>"},
		{name: "authored star before emphasis", source: `\**word*`, required: "<p>*<em>word</em></p>"},
		{name: "authored stars after emphasis", source: `*word*\*\*`, required: "<p><em>word</em>**</p>"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rendered, err := RenderMarkdown(test.source)
			if err != nil {
				t.Fatalf("RenderMarkdown() returned error: %v", err)
			}
			html, _, err := rendered.PersistenceValues()
			if err != nil || !strings.Contains(html, test.required) {
				t.Fatalf("toolbar star composition rendering = (%q, %v), want containing %q", html, err, test.required)
			}
		})
	}
}

func TestRenderMarkdownIsDeterministic(t *testing.T) {
	t.Parallel()

	const source = "| a | b |\n| - | - |\n| ~~x~~ | https://example.org |\n\n- [x] done\n"
	first, err := RenderMarkdown(source)
	if err != nil {
		t.Fatalf("first RenderMarkdown() returned error: %v", err)
	}
	want, _, err := first.PersistenceValues()
	if err != nil {
		t.Fatalf("first PersistenceValues() returned error: %v", err)
	}
	for index := 0; index < 32; index++ {
		next, renderErr := RenderMarkdown(source)
		if renderErr != nil {
			t.Fatalf("render %d returned error: %v", index, renderErr)
		}
		got, _, persistenceErr := next.PersistenceValues()
		if persistenceErr != nil || got != want {
			t.Fatalf("render %d = (%q, %v), want %q", index, got, persistenceErr, want)
		}
	}
}

func TestRenderMarkdownLinkificationRestrictsProtocols(t *testing.T) {
	t.Parallel()

	rendered, err := RenderMarkdown("https://example.org/safe user@example.org ftp://example.org/file [ftp](ftp://example.org/file)")
	if err != nil {
		t.Fatalf("RenderMarkdown() returned error: %v", err)
	}
	html, _, err := rendered.PersistenceValues()
	if err != nil {
		t.Fatalf("PersistenceValues() returned error: %v", err)
	}
	if !strings.Contains(html, `<a href="https://example.org/safe" rel="nofollow noreferrer">https://example.org/safe</a>`) {
		t.Fatalf("rendered HTML lost HTTPS linkification: %s", html)
	}
	if strings.Contains(html, `href="ftp:`) || !strings.Contains(html, "ftp://example.org/file") {
		t.Fatalf("rendered HTML promoted forbidden FTP URL: %s", html)
	}
	if !strings.Contains(html, `<a href="mailto:user@example.org" rel="nofollow noreferrer">user@example.org</a>`) {
		t.Fatalf("rendered HTML lost GFM email linkification: %s", html)
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

	tableHeader := "|a|b|c|d|\n|-|-|-|-|\n"
	tableRow := "|x|x|x|x|\n"
	tableRows := (MaximumMarkdownBytes - len(tableHeader)) / len(tableRow)
	fixtures := map[string]string{
		"dense checked tasks": strings.Repeat("- [x]\n", MaximumMarkdownBytes/len("- [x]\n")),
		"dense table":         tableHeader + strings.Repeat(tableRow, tableRows),
	}
	for name, source := range fixtures {
		name, source := name, source
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var legacy bytes.Buffer
			if err := legacyCommonMarkRenderer.Convert([]byte(source), &legacy); err != nil {
				t.Fatalf("legacy renderer returned error: %v", err)
			}
			legacyHTML := sanitizeLegacyHTML(legacy.String())
			if len(legacyHTML) == 0 || len(legacyHTML) > MaximumRenderedHTMLBytes {
				t.Fatalf("legacy rendered bytes = %d, want 1..%d", len(legacyHTML), MaximumRenderedHTMLBytes)
			}
			rendered, err := RenderMarkdown(source)
			if !errors.Is(err, ErrRenderedHTMLTooLarge) || rendered.valid() {
				t.Fatalf("RenderMarkdown(legacy-valid dense fixture) = (%+v, %v), want oversized sentinel", rendered, err)
			}
			if err := ValidateLegacyRenderedHTML(source, legacyHTML); err != nil {
				t.Fatalf("ValidateLegacyRenderedHTML(exact) returned error: %v", err)
			}
			if err := ValidateLegacyRenderedHTML(source, legacyHTML+"tampered"); err == nil {
				t.Fatal("ValidateLegacyRenderedHTML() accepted noncanonical persisted HTML")
			}
		})
	}
}

func TestRenderedHTMLTooLargeSentinelIsNarrow(t *testing.T) {
	t.Parallel()

	if rendered, err := RenderMarkdown(""); err == nil || errors.Is(err, ErrRenderedHTMLTooLarge) || rendered.valid() {
		t.Fatalf("invalid source = (%+v, %v), want non-size failure", rendered, err)
	}
	if err := ValidateLegacyRenderedHTML("", "<p>x</p>\n"); err == nil || errors.Is(err, ErrRenderedHTMLTooLarge) {
		t.Fatalf("invalid legacy source error = %v, want non-size failure", err)
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
