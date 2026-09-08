//go:build integration

package httpui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/abuse"
	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/forum"
	"github.com/gotthboard/gotth-bb/internal/site"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestAbuseRejectionNoScriptThroughCaddy(t *testing.T) {
	for _, test := range []struct {
		name     string
		basePath string
	}{
		{name: "empty base path"},
		{name: "nonempty base path", basePath: "/bb"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			runAbuseRejectionNoScriptThroughCaddy(t, test.basePath)
		})
	}
}

func runAbuseRejectionNoScriptThroughCaddy(t *testing.T, basePath string) {
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
	destinationPolicy := blockedHTTPDestinationPolicy(t)
	observer := &browserAbuseObserver{}
	var identityMutex sync.Mutex
	identityRequests := 0
	identityFailures := []string{}

	createTopic := func(_ context.Context, _ auth.AccessContext, area, title, markdown string) (forum.PublishResult, error) {
		if title == "Rate" {
			return forum.PublishResult{}, forum.PublicationRateLimitError{RetryAfterSeconds: 17}
		}
		_, renderErr := forum.RenderTopicDraft(destinationPolicy, area, title, markdown)
		return forum.PublishResult{}, renderErr
	}
	createReply := func(context.Context, auth.AccessContext, int64, int64, string) (forum.PublishResult, error) {
		panic("reply is not expected")
	}
	publishing, err := newPublishingHandler(builder, destinationPolicy, observer, createTopic, createReply)
	if err != nil {
		t.Fatalf("construct publishing handler: %v", err)
	}
	editing, err := newEditingHandler(
		builder, destinationPolicy, observer,
		func(context.Context, auth.AccessContext, int64) (store.EditablePost, error) {
			return validEditablePost(), nil
		},
		func(_ context.Context, _ auth.AccessContext, postID int64, revision int32, markdown string) (forum.EditResult, error) {
			_, renderErr := forum.RenderReplyDraft(destinationPolicy, markdown)
			return forum.EditResult{PostID: postID, Revision: revision}, renderErr
		},
		func(context.Context, auth.AccessContext, int64, int32) (forum.DeleteResult, error) {
			panic("delete is not expected")
		},
	)
	if err != nil {
		t.Fatalf("construct editing handler: %v", err)
	}
	siteServices := validSiteHTTPServices()
	siteServices.Editable = func(context.Context, auth.AccessContext) (site.EditableSettings, error) {
		return site.EditableSettings{Shell: site.ShellPresentation{Name: "Browser Board", Description: "Browser description", Theme: "blue"}, Revision: 9}, nil
	}
	siteServices.Update = func(_ context.Context, _ auth.AccessContext, input site.SettingsInput, _ pgtype.UUID) (site.MutationResult, error) {
		if _, renderErr := forum.RenderReplyDraft(destinationPolicy, input.RulesMarkdown); errors.Is(renderErr, abuse.ErrBlockedDestination) {
			return site.MutationResult{}, errors.Join(site.ErrInput, abuse.ErrBlockedDestination)
		}
		return site.MutationResult{}, fmt.Errorf("unexpected allowed settings mutation")
	}
	_, settings, err := newSiteSettingsHandler(builder, observer, siteServices)
	if err != nil {
		t.Fatalf("construct settings handler: %v", err)
	}
	token := validCSRFTokenForTest(0x51)
	application := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		identityMutex.Lock()
		identityRequests++
		if forwarded := request.Header.Values("X-Forwarded-For"); len(forwarded) != 1 || forwarded[0] != "127.0.0.1" || len(request.Header.Values("Forwarded")) != 0 || len(request.Header.Values("X-Real-IP")) != 0 {
			identityFailures = append(identityFailures, fmt.Sprintf("forwarded=%q Forwarded=%q X-Real-IP=%q", forwarded, request.Header.Values("Forwarded"), request.Header.Values("X-Real-IP")))
		}
		identityMutex.Unlock()
		ctx := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, auth.SessionAuthentication{
			SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 1, Role: auth.RoleAdministrator},
		})
		ctx = context.WithValue(ctx, csrfTokenContextKey{}, token)
		request = request.WithContext(ctx)
		switch {
		case strings.HasPrefix(request.URL.Path, "/topics"):
			publishing.ServeHTTP(response, request)
		case strings.HasPrefix(request.URL.Path, "/posts/"):
			editing.ServeHTTP(response, request)
		case request.URL.Path == "/admin/settings":
			settings.ServeHTTP(response, request)
		default:
			http.NotFound(response, request)
		}
	})
	applicationWithID := withModerationTestRequestID(t, application)
	limiter, err := abuse.NewRequestLimiter(bytes.NewReader(bytes.Repeat([]byte{0x61}, 32)), 4096, 100000, time.Minute, time.Now)
	if err != nil {
		t.Fatalf("construct browser request limiter: %v", err)
	}
	admittedApplication, err := NewRequestAdmissionHandler(applicationWithID, limiter, observer, true)
	if err != nil {
		t.Fatalf("construct browser request admission: %v", err)
	}
	securedApplication, err := NewBrowserSecurityHandler(admittedApplication)
	if err != nil {
		t.Fatalf("construct browser security handler: %v", err)
	}
	upstream := httptest.NewServer(securedApplication)
	defer upstream.Close()

	directory := t.TempDir()
	proxy := fmt.Sprintf("reverse_proxy %s {\n   header_up X-Forwarded-For {remote_host}\n   header_up -Forwarded\n   header_up -X-Real-IP\n  }", upstream.URL)
	if basePath != "" {
		proxy = fmt.Sprintf("handle_path %s/* {\n  %s\n }", basePath, proxy)
	}
	configuration := fmt.Sprintf("{\n admin off\n auto_https off\n}\nhttp://127.0.0.1:%d {\n %s\n}\n", port, proxy)
	configurationPath := filepath.Join(directory, "Caddyfile")
	if err := os.WriteFile(configurationPath, []byte(configuration), 0o600); err != nil {
		t.Fatalf("write Caddyfile: %v", err)
	}
	assertAbuseCaddyAdaptedIdentity(t, caddy, configurationPath)
	caddyContext, cancelCaddy := context.WithCancel(context.Background())
	var caddyLog bytes.Buffer
	command := exec.CommandContext(caddyContext, caddy, "run", "--config", configurationPath, "--adapter", "caddyfile")
	command.Stdout, command.Stderr = &caddyLog, &caddyLog
	if err := command.Start(); err != nil {
		cancelCaddy()
		t.Fatalf("start Caddy: %v", err)
	}
	defer func() { cancelCaddy(); _ = command.Wait() }()
	waitForAbuseCaddy(t, publicBase+"/topics/new?area=news", &caddyLog)

	browser := exec.Command(node, "--test", filepath.Join("..", "..", "assets", "scripts", "abuse-rejection.chromium.test.mjs"))
	browser.Env = append(os.Environ(), "CHROMIUM="+chromium, "GOTTH_BB_ABUSE_BROWSER_URL="+publicBase)
	output, err := browser.CombinedOutput()
	if err != nil {
		t.Fatalf("abuse Chromium evidence failed: %v\n%s\nCaddy:\n%s", err, output, caddyLog.String())
	}
	events := observer.snapshot()
	if len(events) != 4 {
		t.Fatalf("observer events = %+v", events)
	}
	wantClasses := []abuse.RejectionClass{abuse.RejectionBlocked, abuse.RejectionPublicationRate, abuse.RejectionBlocked, abuse.RejectionBlocked}
	for index, event := range events {
		if event.Class != wantClasses[index] || event.RequestID != moderationTestRequestID {
			t.Fatalf("observer event %d = %+v", index, event)
		}
	}
	identityMutex.Lock()
	requests, failures := identityRequests, append([]string(nil), identityFailures...)
	identityMutex.Unlock()
	if requests == 0 || len(failures) != 0 {
		t.Fatalf("Caddy identity overwrite = requests %d failures %q", requests, failures)
	}
	t.Logf("abuse browser-through-Caddy admitted: base_path=%q identity_requests=%d caddy=%s chromium=%s node=%s\n%s", basePath, requests, commandPathVersion(t, caddy, "version"), commandPathVersion(t, chromium, "--version"), commandPathVersion(t, node, "--version"), output)
}

