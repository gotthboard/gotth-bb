//go:build integration

package httpui

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/discovery"
	"github.com/gotthboard/gotth-bb/internal/forum"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestDiscoveryBrowserThroughCaddy(t *testing.T) {
	for _, test := range []struct {
		name     string
		basePath string
	}{
		{name: "empty base path"},
		{name: "nonempty base path", basePath: "/bb"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runDiscoveryBrowserThroughCaddy(t, test.basePath)
		})
	}
}

func runDiscoveryBrowserThroughCaddy(t *testing.T, basePath string) {
	if os.Getenv("GOTTH_BB_BROWSER_CADDY") != "1" {
		t.Skip("set GOTTH_BB_BROWSER_CADDY=1 on the designated evidence host")
	}
	caddy, err := exec.LookPath("caddy")
	if err != nil {
		t.Fatalf("locate Caddy: %v", err)
	}
	chromium, err := exec.LookPath("chromium")
	if err != nil {
		t.Fatalf("locate Chromium: %v", err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("locate Node.js: %v", err)
	}
	port := reserveDiscoveryTestPort(t)
	publicBase := fmt.Sprintf("http://127.0.0.1:%d%s", port, basePath)
	builder := mustAbsoluteURLBuilder(t, publicBase, basePath)
	now := pgtype.Timestamptz{Time: time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC), Valid: true}
	inner, err := newDiscoveryHandler(builder, DiscoveryHTTPServices{
		Search: func(_ context.Context, request discovery.SearchRequest, access auth.AccessContext) (discovery.SearchPage, error) {
			if request.AuthorID != 2 || !access.Valid() {
				return discovery.SearchPage{}, fmt.Errorf("browser search input is invalid")
			}
			return discovery.SearchPage{Results: []discovery.SearchResult{{
				Kind: "post", ID: 9, PostID: 9, AreaID: 4, AreaSlug: "general", AreaName: "General",
				TopicID: 3, TopicTitle: "Browser evidence", AuthorID: 2, AuthorName: "Alice", CreatedAt: now,
				Excerpt: "A safe browser-visible excerpt.",
			}}}, nil
		},
		Activity: unavailableActivityService, DirectPost: unavailableDirectPostService,
	})
	if err != nil {
		t.Fatalf("construct discovery handler: %v", err)
	}
	discoveryApplication, err := newDiscoveryPreflightHandler(builder, inner, func(string) (discovery.AuthenticatedCursor, error) {
		return discovery.AuthenticatedCursor{}, discovery.ErrInvalidActivityCursor
	})
	if err != nil {
		t.Fatalf("construct discovery preflight: %v", err)
	}
	application := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/static/" + appStylesheetFilename:
			staticAssetHandler("text/css; charset=utf-8", appStylesheet).ServeHTTP(response, request)
		case "/static/htmx-2.0.10.min.js":
			staticAssetHandler("text/javascript; charset=utf-8", htmxScript).ServeHTTP(response, request)
		case "/static/" + discoveryResponseFilename:
			staticAssetHandler("text/javascript; charset=utf-8", discoveryResponseScript).ServeHTTP(response, request)
		case "/probe-discovery-error":
			response.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprintf(response, `<!doctype html><html lang="en"><body><main id="main-content" hx-get="%s/search?area=private" hx-trigger="load" hx-target="this" hx-swap="outerHTML">pending</main><script src="%s/static/htmx-2.0.10.min.js" defer></script><script src="%s/static/%s" defer></script></body></html>`, publicBase, publicBase, publicBase, discoveryResponseFilename)
		default:
			discoveryApplication.ServeHTTP(response, request)
		}
	})
	upstream := httptest.NewServer(application)
	defer upstream.Close()

	directory := t.TempDir()
	configuration := fmt.Sprintf("{\n admin off\n auto_https off\n}\nhttp://127.0.0.1:%d {\n %s\n}\n", port, browserCaddyProxy(basePath, upstream.URL))
	configurationPath := filepath.Join(directory, "Caddyfile")
	if err := os.WriteFile(configurationPath, []byte(configuration), 0o600); err != nil {
		t.Fatalf("write temporary Caddyfile: %v", err)
	}
	caddyContext, cancelCaddy := context.WithCancel(context.Background())
	var caddyLog bytes.Buffer
	command := exec.CommandContext(caddyContext, caddy, "run", "--config", configurationPath, "--adapter", "caddyfile")
	command.Stdout, command.Stderr = &caddyLog, &caddyLog
	if err := command.Start(); err != nil {
		cancelCaddy()
		t.Fatalf("start Caddy: %v", err)
	}
	defer func() {
		cancelCaddy()
		_ = command.Wait()
	}()
	target := publicBase + "/search?author=2"
	waitForDiscoveryCaddy(t, target, &caddyLog)

	profile := filepath.Join(directory, "chromium-profile")
	browser := exec.Command(chromium,
		"--headless=new", "--disable-background-networking", "--disable-gpu", "--no-first-run", "--no-proxy-server",
		"--user-data-dir="+profile, "--virtual-time-budget=2000", "--dump-dom", target,
	)
	var browserOutput, browserError bytes.Buffer
	browser.Stdout, browser.Stderr = &browserOutput, &browserError
	if err := browser.Run(); err != nil {
		t.Fatalf("Chromium failed: %v; stderr: %s", err, browserError.String())
	}
	document := browserOutput.String()
	for _, required := range []string{
		`<html lang="en"`, `aria-label="Primary"`, `href="` + basePath + `/search"`, `href="` + basePath + `/activity"`,
		`src="` + basePath + `/static/` + discoveryResponseFilename + `"`,
		`<main id="main-content" tabindex="-1"`, `<label class="grid gap-1 font-semibold">Search text`,
		`<label class="grid gap-1 font-semibold">Author ID`, `<h1 id="search-title"`,
		`A safe browser-visible excerpt.`, `href="` + basePath + `/posts/9"`,
	} {
		if !strings.Contains(document, required) {
			t.Fatalf("browser DOM lacks %q", required)
		}
	}
	if basePath != "" && (strings.Contains(document, `href="/search`) || strings.Contains(document, `href="/activity`)) || strings.Contains(document, `<script>alert`) {
		t.Fatalf("browser DOM escaped base path or exposed unsafe markup")
	}
	reflow := exec.Command(node, "--test", filepath.Join("..", "..", "assets", "scripts", "discovery-reflow.chromium.test.mjs"))
	reflow.Env = append(os.Environ(), "CHROMIUM="+chromium, "GOTTH_BB_DISCOVERY_REFLOW_URL="+target)
	if output, err := reflow.CombinedOutput(); err != nil {
		t.Fatalf("Chromium discovery reflow failed: %v; output: %s", err, output)
	}
	errorProbe := exec.Command(chromium,
		"--headless=new", "--disable-background-networking", "--disable-gpu", "--no-first-run", "--no-proxy-server",
		"--user-data-dir="+filepath.Join(directory, "chromium-error-profile"), "--virtual-time-budget=2000", "--dump-dom", publicBase+"/probe-discovery-error",
	)
	var errorDOM, errorLog bytes.Buffer
	errorProbe.Stdout, errorProbe.Stderr = &errorDOM, &errorLog
	if err := errorProbe.Run(); err != nil {
		t.Fatalf("Chromium discovery-error probe failed: %v; stderr: %s", err, errorLog.String())
	}
	if strings.Contains(errorDOM.String(), ">pending<") || !strings.Contains(errorDOM.String(), "Invalid search") || !strings.Contains(errorDOM.String(), `id="main-content"`) {
		t.Fatalf("marked discovery 400 did not replace the HTMX target: %s", errorDOM.String())
	}
	t.Logf("browser-through-Caddy admitted: base_path=%q caddy=%s chromium=%s bytes=%d", basePath, commandPathVersion(t, caddy, "version"), commandPathVersion(t, chromium, "--version"), len(document))
}

