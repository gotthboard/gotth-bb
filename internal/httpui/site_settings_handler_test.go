package httpui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/control"
	contentrender "github.com/gotthboard/gotth-bb/internal/render"
	"github.com/gotthboard/gotth-bb/internal/site"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestPublicRulesRouteIsExactSessionIndependentAndUsesOneProjection(t *testing.T) {
	t.Parallel()
	rendered, err := contentrender.RenderMarkdown("# Community rules\n\nBe decent.")
	if err != nil {
		t.Fatalf("RenderMarkdown() returned error: %v", err)
	}
	loads := 0
	services := validSiteHTTPServices()
	services.Rules = func(context.Context) (site.PublicRules, error) {
		loads++
		return site.PublicRules{
			Shell: site.ShellPresentation{Name: "Configured Board", Description: "Configured description", Theme: "emerald"},
			HTML:  rendered.TrustedHTML(),
		}, nil
	}
	public, _, err := newSiteSettingsHandler(callbackTestURLBuilder(t), &captureAbuseObserver{}, services)
	if err != nil {
		t.Fatalf("newSiteSettingsHandler() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	public.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/rules", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || loads != 1 || !strings.Contains(body, "Configured Board") || !strings.Contains(body, "Be decent") || !strings.Contains(body, `data-brand-theme="emerald"`) {
		t.Fatalf("rules response = (status %d, loads %d, body %q)", response.Code, loads, body)
	}
	for _, forbidden := range []string{"rules_markdown", "rules_renderer_version", "administration_revision"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("public rules response exposes %q", forbidden)
		}
	}
	for _, target := range []string{"/rules?probe=1", "/rules?", "/rules/"} {
		response = httptest.NewRecorder()
		public.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusNotFound || loads != 1 {
			t.Fatalf("noncanonical %q = (status %d, loads %d)", target, response.Code, loads)
		}
	}
}

func TestSiteSettingsHandlerRequiresAbuseObserver(t *testing.T) {
	t.Parallel()
	public, private, err := newSiteSettingsHandler(callbackTestURLBuilder(t), nil, validSiteHTTPServices())
	if err == nil || public != nil || private != nil {
		t.Fatalf("newSiteSettingsHandler(nil observer) = (%v, %v, %v)", public, private, err)
	}
}

func TestSiteSettingsGETAndPOSTPreserveAuthorizationAndFullShellRefresh(t *testing.T) {
	t.Parallel()
	services := validSiteHTTPServices()
	editableCalls, updateCalls := 0, 0
	services.Editable = func(_ context.Context, actor auth.AccessContext) (site.EditableSettings, error) {
		editableCalls++
		if actor.UserID != 7 || actor.Role != auth.RoleAdministrator {
			t.Fatalf("editable actor = %+v", actor)
		}
		return site.EditableSettings{
			Shell:         site.ShellPresentation{Name: "Configured Board", Description: "Configured description", Theme: "cyan"},
			RulesMarkdown: "# Rules", RendererVersion: contentrender.RendererVersion, Revision: 4,
		}, nil
	}
	services.Update = func(_ context.Context, actor auth.AccessContext, input site.SettingsInput, requestID pgtype.UUID) (site.MutationResult, error) {
		updateCalls++
		want := site.SettingsInput{Name: "Changed Board", Description: "Changed description", Theme: "rose", RulesMarkdown: "# New rules", Reason: "Update public presentation", Revision: 4}
		if !reflect.DeepEqual(actor, auth.AccessContext{Authenticated: true, UserID: 7, Role: auth.RoleAdministrator}) || input != want || !requestID.Valid || requestID.Bytes[0] != 0x51 {
			t.Fatalf("site settings update = (%+v, %+v, %+v)", actor, input, requestID)
		}
		return site.MutationResult{Revision: 5, AuditID: 41}, nil
	}
	_, private, err := newSiteSettingsHandler(callbackTestURLBuilder(t), &captureAbuseObserver{}, services)
	if err != nil {
		t.Fatalf("newSiteSettingsHandler() returned error: %v", err)
	}
	private = withModerationTestRequestID(t, private)
	token := validCSRFTokenForTest(0x61)
	contextualize := func(request *http.Request) *http.Request {
		ctx := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, auth.SessionAuthentication{
			SessionID: 9, Access: auth.AccessContext{Authenticated: true, UserID: 7, Role: auth.RoleAdministrator},
		})
		ctx = context.WithValue(ctx, csrfTokenContextKey{}, token)
		return request.WithContext(ctx)
	}

	response := httptest.NewRecorder()
	private.ServeHTTP(response, contextualize(httptest.NewRequest(http.MethodGet, "/admin/settings", nil)))
	if response.Code != http.StatusOK || editableCalls != 1 || !strings.Contains(response.Body.String(), "Configured Board") || !strings.Contains(response.Body.String(), `name="revision" value="4"`) {
		t.Fatalf("settings GET = (status %d, editable %d, body %q)", response.Code, editableCalls, response.Body.String())
	}

	form := url.Values{
		"_csrf": {token}, "site_name": {"Changed Board"}, "site_description": {"Changed description"},
		"brand_theme": {"rose"}, "rules_markdown": {"# New rules"}, "reason": {"Update public presentation"}, "revision": {"4"},
	}
	request := contextualize(httptest.NewRequest(http.MethodPost, "/admin/settings", strings.NewReader(form.Encode())))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("HX-Request", "true")
	response = httptest.NewRecorder()
	private.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("HX-Redirect") != "/bb/admin/settings" || response.Header().Get("HX-Location") != "" || updateCalls != 1 || editableCalls != 1 {
		t.Fatalf("settings POST = (status %d, redirect %q, location %q, updates %d, editable %d)", response.Code, response.Header().Get("HX-Redirect"), response.Header().Get("HX-Location"), updateCalls, editableCalls)
	}
}

