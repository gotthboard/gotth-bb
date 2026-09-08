//go:build integration

package httpui

import (
	"bytes"
	"context"
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
	publicBase := fmt.Sprintf("http://127.0.0.1:%d/bb", port)
	builder := mustAbsoluteURLBuilder(t, publicBase, "/bb")
	destinationPolicy := blockedHTTPDestinationPolicy(t)
	observer := &browserAbuseObserver{}

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
	upstream := httptest.NewServer(applicationWithID)
	defer upstream.Close()

	directory := t.TempDir()
	configuration := fmt.Sprintf("{\n admin off\n auto_https off\n}\nhttp://127.0.0.1:%d {\n handle_path /bb/* {\n  reverse_proxy %s\n }\n}\n", port, upstream.URL)
	configurationPath := filepath.Join(directory, "Caddyfile")
	if err := os.WriteFile(configurationPath, []byte(configuration), 0o600); err != nil {
		t.Fatalf("write Caddyfile: %v", err)
	}
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
	t.Logf("abuse browser-through-Caddy admitted: caddy=%s chromium=%s node=%s\n%s", commandPathVersion(t, caddy, "version"), commandPathVersion(t, chromium, "--version"), commandPathVersion(t, node, "--version"), output)
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
			if response.StatusCode == http.StatusOK && strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Caddy did not serve abuse route: %s", log.String())
}
