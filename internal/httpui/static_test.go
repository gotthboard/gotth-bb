package httpui

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedStaticAssetsMatchPinnedGeneration(t *testing.T) {
	t.Parallel()

	const stylesheetSHA256 = "3104ce3eede233f21f5a885d4fc547bcc33a24f8249e1d2757f0606a6d958251"
	if want := "app-" + stylesheetSHA256 + ".css"; appStylesheetFilename != want {
		t.Fatalf("stylesheet filename = %q, want content-addressed %q", appStylesheetFilename, want)
	}
	const toolbarSHA256 = "9b94e2d14953039596b28abd1bf40cda34ebc0fcd910204606ca0f3862b36848"
	if want := "markdown-toolbar-" + toolbarSHA256 + ".js"; markdownToolbarFilename != want {
		t.Fatalf("Markdown toolbar filename = %q, want content-addressed %q", markdownToolbarFilename, want)
	}
	const discoveryResponseSHA256 = "83d6c618d951879489e90e83d947a31d4211b8f565604c773346fd1ef0d4152b"
	if want := "discovery-response-" + discoveryResponseSHA256 + ".js"; discoveryResponseFilename != want {
		t.Fatalf("discovery response filename = %q, want content-addressed %q", discoveryResponseFilename, want)
	}

	tests := []struct {
		name       string
		content    []byte
		wantSHA256 string
		contains   string
	}{
		{name: "Tailwind CSS", content: appStylesheet, wantSHA256: stylesheetSHA256, contains: ".focus\\:not-sr-only"},
		{name: "HTMX", content: htmxScript, wantSHA256: "71ea67185bfa8c98c39d31717c6fce5d852370fcdfd129db4543774d3145c0de", contains: "htmx"},
		{name: "Discovery response", content: discoveryResponseScript, wantSHA256: discoveryResponseSHA256, contains: discoveryResponseHeader},
		{name: "Markdown toolbar", content: markdownToolbarScript, wantSHA256: toolbarSHA256, contains: "gotthMarkdownToolbar"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			digest := sha256.Sum256(test.content)
			if got := hex.EncodeToString(digest[:]); got != test.wantSHA256 {
				t.Fatalf("SHA-256 = %s, want %s", got, test.wantSHA256)
			}
			if !strings.Contains(string(test.content), test.contains) {
				t.Fatalf("asset does not contain %q", test.contains)
			}
		})
	}
}

func TestDiscoveryResponseScriptNarrowsErrorSwapOptIn(t *testing.T) {
	t.Parallel()

	script := string(discoveryResponseScript)
	for _, required := range []string{`new Set([400, 404, 503])`, `getResponseHeader(marker) !== "1"`, `detail.shouldSwap = true`, `detail.isError = true`} {
		if !strings.Contains(script, required) {
			t.Fatalf("discovery response script lacks %q", required)
		}
	}
	if !strings.Contains(htmxConfiguration, `{"code":"[45]..","swap":false,"error":true}`) {
		t.Fatalf("global HTMX error policy was widened: %s", htmxConfiguration)
	}
}

func TestEmbeddedStylesheetContainsRuntimeThreadDepthClasses(t *testing.T) {
	t.Parallel()

	stylesheet := string(appStylesheet)
	for _, selector := range []string{
		".thread-depth-2{margin-inline-start:.5rem}",
		".thread-depth-3{margin-inline-start:1rem}",
		".thread-depth-4{margin-inline-start:1.5rem}",
		".thread-depth-5{margin-inline-start:2rem}",
		".thread-depth-6{margin-inline-start:2.5rem}",
		".thread-depth-capped{margin-inline-start:3rem}",
		"@media (min-width:40rem)",
		".thread-depth-capped{margin-inline-start:9rem}",
	} {
		if !strings.Contains(stylesheet, selector) {
			t.Fatalf("stylesheet does not contain runtime thread selector %q", selector)
		}
	}
}

func TestEmbeddedStylesheetContainsDarkForumTheme(t *testing.T) {
	t.Parallel()

	stylesheet := string(appStylesheet)
	for _, selector := range []string{
		".bg-slate-950{",
		".bg-slate-900{",
		".bg-slate-800{",
		".text-slate-200{",
		".text-blue-300{",
		".border-slate-700{",
	} {
		if !strings.Contains(stylesheet, selector) {
			t.Fatalf("stylesheet does not contain dark-theme selector %q", selector)
		}
	}
}

func TestEmbeddedStylesheetContainsEveryClosedBrandTheme(t *testing.T) {
	t.Parallel()

	stylesheet := string(appStylesheet)
	for _, selector := range []string{
		"html[data-brand-theme=cyan]{--color-blue-100:",
		"html[data-brand-theme=emerald]{--color-blue-100:",
		"html[data-brand-theme=amber]{--color-blue-100:",
		"html[data-brand-theme=rose]{--color-blue-100:",
	} {
		if !strings.Contains(stylesheet, selector) {
			t.Fatalf("stylesheet does not contain closed brand-theme selector %q", selector)
		}
	}
}

func TestEmbeddedStylesheetBoundsWideRenderedContent(t *testing.T) {
	t.Parallel()

	stylesheet := string(appStylesheet)
	for _, selector := range []string{
		".rendered-post{overflow-wrap:anywhere}",
		".rendered-post pre,.rendered-post table{max-width:100%;display:block;overflow-x:auto}",
	} {
		if !strings.Contains(stylesheet, selector) {
			t.Fatalf("stylesheet does not contain rendered-content containment selector %q", selector)
		}
	}
}

func TestStaticAssetHandlerServesImmutableGetHeadAndRange(t *testing.T) {
	t.Parallel()

	handler := staticAssetHandler("text/css; charset=utf-8", []byte("0123456789"))
	tests := []struct {
		name       string
		method     string
		rangeValue string
		wantStatus int
		wantBody   string
	}{
		{name: "get", method: http.MethodGet, wantStatus: http.StatusOK, wantBody: "0123456789"},
		{name: "head", method: http.MethodHead, wantStatus: http.StatusOK},
		{name: "range", method: http.MethodGet, rangeValue: "bytes=2-5", wantStatus: http.StatusPartialContent, wantBody: "2345"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(test.method, "/static/test.css", nil)
			if test.rangeValue != "" {
				request.Header.Set("Range", test.rangeValue)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || response.Body.String() != test.wantBody {
				t.Fatalf("response = (%d, %q), want (%d, %q)", response.Code, response.Body.String(), test.wantStatus, test.wantBody)
			}
			if got := response.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
				t.Fatalf("Content-Type = %q", got)
			}
			if got := response.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
				t.Fatalf("Cache-Control = %q", got)
			}
		})
	}
}