func TestUnreadControlsKeyboardAndNoScriptThroughCaddy(t *testing.T) {
	for _, test := range []struct {
		name     string
		basePath string
	}{
		{name: "empty base path"},
		{name: "nonempty base path", basePath: "/bb"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runUnreadControlsKeyboardAndNoScriptThroughCaddy(t, test.basePath)
		})
	}
}

func runUnreadControlsKeyboardAndNoScriptThroughCaddy(t *testing.T, basePath string) {
	if os.Getenv("GOTTH_BB_BROWSER_CADDY") != "1" {
		t.Skip("set GOTTH_BB_BROWSER_CADDY=1 on the designated evidence host")
	}
	caddy, err := exec.LookPath("caddy")
	if err != nil {
		t.Fatalf("locate Caddy: %v", err)
	}
	chromium, err := exec.LookPath("chromium")
	if err != nil {
		t.Fatalf("locate Chromium: %v", err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("locate Node: %v", err)
	}
	port := reserveDiscoveryTestPort(t)
	publicBase := fmt.Sprintf("http://127.0.0.1:%d%s", port, basePath)
	builder := mustAbsoluteURLBuilder(t, publicBase, basePath)
	var marked atomic.Bool

	topicHandler, err := newTopicPostListHandler(builder, store.MaximumPostPage, func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
		page := topicPostTestPage(1)
		state := store.ReadStateNew
		if marked.Load() {
			state = store.ReadStateRead
		}
		page.ReadState = &state
		return page, nil
	})
	if err != nil {
		t.Fatalf("construct topic handler: %v", err)
	}
	unreadInner, err := newUnreadHandler(builder, UnreadHTTPServices{
		FirstUnread: func(context.Context, auth.AccessContext, int64) (forum.FirstUnreadTarget, error) {
			if marked.Load() {
				return forum.FirstUnreadTarget{}, nil
			}
			return forum.FirstUnreadTarget{PostID: 101, Page: 1}, nil
		},
		MarkRead: func(context.Context, auth.AccessContext, int64) error {
			marked.Store(true)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("construct unread handler: %v", err)
	}
	unreadHandler, err := newUnreadPreflightHandler(builder, unreadInner)
	if err != nil {
		t.Fatalf("construct unread preflight: %v", err)
	}
	authentication := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 11, Role: auth.RoleMember}}
	token := validCSRFTokenForTest(0x51)
	withPrivateAuthority := func(request *http.Request) *http.Request {
		ctx := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, authentication)
		ctx = context.WithValue(ctx, csrfTokenContextKey{}, token)
		ctx = context.WithValue(ctx, unreadControlsContextKey{}, true)
		return request.WithContext(ctx)
	}
	application := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/static/" + appStylesheetFilename:
			staticAssetHandler("text/css; charset=utf-8", appStylesheet).ServeHTTP(response, request)
		case "/static/htmx-2.0.10.min.js":
			staticAssetHandler("text/javascript; charset=utf-8", htmxScript).ServeHTTP(response, request)
		case "/static/" + discoveryResponseFilename:
			staticAssetHandler("text/javascript; charset=utf-8", discoveryResponseScript).ServeHTTP(response, request)
		case "/static/" + markdownToolbarFilename:
			staticAssetHandler("text/javascript; charset=utf-8", markdownToolbarScript).ServeHTTP(response, request)
		case "/topics/42":
			routeContext := chi.NewRouteContext()
			routeContext.URLParams.Add("topicID", "42")
			ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
			topicHandler.ServeHTTP(response, withPrivateAuthority(request.WithContext(ctx)))
		case "/topics/42/unread", "/topics/42/read":
			unreadHandler.ServeHTTP(response, withPrivateAuthority(request))
		default:
			http.NotFound(response, request)
		}
	})
	upstream := httptest.NewServer(application)
	defer upstream.Close()

	directory := t.TempDir()
	configuration := fmt.Sprintf("{\n admin off\n auto_https off\n}\nhttp://127.0.0.1:%d {\n %s\n}\n", port, browserCaddyProxy(basePath, upstream.URL))
	configurationPath := filepath.Join(directory, "Caddyfile")
	if err := os.WriteFile(configurationPath, []byte(configuration), 0o600); err != nil {
		t.Fatalf("write temporary Caddyfile: %v", err)
	}
	caddyContext, cancelCaddy := context.WithCancel(context.Background())
	var caddyLog bytes.Buffer
	command := exec.CommandContext(caddyContext, caddy, "run", "--config", configurationPath, "--adapter", "caddyfile")
	command.Stdout, command.Stderr = &caddyLog, &caddyLog
	if err := command.Start(); err != nil {
		cancelCaddy()
		t.Fatalf("start Caddy: %v", err)
	}
	defer func() {
		cancelCaddy()
		_ = command.Wait()
	}()
	target := publicBase + "/topics/42"
	waitForDiscoveryCaddy(t, target, &caddyLog)

	browser := exec.Command(node, "--test", filepath.Join("..", "..", "assets", "scripts", "unread-controls.chromium.test.mjs"))
	browser.Env = append(os.Environ(), "CHROMIUM="+chromium, "GOTTH_BB_UNREAD_BROWSER_URL="+target)
	output, err := browser.CombinedOutput()
	if err != nil {
		t.Fatalf("unread Chromium evidence failed: %v\n%s", err, output)
	}
	if !marked.Load() {
		t.Fatal("keyboard mark-read form did not invoke the server mutation")
	}
	t.Logf("unread browser-through-Caddy admitted: base_path=%q caddy=%s chromium=%s node=%s\n%s", basePath, commandPathVersion(t, caddy, "version"), commandPathVersion(t, chromium, "--version"), commandPathVersion(t, node, "--version"), output)
}

func browserCaddyProxy(basePath, upstream string) string {
	proxy := "reverse_proxy " + upstream
	if basePath != "" {
		return fmt.Sprintf("handle_path %s/* {\n  %s\n }", basePath, proxy)
	}
	return proxy
}

func reserveDiscoveryTestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve Caddy port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release Caddy port reservation: %v", err)
	}
	return port
}

func mustAbsoluteURLBuilder(t *testing.T, raw, basePath string) URLBuilder {
	t.Helper()
	public, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse public URL: %v", err)
	}
	builder, err := NewURLBuilder(*public, basePath)
	if err != nil {
		t.Fatalf("NewURLBuilder() returned error: %v", err)
	}
	return builder
}

func waitForDiscoveryCaddy(t *testing.T, target string, log *bytes.Buffer) {
	t.Helper()
	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(target)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && response.Header.Get("Cache-Control") == "private, no-store" {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Caddy did not serve discovery route: %s", log.String())
}

func commandPathVersion(t *testing.T, path string, argument string) string {
	t.Helper()
	output, err := exec.Command(path, argument).CombinedOutput()
	if err != nil {
		t.Fatalf("read %s version: %v", path, err)
	}
	return strings.TrimSpace(string(output))
}