func assertAbuseCaddyAdaptedIdentity(t *testing.T, caddy, configurationPath string) {
	t.Helper()
	command := exec.Command(caddy, "adapt", "--config", configurationPath, "--adapter", "caddyfile")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("adapt Caddy configuration: %v\n%s", err, stderr.String())
	}
	var adapted any
	if err := json.Unmarshal(stdout.Bytes(), &adapted); err != nil {
		t.Fatalf("decode adapted Caddy configuration: %v\n%s", err, stdout.String())
	}
	proxies := make([]map[string]any, 0, 1)
	var walk func(any)
	walk = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if value["handler"] == "reverse_proxy" {
				proxies = append(proxies, value)
			}
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(adapted)
	if len(proxies) != 1 {
		t.Fatalf("adapted reverse-proxy handlers = %d, want 1", len(proxies))
	}
	headers, ok := proxies[0]["headers"].(map[string]any)
	if !ok {
		t.Fatal("adapted reverse proxy has no header operations")
	}
	requestHeaders, ok := headers["request"].(map[string]any)
	if !ok {
		t.Fatal("adapted reverse proxy has no request-header operations")
	}
	set, ok := requestHeaders["set"].(map[string]any)
	if !ok || len(set) != 1 || !equalAbuseCaddyStrings(set["X-Forwarded-For"], "{http.request.remote.host}") {
		t.Fatalf("adapted request-header set = %#v", requestHeaders["set"])
	}
	deleted, ok := requestHeaders["delete"].([]any)
	if !ok || len(deleted) != 2 || !equalAbuseCaddyStrings(deleted, "Forwarded", "X-Real-IP") {
		t.Fatalf("adapted request-header delete = %#v", requestHeaders["delete"])
	}
}

func equalAbuseCaddyStrings(value any, want ...string) bool {
	values, ok := value.([]any)
	if !ok || len(values) != len(want) {
		return false
	}
	for index, expected := range want {
		if values[index] != expected {
			return false
		}
	}
	return true
}

type browserAbuseObserver struct {
	mutex  sync.Mutex
	events []abuse.Event
}

func (observer *browserAbuseObserver) Observe(_ context.Context, event abuse.Event) {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()
	observer.events = append(observer.events, event)
}

func (observer *browserAbuseObserver) snapshot() []abuse.Event {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()
	return append([]abuse.Event(nil), observer.events...)
}

func waitForAbuseCaddy(t *testing.T, target string, log *bytes.Buffer) {
	t.Helper()
	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(target)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && strings.Contains(response.Header.Get("Cache-Control"), "no-store") && hasBetaBrowserSecurityHeaders(response.Header) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Caddy did not serve abuse route: %s", log.String())
}

func hasBetaBrowserSecurityHeaders(header http.Header) bool {
	want := map[string]string{
		"Content-Security-Policy":      browserContentSecurityPolicy,
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Origin-Agent-Cluster":         "?1",
		"Permissions-Policy":           "camera=(), geolocation=(), microphone=(), payment=(), usb=()",
		"Referrer-Policy":              "no-referrer",
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
	}
	for name, value := range want {
		if header.Get(name) != value {
			return false
		}
	}
	return true
}
