//go:build integration

package httpui

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/gotthboard/gotth-bb/internal/administration"
	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestAdministrationKeyboardAndNoScriptThroughCaddy(t *testing.T) {
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
	services := administrationCompletionTestServices()
	var changed atomic.Bool
	services.LoadAccount = func(context.Context, auth.AccessContext, int64) (administration.AccountSummary, error) {
		displayName := "Local Member"
		if changed.Load() {
			displayName = "Updated Member"
		}
		return administration.AccountSummary{ID: 2, DisplayName: displayName, Role: policy.RoleMember, Revision: 3}, nil
	}
	services.ChangeRole = func(_ context.Context, actor auth.AccessContext, userID int64, role, expected policy.Role, reason string, revision int64, requestID pgtype.UUID) (administration.AccountMutationResult, error) {
		if actor.UserID != 1 || userID != 2 || role != policy.RoleMember || expected != policy.RoleMember || reason != "Keyboard administration evidence" || revision != 3 || !requestID.Valid {
			return administration.AccountMutationResult{}, fmt.Errorf("unexpected keyboard role form: actor=%+v user=%d role=%d expected=%d reason=%q revision=%d request=%+v", actor, userID, role, expected, reason, revision, requestID)
		}
		changed.Store(true)
		return administration.AccountMutationResult{UserID: 2, Revision: 4, AuditID: 9}, nil
	}
	inner, err := newAdministrationCompletionHandler(builder, services)
	if err != nil {
		t.Fatalf("construct administration handler: %v", err)
	}
	inner = withModerationTestRequestID(t, inner)
	authentication := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 1, Role: auth.RoleAdministrator}}
	token := validCSRFTokenForTest(0x51)
	application := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/static/" + appStylesheetFilename:
			staticAssetHandler("text/css; charset=utf-8", appStylesheet).ServeHTTP(response, request)
		case "/static/htmx-2.0.10.min.js":
			staticAssetHandler("text/javascript; charset=utf-8", htmxScript).ServeHTTP(response, request)
		default:
			ctx := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, authentication)
			ctx = context.WithValue(ctx, csrfTokenContextKey{}, token)
			inner.ServeHTTP(response, request.WithContext(ctx))
		}
	})
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
	target := publicBase + "/admin"
	waitForDiscoveryCaddy(t, target, &caddyLog)

	browser := exec.Command(node, "--test", filepath.Join("..", "..", "assets", "scripts", "administration.chromium.test.mjs"))
	browser.Env = append(os.Environ(), "CHROMIUM="+chromium, "GOTTH_BB_ADMINISTRATION_BROWSER_URL="+target)
	output, err := browser.CombinedOutput()
	if err != nil {
		t.Fatalf("administration Chromium evidence failed: %v\n%s", err, output)
	}
	if !changed.Load() {
		t.Fatal("keyboard role form did not invoke the server mutation")
	}
	t.Logf("administration browser-through-Caddy admitted: caddy=%s chromium=%s node=%s\n%s", commandPathVersion(t, caddy, "version"), commandPathVersion(t, chromium, "--version"), commandPathVersion(t, node, "--version"), output)
}
