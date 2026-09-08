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
	"time"

	"github.com/gotthboard/gotth-bb/internal/administration"
	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/policy"
	contentrender "github.com/gotthboard/gotth-bb/internal/render"
	"github.com/gotthboard/gotth-bb/internal/site"
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
	var emptyDashboard, membership, areaAssigned, sessionRevoked atomic.Bool
	membership.Store(true)
	areaAssigned.Store(true)
	var groupName, areaMode, siteName, siteDescription, siteTheme, rulesMarkdown atomic.Value
	groupName.Store("Members")
	areaMode.Store(policy.PostingNormal)
	siteName.Store("Browser Board")
	siteDescription.Store("Browser description")
	siteTheme.Store("blue")
	rulesMarkdown.Store("# Browser rules")
	var groupRevision, areaRevision, siteRevision atomic.Int64
	groupRevision.Store(5)
	areaRevision.Store(2)
	siteRevision.Store(1)
	services.Dashboard = func(context.Context, auth.AccessContext) (administration.Dashboard, error) {
		page := administration.Dashboard{ObservedAt: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)}
		if !emptyDashboard.Load() {
			page.Users, page.Members, page.Moderators, page.Administrators = 52, 50, 1, 1
			page.ActiveUsers, page.SuspendedUsers, page.Topics, page.Posts = 51, 1, 4, 8
			page.OpenReports, page.InReviewReports = 1, 2
		}
		return page, nil
	}
	services.ListAccounts = func(_ context.Context, _ auth.AccessContext, after int64) (administration.AccountPage, error) {
		if after > 0 {
			return administration.AccountPage{Accounts: []administration.AccountSummary{{ID: 52, DisplayName: "Continuation Account", Role: policy.RoleMember, Revision: 1}}}, nil
		}
		accounts := make([]administration.AccountSummary, 50)
		for index := range accounts {
			id := int64(index + 2)
			name := fmt.Sprintf("Browser Account %02d", id)
			if id == 2 {
				name = "Local Member"
			}
			accounts[index] = administration.AccountSummary{ID: id, DisplayName: name, Role: policy.RoleMember, Revision: 3}
		}
		return administration.AccountPage{Accounts: accounts, NextAfter: 51}, nil
	}
	services.LoadAccount = func(_ context.Context, _ auth.AccessContext, userID int64) (administration.AccountSummary, error) {
		if userID == 1 {
			return administration.AccountSummary{ID: 1, DisplayName: "Browser Administrator", Role: policy.RoleAdministrator, Revision: 2}, nil
		}
		displayName := "Local Member"
		if changed.Load() {
			displayName = "Updated Member"
		}
		return administration.AccountSummary{ID: 2, DisplayName: displayName, Role: policy.RoleMember, Revision: 3}, nil
	}
	services.ListAccountGroups = func(context.Context, auth.AccessContext, int64, int64) (administration.AccountGroupPage, error) {
		return administration.AccountGroupPage{Groups: []administration.AccountGroup{{ID: 4, Name: groupName.Load().(string), Member: membership.Load()}}}, nil
	}
	services.ChangeRole = func(_ context.Context, actor auth.AccessContext, userID int64, role, expected policy.Role, reason string, revision int64, requestID pgtype.UUID) (administration.AccountMutationResult, error) {
		if actor.UserID != 1 || !requestID.Valid {
			return administration.AccountMutationResult{}, fmt.Errorf("unexpected keyboard role form: actor=%+v user=%d role=%d expected=%d reason=%q revision=%d request=%+v", actor, userID, role, expected, reason, revision, requestID)
		}
		if userID == 1 && role == policy.RoleMember && expected == policy.RoleAdministrator && reason == "Revoke browser session" && revision == 2 {
			sessionRevoked.Store(true)
			return administration.AccountMutationResult{UserID: 1, Revision: 3, AuditID: 10}, nil
		}
		if userID != 2 || role != policy.RoleMember || expected != policy.RoleMember || reason != "Keyboard administration evidence" || revision != 3 {
			return administration.AccountMutationResult{}, fmt.Errorf("unexpected keyboard role target: user=%d role=%d expected=%d reason=%q revision=%d", userID, role, expected, reason, revision)
		}
		changed.Store(true)
		return administration.AccountMutationResult{UserID: 2, Revision: 4, AuditID: 9}, nil
	}
	services.ChangeMembership = func(_ context.Context, _ auth.AccessContext, userID, groupID int64, grant bool, _ string, revision int64, requestID pgtype.UUID) (administration.AccountMutationResult, error) {
		if userID != 2 || groupID != 4 || revision != 3 || !requestID.Valid {
			return administration.AccountMutationResult{}, fmt.Errorf("unexpected membership mutation")
		}
		membership.Store(grant)
		return administration.AccountMutationResult{UserID: userID, Revision: revision + 1, AuditID: 11}, nil
	}
	services.ListGroups = func(context.Context, auth.AccessContext, int64) (administration.GroupPage, error) {
		return administration.GroupPage{Groups: []administration.GroupSummary{{ID: 4, Name: groupName.Load().(string), Revision: groupRevision.Load()}}}, nil
	}
	services.CreateGroup = func(_ context.Context, _ auth.AccessContext, name, reason string, requestID pgtype.UUID) (administration.GroupMutationResult, error) {
		if name != "Browser Operators" || reason != "Create browser group" || !requestID.Valid {
			return administration.GroupMutationResult{}, fmt.Errorf("unexpected group create")
		}
		groupName.Store(name)
		groupRevision.Store(1)
		return administration.GroupMutationResult{GroupID: 4, Revision: 1, AuditID: 12}, nil
	}
	services.RenameGroup = func(_ context.Context, _ auth.AccessContext, groupID int64, name, reason string, revision int64, requestID pgtype.UUID) (administration.GroupMutationResult, error) {
		if groupID != 4 || name != "Renamed Browser Operators" || reason != "Rename browser group" || revision != groupRevision.Load() || !requestID.Valid {
			return administration.GroupMutationResult{}, fmt.Errorf("unexpected group rename")
		}
		groupName.Store(name)
		groupRevision.Add(1)
		return administration.GroupMutationResult{GroupID: 4, Revision: groupRevision.Load(), AuditID: 13}, nil
	}
	services.ListAreas = func(context.Context, auth.AccessContext, int32, int64) (administration.AreaPage, error) {
		return administration.AreaPage{Areas: []administration.AreaSummary{{ID: 3, Slug: "general", Name: "General", Visibility: policy.VisibilityGroups, PostingMode: areaMode.Load().(policy.PostingMode), Revision: areaRevision.Load(), GroupCount: 1}}}, nil
	}
	services.LoadArea = func(context.Context, auth.AccessContext, int64, int64) (administration.AreaDetail, error) {
		return administration.AreaDetail{Area: administration.AreaSummary{ID: 3, Slug: "general", Name: "General", Visibility: policy.VisibilityGroups, PostingMode: areaMode.Load().(policy.PostingMode), Revision: areaRevision.Load()}, Groups: []administration.AreaGroup{{ID: 4, Name: groupName.Load().(string), Assigned: areaAssigned.Load()}}}, nil
	}
	services.UpdateArea = func(_ context.Context, _ auth.AccessContext, areaID int64, input administration.AreaCoreInput, requestID pgtype.UUID) (administration.AreaCompletionResult, error) {
		if areaID != 3 || input.Revision != areaRevision.Load() || !requestID.Valid {
			return administration.AreaCompletionResult{}, fmt.Errorf("unexpected area update")
		}
		areaMode.Store(input.PostingMode)
		areaRevision.Add(1)
		return administration.AreaCompletionResult{AreaID: areaID, Revision: areaRevision.Load(), AuditID: 14}, nil
	}
	services.ChangeAreaGroup = func(_ context.Context, _ auth.AccessContext, areaID, groupID int64, grant bool, _ string, revision int64, requestID pgtype.UUID) (administration.AreaCompletionResult, error) {
		if areaID != 3 || groupID != 4 || revision != areaRevision.Load() || !requestID.Valid {
			return administration.AreaCompletionResult{}, fmt.Errorf("unexpected area group mutation")
		}
		areaAssigned.Store(grant)
		areaRevision.Add(1)
		return administration.AreaCompletionResult{AreaID: areaID, Revision: areaRevision.Load(), AuditID: 15}, nil
	}
	inner, err := newAdministrationCompletionHandler(builder, services)
	if err != nil {
		t.Fatalf("construct administration handler: %v", err)
	}
	inner = withModerationTestRequestID(t, inner)
	siteServices := SiteHTTPServices{
		Shell: func(context.Context) (site.ShellPresentation, error) {
			return site.ShellPresentation{Name: siteName.Load().(string), Description: siteDescription.Load().(string), Theme: siteTheme.Load().(string)}, nil
		},
		Rules: func(context.Context) (site.PublicRules, error) {
			rendered, renderErr := contentrender.RenderMarkdown(rulesMarkdown.Load().(string))
			if renderErr != nil {
				return site.PublicRules{}, renderErr
			}
			return site.PublicRules{Shell: site.ShellPresentation{Name: siteName.Load().(string), Description: siteDescription.Load().(string), Theme: siteTheme.Load().(string)}, HTML: rendered.TrustedHTML()}, nil
		},
		Editable: func(context.Context, auth.AccessContext) (site.EditableSettings, error) {
			return site.EditableSettings{Shell: site.ShellPresentation{Name: siteName.Load().(string), Description: siteDescription.Load().(string), Theme: siteTheme.Load().(string)}, RulesMarkdown: rulesMarkdown.Load().(string), RendererVersion: contentrender.RendererVersion, Revision: siteRevision.Load()}, nil
		},
		Update: func(_ context.Context, _ auth.AccessContext, input site.SettingsInput, requestID pgtype.UUID) (site.MutationResult, error) {
			if input.Revision != siteRevision.Load() || !requestID.Valid {
				return site.MutationResult{}, fmt.Errorf("unexpected site settings mutation")
			}
			siteName.Store(input.Name)
			siteDescription.Store(input.Description)
			siteTheme.Store(input.Theme)
			rulesMarkdown.Store(input.RulesMarkdown)
			siteRevision.Add(1)
			return site.MutationResult{Revision: siteRevision.Load(), AuditID: 16}, nil
		},
	}
	publicSite, privateSite, err := newSiteSettingsHandler(builder, siteServices)
	if err != nil {
		t.Fatalf("construct site settings handler: %v", err)
	}
	privateSite = withModerationTestRequestID(t, privateSite)
	adminDestination, err := builder.Path("admin")
	if err != nil {
		t.Fatalf("build administration destination: %v", err)
	}
	token := validCSRFTokenForTest(0x51)
	application := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/static/" + appStylesheetFilename:
			staticAssetHandler("text/css; charset=utf-8", appStylesheet).ServeHTTP(response, request)
		case "/static/htmx-2.0.10.min.js":
			staticAssetHandler("text/javascript; charset=utf-8", htmxScript).ServeHTTP(response, request)
		case "/__test/empty":
			emptyDashboard.Store(true)
			http.Redirect(response, request, adminDestination, http.StatusSeeOther)
		case "/__test/populated":
			emptyDashboard.Store(false)
			http.Redirect(response, request, adminDestination, http.StatusSeeOther)
		default:
			authentication := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 1, Role: auth.RoleAdministrator}}
			if sessionRevoked.Load() {
				authentication = auth.SessionAuthentication{}
			}
			ctx := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, authentication)
			ctx = context.WithValue(ctx, csrfTokenContextKey{}, token)
			request = request.WithContext(ctx)
			switch request.URL.Path {
			case "/rules":
				publicSite.ServeHTTP(response, request)
			case "/admin/settings":
				privateSite.ServeHTTP(response, request)
			default:
				inner.ServeHTTP(response, request)
			}
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
	if groupName.Load().(string) != "Renamed Browser Operators" || areaMode.Load().(policy.PostingMode) != policy.PostingNormal || !areaAssigned.Load() ||
		siteName.Load().(string) != "Updated Browser Board" || rulesMarkdown.Load().(string) != "# Updated browser rules" || !sessionRevoked.Load() {
		t.Fatalf("browser matrix did not complete: group=%q area_mode=%q assigned=%t site=%q rules=%q session_revoked=%t",
			groupName.Load(), areaMode.Load(), areaAssigned.Load(), siteName.Load(), rulesMarkdown.Load(), sessionRevoked.Load())
	}
	t.Logf("administration browser-through-Caddy admitted: caddy=%s chromium=%s node=%s\n%s", commandPathVersion(t, caddy, "version"), commandPathVersion(t, chromium, "--version"), commandPathVersion(t, node, "--version"), output)
}
