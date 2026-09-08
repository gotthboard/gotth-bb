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
		`<h1>one</h1><h2>two</h2><h3>three</h3><h4>four</h4><h5>five</h5><h6>six</h6><hr>` +
		`<ul><li>one</li></ul><ol><li>two</li></ol>` +
		`<blockquote>quote</blockquote><pre><code>if x &lt; y</code></pre>` +
		`<p><a href="/bb/topics/7">local</a> <a href="https://example.org/read">external</a><br>done</p>` +
		`<del>gone</del><table><thead><tr><th>head</th></tr></thead><tbody><tr><td>cell</td></tr></tbody></table>` +
		`<input disabled="" type="checkbox"><input checked="" disabled="" type="checkbox">`

	want := `<p>Hello <em>careful</em> <strong>world</strong> 👋</p>` +
		`<h1>one</h1><h2>two</h2><h3>three</h3><h4>four</h4><h5>five</h5><h6>six</h6><hr>` +
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
		`<h1 id="x" style="color:red" onclick="alert(0)">heading</h1><hr id="rule" style="display:none">` +
		`<p id="x" class="y" style="color:red" onclick="alert(2)">safe` +
		`<img src="https://example.org/tracker.png"><iframe src="https://example.org"></iframe>` +
		`<table style="color:red" onclick="alert(3)"><tr><td colspan="2">cell</td></tr></table>` +
		`<input type="text" disabled="" name="stolen"><input type="checkbox" onclick="alert(4)"></p>`

	if got, want := SanitizeHTML(raw).html, `<h1>heading</h1><hr><p>safe<table><tr><td>cell</td></tr></table></p>`; got != want {
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

func TestSanitizeHTMLRejectsMalformedTaskInputsWithoutEatingFollowingText(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		`<input type="checkbox" disabled="`,
		`<inp`,
		`<input type="checkbox" disabled="" disabled="">`,
		`<input type="checkbox" type="checkbox" disabled="">`,
		`<input checked="" checked="" disabled="" type="checkbox">`,
		`<input type="checkbox" disabled>`,
		`<input type="checkbox" disabled=""/>`,
		`<input  type="checkbox" disabled="">`,
		`<INPUT TYPE="checkbox" DISABLED="">`,
	} {
		if got := SanitizeHTML(raw).html; strings.Contains(got, "<input") {
			t.Fatalf("malformed task input %q survived as %q", raw, got)
		}
	}
	if got, want := SanitizeHTML(`<input type="checkbox" disabled="">following`).html, `<input type="checkbox" disabled="">following`; got != want {
		t.Fatalf("task input trailing text = %q, want %q", got, want)
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

func TestSanitizeHTMLPreservesEscapedLinkQueryExactly(t *testing.T) {
	t.Parallel()

	raw := `<p><a href="https://example.org/a?x=1&amp;y=2">https://example.org/a?x=1&amp;y=2</a></p>`
	want := `<p><a href="https://example.org/a?x=1&amp;y=2" rel="nofollow noreferrer">https://example.org/a?x=1&amp;y=2</a></p>`
	if got := SanitizeHTML(raw).html; got != want {
		t.Fatalf("sanitized escaped query = %q, want %q", got, want)
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

func TestTrustedHTMLVisibleTextUsesOnlyTextAndDocumentedBoundaries(t *testing.T) {
	t.Parallel()

	trusted := SanitizeHTML(`<h1 title="ignored">Head<strong>ing</strong></h1>` +
		`<p>one<a href="https://secret.example/path">two</a><br>three</p>` +
		`<ul><li>four</li><li>five</li></ul><hr>` +
		`<table><thead><tr><th>six</th><th>seven</th></tr></thead>` +
		`<tbody><tr><td>eight</td><td>nine</td></tr></tbody></table>` +
		`<input checked="" disabled="" type="checkbox">ten<!-- ignored -->`)
	if got, want := trusted.VisibleText(), "Heading onetwo three four five six seven eight nine ten"; got != want {
		t.Fatalf("visible text = %q, want %q", got, want)
	}
}

func TestTrustedHTMLVisibleTextPinsUnicode15WhitespaceAndNFC(t *testing.T) {
	t.Parallel()

	trusted := SanitizeHTML("<p>  Cafe\u0301\t\n\u0085\u00a0\u1680\u2000\u200a\u2028\u2029\u202f\u205f\u3000next\u200bword  </p>")
	if got, want := trusted.VisibleText(), "Café next\u200bword"; got != want {
		t.Fatalf("visible text = %q, want %q", got, want)
	}
}

func TestTrustedHTMLZeroValueHasEmptyVisibleText(t *testing.T) {
	t.Parallel()

	var trusted TrustedHTML
	if got := trusted.VisibleText(); got != "" {
		t.Fatalf("zero visible text = %q, want empty", got)
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