func TestSiteSettingsPreflightRejectsBeforeBodyAndMutation(t *testing.T) {
	t.Parallel()
	services := validSiteHTTPServices()
	services.Editable = func(context.Context, auth.AccessContext) (site.EditableSettings, error) {
		panic("noncanonical settings request loaded private state")
	}
	services.Update = func(context.Context, auth.AccessContext, site.SettingsInput, pgtype.UUID) (site.MutationResult, error) {
		panic("noncanonical settings request mutated state")
	}
	_, private, err := newSiteSettingsHandler(callbackTestURLBuilder(t), &captureAbuseObserver{}, services)
	if err != nil {
		t.Fatalf("newSiteSettingsHandler() returned error: %v", err)
	}
	body := &countingReadCloser{reader: strings.NewReader("secret=body")}
	request := httptest.NewRequest(http.MethodPost, "/admin/settings?probe=1", nil)
	request.Body = body
	request.ContentLength = 11
	ctx := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, auth.SessionAuthentication{
		SessionID: 9, Access: auth.AccessContext{Authenticated: true, UserID: 7, Role: auth.RoleAdministrator},
	})
	request = request.WithContext(ctx)
	response := httptest.NewRecorder()
	private.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || body.reads != 0 {
		t.Fatalf("noncanonical settings POST = (status %d, reads %d)", response.Code, body.reads)
	}
}

