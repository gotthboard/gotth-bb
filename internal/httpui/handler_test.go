package httpui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestPublicShellRendersCompletePageFragmentAndHistoryPage(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t, "/bb")
	tests := []struct {
		name      string
		hxRequest string
		history   string
		wantPage  bool
	}{
		{name: "complete page", wantPage: true},
		{name: "HTMX fragment", hxRequest: "true"},
		{name: "history restoration", hxRequest: "true", history: "true", wantPage: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if test.hxRequest != "" {
				request.Header.Set("HX-Request", test.hxRequest)
			}
			if test.history != "" {
				request.Header.Set("HX-History-Restore-Request", test.history)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body := response.Body.String()
			if request.Pattern != "GET /" {
				t.Fatalf("request pattern = %q, want GET /", request.Pattern)
			}
			if response.Code != http.StatusOK || !strings.Contains(body, `id="main-content"`) || !strings.Contains(body, "No public discussion areas are available yet") {
				t.Fatalf("response = (%d, %q)", response.Code, body)
			}
			if got := strings.HasPrefix(body, "<!doctype html>"); got != test.wantPage {
				t.Fatalf("complete page = %v, want %v", got, test.wantPage)
			}
			if test.wantPage {
				for _, required := range []string{
					`rel="canonical" href="https://forum.example.test/bb/"`,
					`href="/bb/static/app-1.0.0-alpha.1.css"`,
					`src="/bb/static/htmx-2.0.10.min.js"`,
					`name="htmx-config"`,
				} {
					if !strings.Contains(body, required) {
						t.Errorf("complete page lacks %s", required)
					}
				}
				var parsedConfig map[string]any
				if err := json.Unmarshal([]byte(findMetaContent(t, body, "htmx-config")), &parsedConfig); err != nil {
					t.Fatalf("rendered HTMX configuration is invalid JSON: %v", err)
				}
			} else if strings.Contains(body, "<html") || strings.Contains(body, "htmx-config") {
				t.Fatal("fragment contained the document shell")
			}
			if got := response.Header().Get("Content-Security-Policy"); got != browserContentSecurityPolicy {
				t.Fatalf("Content-Security-Policy = %q", got)
			}
		})
	}
}

func TestPublicShellUsesAlternatePrefixForEveryApplicationURL(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t, "/community/board")
	for _, hxRequest := range []string{"", "true"} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("HX-Request", hxRequest)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertPrefixedApplicationURLs(t, response.Body.String(), "/community/board")
		if strings.Contains(response.Body.String(), "/bb/") {
			t.Fatal("rendered shell contains hard-coded /bb path")
		}
	}
}

func TestHealthAndStaticRoutes(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t, "/bb")
	tests := []struct {
		name         string
		method       string
		path         string
		wantStatus   int
		wantType     string
		bodyContains string
	}{
		{name: "liveness", method: http.MethodGet, path: "/health/live", wantStatus: http.StatusOK, wantType: "text/plain; charset=utf-8", bodyContains: "ok\n"},
		{name: "readiness", method: http.MethodGet, path: "/health/ready", wantStatus: http.StatusServiceUnavailable, wantType: "text/plain; charset=utf-8", bodyContains: "not ready\n"},
		{name: "stylesheet", method: http.MethodGet, path: "/static/app-1.0.0-alpha.1.css", wantStatus: http.StatusOK, wantType: "text/css; charset=utf-8", bodyContains: "focus"},
		{name: "HTMX", method: http.MethodGet, path: "/static/htmx-2.0.10.min.js", wantStatus: http.StatusOK, wantType: "text/javascript; charset=utf-8", bodyContains: "htmx"},
		{name: "stylesheet HEAD", method: http.MethodHead, path: "/static/app-1.0.0-alpha.1.css", wantStatus: http.StatusOK, wantType: "text/css; charset=utf-8"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			response := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.path, nil)
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || response.Header().Get("Content-Type") != test.wantType || !strings.Contains(response.Body.String(), test.bodyContains) {
				t.Fatalf("response = (%d, %q, %q)", response.Code, response.Header().Get("Content-Type"), response.Body.String())
			}
			if response.Header().Get("Content-Security-Policy") != browserContentSecurityPolicy {
				t.Fatal("route escaped browser security policy")
			}
			wantPattern := test.method + " " + test.path
			if request.Pattern != wantPattern {
				t.Fatalf("request pattern = %q, want %q", request.Pattern, wantPattern)
			}
		})
	}
}

