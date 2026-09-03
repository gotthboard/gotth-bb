package httpui

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/administration"
	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/observability"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestAreaAdministrationGETRequiresFreshAdministratorAndRendersForms(t *testing.T) {
	t.Parallel()
	admin := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleAdministrator}}
	page := areaAdministrationTestPage()
	loads := 0
	handler := newAreaAdministrationTestHandler(t, func(_ context.Context, actor auth.AccessContext) (administration.AreaManagementPage, error) {
		loads++
		if !reflect.DeepEqual(actor, admin.Access) {
			t.Fatalf("loader actor = %+v", actor)
		}
		return page, nil
	}, panicAreaCreator, panicAreaUpdater)
	for _, test := range []struct {
		name         string
		auth         auth.SessionAuthentication
		wantStatus   int
		wantLocation string
		wantLoads    int
	}{
		{name: "anonymous", wantStatus: http.StatusSeeOther, wantLocation: "/bb/login?return=%2Fbb%2Fadmin%2Fareas"},
		{name: "stale", auth: func() auth.SessionAuthentication { value := admin; value.RequiresRevalidation = true; return value }(), wantStatus: http.StatusSeeOther, wantLocation: "/bb/auth/revalidate?return=%2Fbb%2Fadmin%2Fareas"},
		{name: "moderator", auth: auth.SessionAuthentication{SessionID: 8, Access: auth.AccessContext{Authenticated: true, UserID: 41, Role: auth.RoleModerator}}, wantStatus: http.StatusForbidden},
		{name: "administrator", auth: admin, wantStatus: http.StatusOK, wantLoads: 1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			request := areaAdministrationTestRequest(http.MethodGet, "/admin/areas", nil, test.auth)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || response.Header().Get("Location") != test.wantLocation {
				t.Fatalf("GET response = (%d, %q, %q)", response.Code, response.Header().Get("Location"), response.Body.String())
			}
			if test.wantStatus == http.StatusOK {
				for _, want := range []string{"Manage discussion areas", "Create area", "General", `/bb/admin/areas/3`, `name="revision"`, "2026-09-02T12:00:00Z", "Members", `hx-post="/bb/admin/areas"`, `hx-post="/bb/admin/areas/3"`, `hx-target="#main-content"`} {
					if !strings.Contains(response.Body.String(), want) {
						t.Fatalf("GET body missing %q: %q", want, response.Body.String())
					}
				}
			}
		})
	}
	if loads != 1 {
		t.Fatalf("loader calls = %d, want 1", loads)
	}
}

func TestAuthenticatedRouterDispatchesOnlyCanonicalAreaAdministrationPaths(t *testing.T) {
	t.Parallel()
	builder := callbackTestURLBuilder(t)
	sessionToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x61}, 32))
	csrf, err := deriveCSRFToken(sessionToken)
	if err != nil {
		t.Fatalf("deriveCSRFToken() returned error: %v", err)
	}
	service := &authenticatedHandlerTestService{}
	service.authenticate = func(context.Context, string) (auth.SessionAuthentication, error) {
		service.authenticateCalls++
		return auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleAdministrator}}, nil
	}
	creates := 0
	handler, err := newAuthenticatedHandler(
		builder, service, emptyAreaIndexLister, panicAreaTopicPageLoader, store.MaximumTopicPage,
		panicTopicPostPageLoader, store.MaximumPostPage, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		func(context.Context, auth.AccessContext) (administration.AreaManagementPage, error) {
			return administration.AreaManagementPage{}, nil
		},
		func(_ context.Context, _ auth.AccessContext, input administration.AreaInput, _ pgtype.UUID) (administration.AreaMutationResult, error) {
			creates++
			return administration.AreaMutationResult{AreaID: 1, Slug: input.Slug, AuditID: 2}, nil
		},
		panicAreaUpdater, url.URL{}, false, nil, nil, "gotth_bb_session", true, unavailableReadiness,
	)
	if err != nil {
		t.Fatalf("newAuthenticatedHandler() returned error: %v", err)
	}
	handler = withModerationTestRequestID(t, handler)
	get := httptest.NewRequest(http.MethodGet, "/admin/areas", nil)
	get.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK || service.authenticateCalls != 1 {
		t.Fatalf("GET route = (status %d, auth %d, body %q)", getResponse.Code, service.authenticateCalls, getResponse.Body.String())
	}
	form := url.Values{"_csrf": {csrf}, "slug": {"general"}, "name": {"General"}, "description": {""}, "display_order": {"0"}, "visibility": {"public"}, "posting_mode": {"normal"}, "reason": {"Create area"}}
	post := httptest.NewRequest(http.MethodPost, "/admin/areas", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	postResponse := httptest.NewRecorder()
	handler.ServeHTTP(postResponse, post)
	if postResponse.Code != http.StatusSeeOther || service.authenticateCalls != 2 || creates != 1 {
		t.Fatalf("POST route = (status %d, auth %d, creates %d, body %q)", postResponse.Code, service.authenticateCalls, creates, postResponse.Body.String())
	}
	malformed := httptest.NewRequest(http.MethodPost, "/admin/areas/01", nil)
	malformed.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	malformedResponse := httptest.NewRecorder()
	handler.ServeHTTP(malformedResponse, malformed)
	if malformedResponse.Code != http.StatusNotFound || service.authenticateCalls != 2 || creates != 1 {
		t.Fatalf("malformed route = (status %d, auth %d, creates %d)", malformedResponse.Code, service.authenticateCalls, creates)
	}
}

