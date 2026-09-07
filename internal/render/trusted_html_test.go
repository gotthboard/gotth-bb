package render

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestSanitizeHTMLAllowsDocumentedMarkup(t *testing.T) {
	t.Parallel()

	raw := `<p>Hello <em>careful</em> <strong>world</strong> 👋</p>` +
		`<ul><li>one</li></ul><ol><li>two</li></ol>` +
		`<blockquote>quote</blockquote><pre><code>if x &lt; y</code></pre>` +
		`<p><a href="/bb/topics/7">local</a> <a href="https://example.org/read">external</a><br>done</p>` +
		`<del>gone</del><table><thead><tr><th>head</th></tr></thead><tbody><tr><td>cell</td></tr></tbody></table>` +
		`<input disabled="" type="checkbox"><input checked="" disabled="" type="checkbox">`

	want := `<p>Hello <em>careful</em> <strong>world</strong> 👋</p>` +
		`<ul><li>one</li></ul><ol><li>two</li></ol>` +
		`<blockquote>quote</blockquote><pre><code>if x &lt; y</code></pre>` +
		`<p><a href="/bb/topics/7" rel="nofollow noreferrer">local</a> <a href="https://example.org/read" rel="nofollow noreferrer">external</a><br>done</p>` +
		`<del>gone</del><table><thead><tr><th>head</th></tr></thead><tbody><tr><td>cell</td></tr></tbody></table>` +
		`<input disabled="" type="checkbox"><input checked="" disabled="" type="checkbox">`

	if got := SanitizeHTML(raw).html; got != want {
		t.Fatalf("sanitized HTML = %q, want %q", got, want)
	}
}

func TestSanitizeHTMLStripsExecutableAndUndocumentedMarkup(t *testing.T) {
	t.Parallel()

	raw := `<script>alert(1)</script><style>body{display:none}</style>` +
		`<p id="x" class="y" style="color:red" onclick="alert(2)">safe` +
		`<img src="https://example.org/tracker.png"><iframe src="https://example.org"></iframe>` +
		`<table style="color:red" onclick="alert(3)"><tr><td colspan="2">cell</td></tr></table>` +
		`<input type="text" disabled="" name="stolen"><input type="checkbox" onclick="alert(4)"></p>`

	if got, want := SanitizeHTML(raw).html, `<p>safe<table><tr><td>cell</td></tr></table></p>`; got != want {
		t.Fatalf("sanitized HTML = %q, want %q", got, want)
	}
}

func TestSanitizeHTMLAcceptsOnlyExactDisabledTaskInputs(t *testing.T) {
	t.Parallel()

	raw := `<input><input type="checkbox"><input type="text" disabled="">` +
		`<input type="checkbox" name="x" value="y" form="z" disabled="">` +
		`<input type="checkbox" disabled=""><input checked="" disabled="" type="checkbox">`
	if got, want := SanitizeHTML(raw).html, `<input type="checkbox" disabled=""><input checked="" disabled="" type="checkbox">`; got != want {
		t.Fatalf("sanitized task inputs = %q, want %q", got, want)
	}
}

func TestSanitizeHTMLRestrictsLinkSchemes(t *testing.T) {
	t.Parallel()

	raw := `<p>` +
		`<a href="javascript:alert(1)">javascript</a>` +
		`<a href="data:text/html,boom">data</a>` +
		`<a href="mailto:user@example.org">mail</a>` +
		`<a href="//example.org/read">scheme-relative</a>` +
		`<a href="///example.org/read">ambiguous-relative</a>` +
		`<a href="http://example.org/read">http</a>` +
		`</p>`

	want := `<p>` +
		`javascript` +
		`data` +
		`<a href="mailto:user@example.org" rel="nofollow noreferrer">mail</a>` +
		`<a href="//example.org/read" rel="nofollow noreferrer">scheme-relative</a>` +
		`<a href="///example.org/read" rel="nofollow noreferrer">ambiguous-relative</a>` +
		`<a href="http://example.org/read" rel="nofollow noreferrer">http</a>` +
		`</p>`

	if got := SanitizeHTML(raw).html; got != want {
		t.Fatalf("sanitized HTML = %q, want %q", got, want)
	}
}

func TestTrustedHTMLComponentRendersOnlySanitizedHTML(t *testing.T) {
	t.Parallel()

	trusted := SanitizeHTML(`<p title="removed">hello &amp; <strong>safe</strong><script>bad</script></p>`)
	var output bytes.Buffer
	if err := trusted.Component().Render(context.Background(), &output); err != nil {
		t.Fatalf("render trusted HTML: %v", err)
	}
	if got, want := output.String(), `<p>hello &amp; <strong>safe</strong></p>`; got != want {
		t.Fatalf("rendered HTML = %q, want %q", got, want)
	}
}

func TestTrustedHTMLZeroValueRendersEmpty(t *testing.T) {
	t.Parallel()

	var trusted TrustedHTML
	var output bytes.Buffer
	if err := trusted.Component().Render(context.Background(), &output); err != nil {
		t.Fatalf("render zero trusted HTML: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("rendered zero value = %q, want empty", output.String())
	}
}

func TestSanitizeHTMLPolicyIsConcurrent(t *testing.T) {
	for index := range 64 {
		index := index
		t.Run(string(rune('A'+index%26)), func(t *testing.T) {
			t.Parallel()
			raw := `<p><a href="https://example.org/` + strings.Repeat("x", index+1) + `">safe</a><script>bad</script></p>`
			trusted := SanitizeHTML(raw)
			if strings.Contains(trusted.html, "script") || !strings.Contains(trusted.html, `rel="nofollow noreferrer"`) {
				t.Fatalf("unsafe or incomplete concurrent result: %q", trusted.html)
			}
		})
	}
}