func TestSiteSettingsPOSTRejectsAuthorityAndSizeBeforeBody(t *testing.T) {
	t.Parallel()
	updateCalls := 0
	services := validSiteHTTPServices()
	services.Update = func(context.Context, auth.AccessContext, site.SettingsInput, pgtype.UUID) (site.MutationResult, error) {
		updateCalls++
		return site.MutationResult{}, errors.New("settings mutation must not run")
	}
	_, private, err := newSiteSettingsHandler(callbackTestURLBuilder(t), &captureAbuseObserver{}, services)
	if err != nil {
		t.Fatalf("newSiteSettingsHandler() returned error: %v", err)
	}
	token := validCSRFTokenForTest(0x61)
	adminContext := func(request *http.Request) *http.Request {
		ctx := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, auth.SessionAuthentication{
			SessionID: 9, Access: auth.AccessContext{Authenticated: true, UserID: 7, Role: auth.RoleAdministrator},
		})
		return request.WithContext(context.WithValue(ctx, csrfTokenContextKey{}, token))
	}
	staleContext := func(request *http.Request) *http.Request {
		ctx := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, auth.SessionAuthentication{SessionID: 9})
		return request.WithContext(ctx)
	}

	tests := []struct {
		name          string
		contextualize func(*http.Request) *http.Request
		headerToken   string
		contentLength int64
		wantStatus    int
	}{
		{name: "missing session", contextualize: func(request *http.Request) *http.Request { return request }, wantStatus: http.StatusSeeOther},
		{name: "stale session", contextualize: staleContext, wantStatus: http.StatusSeeOther},
		{name: "malformed header CSRF", contextualize: adminContext, headerToken: "malformed", wantStatus: http.StatusForbidden},
		{name: "declared body over limit", contextualize: adminContext, contentLength: maximumSiteSettingsFormBytes + 1, wantStatus: http.StatusForbidden},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			body := &countingReadCloser{reader: strings.NewReader("secret=body")}
			request := httptest.NewRequest(http.MethodPost, "/admin/settings", nil)
			request.Body = body
			request.ContentLength = test.contentLength
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if test.headerToken != "" {
				request.Header.Set(csrfHeaderName, test.headerToken)
			}
			request = test.contextualize(request)
			response := httptest.NewRecorder()
			private.ServeHTTP(response, request)
			if response.Code != test.wantStatus || body.reads != 0 || updateCalls != 0 || !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
				t.Fatalf("settings POST = (status %d, reads %d, updates %d, cache %q)", response.Code, body.reads, updateCalls, response.Header().Get("Cache-Control"))
			}
			if strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("settings rejection exposed submitted body: %q", response.Body.String())
			}
		})
	}
}

func TestSiteSettingsPOSTRejectsDuplicateAndUnknownFields(t *testing.T) {
	t.Parallel()
	updateCalls := 0
	services := validSiteHTTPServices()
	services.Update = func(context.Context, auth.AccessContext, site.SettingsInput, pgtype.UUID) (site.MutationResult, error) {
		updateCalls++
		return site.MutationResult{}, errors.New("settings mutation must not run")
	}
	_, private, err := newSiteSettingsHandler(callbackTestURLBuilder(t), &captureAbuseObserver{}, services)
	if err != nil {
		t.Fatalf("newSiteSettingsHandler() returned error: %v", err)
	}
	token := validCSRFTokenForTest(0x61)
	valid := url.Values{
		"_csrf": {token}, "site_name": {"Board"}, "site_description": {"Description"},
		"brand_theme": {"blue"}, "rules_markdown": {"# Rules"}, "reason": {"Update rules"}, "revision": {"1"},
	}
	for _, test := range []struct {
		name string
		form url.Values
	}{
		{name: "unknown", form: func() url.Values { form := cloneValues(valid); form.Set("unknown", "value"); return form }()},
		{name: "duplicate", form: func() url.Values {
			form := cloneValues(valid)
			form["site_name"] = []string{"Board", "Other"}
			return form
		}()},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodPost, "/admin/settings", strings.NewReader(test.form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set(csrfHeaderName, token)
			ctx := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, auth.SessionAuthentication{
				SessionID: 9, Access: auth.AccessContext{Authenticated: true, UserID: 7, Role: auth.RoleAdministrator},
			})
			ctx = context.WithValue(ctx, csrfTokenContextKey{}, token)
			response := httptest.NewRecorder()
			private.ServeHTTP(response, request.WithContext(ctx))
			if response.Code != http.StatusBadRequest || updateCalls != 0 || !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
				t.Fatalf("settings POST = (status %d, updates %d, cache %q)", response.Code, updateCalls, response.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestExactSiteRoutePreflightRejectsBeforeSessionBodyAndDatabase(t *testing.T) {
	t.Parallel()
	nextCalls := 0
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		nextCalls++
	})
	settings := withExactSiteRoutePreflight(next, "/admin/settings", http.MethodGet, http.MethodPost)
	rules := withExactSiteRoutePreflight(next, "/rules", http.MethodGet)

	for _, test := range []struct {
		name    string
		handler http.Handler
		method  string
		target  string
		status  int
	}{
		{name: "settings query", handler: settings, method: http.MethodPost, target: "/admin/settings?probe=1", status: http.StatusNotFound},
		{name: "settings force query", handler: settings, method: http.MethodGet, target: "/admin/settings?", status: http.StatusNotFound},
		{name: "settings method", handler: settings, method: http.MethodPut, target: "/admin/settings", status: http.StatusMethodNotAllowed},
		{name: "rules query", handler: rules, method: http.MethodGet, target: "/rules?probe=1", status: http.StatusNotFound},
		{name: "rules method", handler: rules, method: http.MethodPost, target: "/rules", status: http.StatusMethodNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &countingReadCloser{reader: strings.NewReader("secret=body")}
			request := httptest.NewRequest(test.method, test.target, nil)
			request.Body = body
			request.ContentLength = 11
			response := httptest.NewRecorder()
			test.handler.ServeHTTP(response, request)
			if response.Code != test.status || body.reads != 0 || nextCalls != 0 || request.Pattern == "" || strings.Contains(request.Pattern, "?") {
				t.Fatalf("preflight response = (status %d, reads %d, next %d, pattern %q)", response.Code, body.reads, nextCalls, request.Pattern)
			}
			if strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "GOTTH") {
				t.Fatalf("preflight response leaked branded or submitted state: %q", response.Body.String())
			}
		})
	}
}

