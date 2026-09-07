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
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/discovery"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestDiscoveryBrowserThroughCaddy(t *testing.T) {
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
	port := reserveDiscoveryTestPort(t)
	publicBase := fmt.Sprintf("http://127.0.0.1:%d/bb", port)
	builder := mustAbsoluteURLBuilder(t, publicBase, "/bb")
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
	application, err := newDiscoveryPreflightHandler(builder, inner, func(string) (discovery.AuthenticatedCursor, error) {
		return discovery.AuthenticatedCursor{}, discovery.ErrInvalidActivityCursor
	})
	if err != nil {
		t.Fatalf("construct discovery preflight: %v", err)
	}
	upstream := httptest.NewServer(application)
	defer upstream.Close()

	directory := t.TempDir()
	configuration := fmt.Sprintf("{\n admin off\n auto_https off\n}\nhttp://127.0.0.1:%d {\n handle_path /bb/* {\n  reverse_proxy %s\n }\n}\n", port, upstream.URL)
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
		"--user-data-dir="+profile, "--dump-dom", target,
	)
	var browserOutput, browserError bytes.Buffer
	browser.Stdout, browser.Stderr = &browserOutput, &browserError
	if err := browser.Run(); err != nil {
		t.Fatalf("Chromium failed: %v; stderr: %s", err, browserError.String())
	}
	document := browserOutput.String()
	for _, required := range []string{
		`<html lang="en"`, `aria-label="Primary"`, `href="/bb/search"`, `href="/bb/activity"`,
		`<main id="main-content" tabindex="-1"`, `<label class="grid gap-1 font-semibold">Search text`,
		`<label class="grid gap-1 font-semibold">Author ID`, `<h1 id="search-title"`,
		`A safe browser-visible excerpt.`, `href="/bb/posts/9"`,
	} {
		if !strings.Contains(document, required) {
			t.Fatalf("browser DOM lacks %q", required)
		}
	}
	if strings.Contains(document, `href="/search`) || strings.Contains(document, `href="/activity`) || strings.Contains(document, `<script>alert`) {
		t.Fatalf("browser DOM escaped base path or exposed unsafe markup")
	}
	t.Logf("browser-through-Caddy admitted: caddy=%s chromium=%s bytes=%d", commandPathVersion(t, caddy, "version"), commandPathVersion(t, chromium, "--version"), len(document))
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