func TestAreaAdministrationPOSTCreatesAndUpdatesWithExactAuthority(t *testing.T) {
	t.Parallel()
	admin := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleAdministrator}}
	wantUUID := pgtype.UUID{Bytes: [16]byte{0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51}, Valid: true}
	creates, updates := 0, 0
	handler := newAreaAdministrationTestHandler(t, func(context.Context, auth.AccessContext) (administration.AreaManagementPage, error) {
		return areaAdministrationTestPage(), nil
	}, func(_ context.Context, actor auth.AccessContext, input administration.AreaInput, requestID pgtype.UUID) (administration.AreaMutationResult, error) {
		creates++
		want := administration.AreaInput{Slug: "general", Name: "General", Description: "Discuss anything", DisplayOrder: 4, Visibility: policy.VisibilityPublic, PostingMode: policy.PostingNormal, GroupIDs: []int64{}, Reason: "Create general discussion"}
		if !reflect.DeepEqual(actor, admin.Access) || !reflect.DeepEqual(input, want) || requestID != wantUUID {
			t.Fatalf("create call = (%+v, %+v, %+v)", actor, input, requestID)
		}
		return administration.AreaMutationResult{AreaID: 9, Slug: input.Slug, AuditID: 11}, nil
	}, func(_ context.Context, actor auth.AccessContext, areaID int64, input administration.AreaInput, requestID pgtype.UUID) (administration.AreaMutationResult, error) {
		updates++
		wantRevision := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
		if !reflect.DeepEqual(actor, admin.Access) || areaID != 3 || input.Slug != "general" || input.Name != "General updated" || input.Visibility != policy.VisibilityGroups || !reflect.DeepEqual(input.GroupIDs, []int64{2, 7}) || !input.Revision.Equal(wantRevision) || requestID != wantUUID {
			t.Fatalf("update call = (%+v, %d, %+v, %+v)", actor, areaID, input, requestID)
		}
		return administration.AreaMutationResult{AreaID: areaID, Slug: input.Slug, AuditID: 12}, nil
	})
	createForm := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "slug": {"general"}, "name": {"General"}, "description": {"Discuss anything"}, "display_order": {"4"}, "visibility": {"public"}, "posting_mode": {"normal"}, "reason": {"Create general discussion"}}
	updateForm := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "slug": {"general"}, "name": {"General updated"}, "description": {"Discuss anything"}, "display_order": {"5"}, "visibility": {"groups"}, "posting_mode": {"read_only"}, "group_id": {"7", "2"}, "reason": {"Restrict area"}, "revision": {"2026-09-02T12:00:00Z"}}
	for _, test := range []struct {
		target, name string
		form         url.Values
	}{
		{target: "/admin/areas", name: "create", form: createForm},
		{target: "/admin/areas/3", name: "update", form: updateForm},
	} {
		for _, fragment := range []bool{false, true} {
			request := areaAdministrationTestRequest(http.MethodPost, test.target, test.form, admin)
			if fragment {
				request.Header.Set("HX-Request", "true")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if fragment {
				wantLocation := `{"path":"/bb/admin/areas","target":"#main-content","swap":"outerHTML"}`
				if response.Code != http.StatusNoContent || response.Header().Get("HX-Location") != wantLocation || response.Header().Get("HX-Redirect") != "" || response.Header().Get("Location") != "" {
					t.Fatalf("HTMX %s = (%d, headers %v, body %q)", test.name, response.Code, response.Header(), response.Body.String())
				}
			} else if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/bb/admin/areas" || response.Header().Get("HX-Location") != "" {
				t.Fatalf("ordinary %s = (%d, headers %v, body %q)", test.name, response.Code, response.Header(), response.Body.String())
			}
		}
	}
	if creates != 2 || updates != 2 {
		t.Fatalf("mutation calls = (%d, %d)", creates, updates)
	}
}