func TestRouterReturnsFullAndHTMXNotFoundPagesAndStandardsCompliantMethodError(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t, "/bb")
	for _, hxRequest := range []string{"", "true"} {
		request := httptest.NewRequest(http.MethodGet, "/missing", nil)
		request.Header.Set("HX-Request", hxRequest)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		body := response.Body.String()
		if response.Code != http.StatusNotFound || !strings.Contains(body, "Page not found") {
			t.Fatalf("not-found response = (%d, %q)", response.Code, body)
		}
		if gotPage := strings.HasPrefix(body, "<!doctype html>"); gotPage != (hxRequest == "") {
			t.Fatalf("not-found complete page = %v", gotPage)
		}
		if strings.Contains(body, `rel="canonical"`) {
			t.Fatal("not-found page advertised a canonical resource")
		}
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/health/live", nil))
	if response.Code != http.StatusMethodNotAllowed || len(response.Header().Values("Allow")) == 0 {
		t.Fatalf("method response = (%d, Allow %q)", response.Code, response.Header().Values("Allow"))
	}
	if response.Header().Get("Content-Security-Policy") != browserContentSecurityPolicy {
		t.Fatal("method error escaped browser security policy")
	}
}

func TestNewHandlerRejectsInvalidDependencies(t *testing.T) {
	t.Parallel()

	if got, err := NewHandler(URLBuilder{}, emptyAreaIndexLister); err == nil || got != nil {
		t.Fatalf("NewHandler(zero builder) = (%v, %v), want (nil, error)", got, err)
	}
	if got, err := NewHandler(callbackTestURLBuilder(t), nil); err == nil || got != nil {
		t.Fatalf("NewHandler(nil lister) = (%v, %v), want (nil, error)", got, err)
	}
}

func TestPublicPageAndNotFoundHandlersPropagateCommittedWriteFailure(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t, "/bb")
	for _, path := range []string{"/", "/missing"} {
		writer := &failingRenderResponseWriter{header: make(http.Header), cause: errTestResponseWrite}
		recovered := captureHandlerPanic(func() {
			handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, path, nil))
		})
		if !errors.Is(asError(recovered), errTestResponseWrite) {
			t.Fatalf("%s panic = %v, want write cause", path, recovered)
		}
	}
}

func newTestHandler(t *testing.T, basePath string) http.Handler {
	t.Helper()
	publicBase, err := url.Parse("https://forum.example.test" + basePath)
	if err != nil {
		t.Fatalf("url.Parse() returned error: %v", err)
	}
	builder, err := NewURLBuilder(*publicBase, basePath)
	if err != nil {
		t.Fatalf("NewURLBuilder() returned error: %v", err)
	}
	handler, err := NewHandler(builder, emptyAreaIndexLister)
	if err != nil {
		t.Fatalf("NewHandler() returned error: %v", err)
	}
	secured, err := NewBrowserSecurityHandler(handler)
	if err != nil {
		t.Fatalf("NewBrowserSecurityHandler() returned error: %v", err)
	}
	return secured
}

func assertPrefixedApplicationURLs(t *testing.T, body, basePath string) {
	t.Helper()
	document, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("html.Parse() returned error: %v", err)
	}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		for _, attribute := range node.Attr {
			switch attribute.Key {
			case "href", "src", "action", "hx-get", "hx-post":
				if strings.HasPrefix(attribute.Val, "/") && !strings.HasPrefix(attribute.Val, basePath+"/") {
					t.Errorf("%s=%q escapes base path %q", attribute.Key, attribute.Val, basePath)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
}

func findMetaContent(t *testing.T, body, name string) string {
	t.Helper()
	document, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("html.Parse() returned error: %v", err)
	}
	var walk func(*html.Node) string
	walk = func(node *html.Node) string {
		if node.Type == html.ElementNode && node.Data == "meta" {
			matched := false
			content := ""
			for _, attribute := range node.Attr {
				if attribute.Key == "name" && attribute.Val == name {
					matched = true
				}
				if attribute.Key == "content" {
					content = attribute.Val
				}
			}
			if matched {
				return content
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if found := walk(child); found != "" {
				return found
			}
		}
		return ""
	}
	content := walk(document)
	if content == "" {
		t.Fatalf("meta %q has no content", name)
	}
	return content
}

var errTestResponseWrite = errors.New("test response write failed")

func captureHandlerPanic(run func()) (recovered any) {
	defer func() { recovered = recover() }()
	run()
	return nil
}

func asError(value any) error {
	result, _ := value.(error)
	return result
}