func TestSiteSettingsFailuresDoNotLeakServiceDetails(t *testing.T) {
	t.Parallel()
	services := validSiteHTTPServices()
	services.Rules = func(context.Context) (site.PublicRules, error) {
		return site.PublicRules{}, errors.New("postgres secret")
	}
	public, _, err := newSiteSettingsHandler(callbackTestURLBuilder(t), &captureAbuseObserver{}, services)
	if err != nil {
		t.Fatalf("newSiteSettingsHandler() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	public.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/rules", nil))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "postgres") || strings.Contains(response.Body.String(), "GOTTH") {
		t.Fatalf("rules failure = (status %d, body %q)", response.Code, response.Body.String())
	}
}

func validSiteHTTPServices() SiteHTTPServices {
	return SiteHTTPServices{
		Shell: func(context.Context) (site.ShellPresentation, error) {
			return site.ShellPresentation{Name: "Board", Description: "Description", Theme: "blue"}, nil
		},
		Rules: func(context.Context) (site.PublicRules, error) {
			return site.PublicRules{Shell: site.ShellPresentation{Name: "Board", Description: "Description", Theme: "blue"}}, nil
		},
		Editable: func(context.Context, auth.AccessContext) (site.EditableSettings, error) {
			return site.EditableSettings{Shell: site.ShellPresentation{Name: "Board", Description: "Description", Theme: "blue"}, RendererVersion: contentrender.RendererVersion, Revision: 1}, nil
		},
		Update: func(context.Context, auth.AccessContext, site.SettingsInput, pgtype.UUID) (site.MutationResult, error) {
			return site.MutationResult{Revision: 2, AuditID: 1}, nil
		},
		Control: func(context.Context) (control.Settings, error) {
			return control.Settings{Registration: control.RegistrationClosed}, nil
		},
	}
}

type countingReadCloser struct {
	reader io.Reader
	reads  int
}

func (body *countingReadCloser) Read(buffer []byte) (int, error) {
	body.reads++
	return body.reader.Read(buffer)
}

func (*countingReadCloser) Close() error { return nil }