func TestAreaAdministrationRejectsCSRFMalformedAndServiceFailures(t *testing.T) {
	t.Parallel()
	admin := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleAdministrator}}
	calls := 0
	creator := func(context.Context, auth.AccessContext, administration.AreaInput, pgtype.UUID) (administration.AreaMutationResult, error) {
		calls++
		return administration.AreaMutationResult{}, administration.ErrAreaAdministrationInput
	}
	handler := newAreaAdministrationTestHandler(t, func(context.Context, auth.AccessContext) (administration.AreaManagementPage, error) {
		return areaAdministrationTestPage(), nil
	}, creator, panicAreaUpdater)
	valid := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "slug": {"general"}, "name": {"General"}, "description": {""}, "display_order": {"0"}, "visibility": {"public"}, "posting_mode": {"normal"}, "reason": {"Create area"}}
	for _, test := range []struct {
		name       string
		form       url.Values
		wantStatus int
		wantCalls  int
	}{
		{name: "bad csrf", form: func() url.Values { value := cloneValues(valid); value.Set("_csrf", "bad"); return value }(), wantStatus: http.StatusForbidden},
		{name: "unknown field", form: func() url.Values { value := cloneValues(valid); value.Set("extra", "bad"); return value }(), wantStatus: http.StatusBadRequest},
		{name: "service validation", form: valid, wantStatus: http.StatusUnprocessableEntity, wantCalls: 1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			request := areaAdministrationTestRequest(http.MethodPost, "/admin/areas", test.form, admin)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("response = (%d, %q), want %d", response.Code, response.Body.String(), test.wantStatus)
			}
		})
	}
	if calls != 1 {
		t.Fatalf("creator calls = %d, want 1", calls)
	}
}

func newAreaAdministrationTestHandler(t *testing.T, load AreaAdministrationLoader, create AreaCreator, update AreaUpdater) http.Handler {
	t.Helper()
	handler, err := newAreaAdministrationHandler(callbackTestURLBuilder(t), load, create, update)
	if err != nil {
		t.Fatalf("newAreaAdministrationHandler() returned error: %v", err)
	}
	wrapped, err := observability.NewRequestIDMiddleware(handler, func() (string, error) { return moderationTestRequestID, nil })
	if err != nil {
		t.Fatalf("NewRequestIDMiddleware() returned error: %v", err)
	}
	return wrapped
}

func areaAdministrationTestRequest(method, target string, form url.Values, authentication auth.SessionAuthentication) *http.Request {
	var body *strings.Reader
	if form == nil {
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(form.Encode())
	}
	request := httptest.NewRequest(method, target, body)
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	ctx := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, authentication)
	ctx = context.WithValue(ctx, csrfTokenContextKey{}, validCSRFTokenForTest(0x51))
	return request.WithContext(ctx)
}

func areaAdministrationTestPage() administration.AreaManagementPage {
	return administration.AreaManagementPage{
		Areas:  []administration.ManagedArea{{ID: 3, Slug: "general", Name: "General", Description: "Talk here", Visibility: policy.VisibilityPublic, PostingMode: policy.PostingNormal, UpdatedAt: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)}},
		Groups: []administration.ForumGroup{{ID: 2, Name: "Members"}, {ID: 7, Name: "Staff"}},
	}
}

func panicAreaCreator(context.Context, auth.AccessContext, administration.AreaInput, pgtype.UUID) (administration.AreaMutationResult, error) {
	panic("area creator called")
}

func panicAreaUpdater(context.Context, auth.AccessContext, int64, administration.AreaInput, pgtype.UUID) (administration.AreaMutationResult, error) {
	panic("area updater called")
}

func cloneValues(source url.Values) url.Values {
	clone := make(url.Values, len(source))
	for key, values := range source {
		clone[key] = append([]string(nil), values...)
	}
	return clone
}
