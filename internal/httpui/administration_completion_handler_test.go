package httpui

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/administration"
	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestAdministrationCompletionGETRoutesRenderBoundedPages(t *testing.T) {
	t.Parallel()
	services := administrationCompletionTestServices()
	handler, err := newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
	if err != nil {
		t.Fatalf("newAdministrationCompletionHandler(): %v", err)
	}
	admin := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 1, Role: auth.RoleAdministrator}}
	for _, test := range []struct {
		path    string
		wants   []string
		forbids []string
	}{
		{path: "/admin", wants: []string{"Board administration", "Reports in review", "2026-09-08T12:00:00Z"}},
		{path: "/admin/accounts", wants: []string{"Accounts", "Local Member", "/bb/admin/accounts/2"}},
		{path: "/admin/accounts/2", wants: []string{"Local Member", "Change role", "Members", "Group membership", "/bb/admin/accounts/2/groups/4"}, forbids: []string{`name="group_id"`}},
		{path: "/admin/groups", wants: []string{"Groups", "Create group", "Members"}},
		{path: "/admin/areas", wants: []string{"Areas", "Create area", "General", "/bb/admin/areas/3"}},
		{path: "/admin/areas/3", wants: []string{"General", "Update area", "Group access", "Members", "/bb/admin/areas/3/groups/4"}, forbids: []string{`name="group_id"`, `name="slug"`}},
	} {
		test := test
		t.Run(test.path, func(t *testing.T) {
			request := areaAdministrationTestRequest(http.MethodGet, test.path, nil, admin)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("GET %s = (%d, %q, %q)", test.path, response.Code, response.Header().Get("Cache-Control"), response.Body.String())
			}
			for _, want := range test.wants {
				if !strings.Contains(response.Body.String(), want) {
					t.Fatalf("GET %s missing %q: %q", test.path, want, response.Body.String())
				}
			}
			for _, forbidden := range test.forbids {
				if strings.Contains(response.Body.String(), forbidden) {
					t.Fatalf("GET %s contains forbidden %q: %q", test.path, forbidden, response.Body.String())
				}
			}
		})
	}
}

func TestAdministrationCompletionMutationUsesStrictFormAndHTMXNavigation(t *testing.T) {
	t.Parallel()
	services := administrationCompletionTestServices()
	calls := 0
	services.RenameGroup = func(_ context.Context, actor auth.AccessContext, groupID int64, name, reason string, revision int64, requestID pgtype.UUID) (administration.GroupMutationResult, error) {
		calls++
		if actor.UserID != 1 || groupID != 4 || name != "Registered Members" || reason != "Clarify access" || revision != 5 || !requestID.Valid {
			t.Fatalf("rename args = (%+v,%d,%q,%q,%d,%+v)", actor, groupID, name, reason, revision, requestID)
		}
		return administration.GroupMutationResult{GroupID: 4, Name: name, Revision: 6, AuditID: 9}, nil
	}
	handler, err := newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
	if err != nil {
		t.Fatal(err)
	}
	handler = withModerationTestRequestID(t, handler)
	admin := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 1, Role: auth.RoleAdministrator}}
	form := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "name": {"Registered Members"}, "reason": {"Clarify access"}, "revision": {"5"}}
	request := areaAdministrationTestRequest(http.MethodPost, "/admin/groups/4", form, admin)
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("HX-Location") != `{"path":"/bb/admin/groups","target":"#main-content","swap":"outerHTML"}` || calls != 1 {
		t.Fatalf("rename = (%d,%q,%d,%q)", response.Code, response.Header().Get("HX-Location"), calls, response.Body.String())
	}
	bad := cloneValues(form)
	bad["surprise"] = []string{"1"}
	badRequest := areaAdministrationTestRequest(http.MethodPost, "/admin/groups/4", bad, admin)
	badResponse := httptest.NewRecorder()
	handler.ServeHTTP(badResponse, badRequest)
	if badResponse.Code != http.StatusBadRequest || calls != 1 {
		t.Fatalf("bad form = (%d,%d,%q)", badResponse.Code, calls, badResponse.Body.String())
	}
}

func TestAdministrationCompletionTargetsMembershipOnlyFromCanonicalPath(t *testing.T) {
	t.Parallel()
	services := administrationCompletionTestServices()
	calls := 0
	services.ChangeMembership = func(_ context.Context, actor auth.AccessContext, userID, groupID int64, grant bool, reason string, revision int64, requestID pgtype.UUID) (administration.AccountMutationResult, error) {
		calls++
		if actor.UserID != 1 || userID != 2 || groupID != 4 || !grant || reason != "Grant access" || revision != 3 || !requestID.Valid {
			t.Fatalf("membership args = (%+v,%d,%d,%t,%q,%d,%+v)", actor, userID, groupID, grant, reason, revision, requestID)
		}
		return administration.AccountMutationResult{UserID: 2, Revision: 4, AuditID: 8}, nil
	}
	handler, err := newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
	if err != nil {
		t.Fatal(err)
	}
	handler = withModerationTestRequestID(t, handler)
	admin := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 1, Role: auth.RoleAdministrator}}
	form := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "action": {"grant"}, "reason": {"Grant access"}, "revision": {"3"}}
	request := areaAdministrationTestRequest(http.MethodPost, "/admin/accounts/2/groups/4", form, admin)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/bb/admin/accounts/2" || calls != 1 {
		t.Fatalf("membership = (%d,%q,%d,%q)", response.Code, response.Header().Get("Location"), calls, response.Body.String())
	}
	bad := cloneValues(form)
	bad.Set("group_id", "5")
	badRequest := areaAdministrationTestRequest(http.MethodPost, "/admin/accounts/2/groups/4", bad, admin)
	badResponse := httptest.NewRecorder()
	handler.ServeHTTP(badResponse, badRequest)
	if badResponse.Code != http.StatusBadRequest || calls != 1 {
		t.Fatalf("body target = (%d,%d,%q)", badResponse.Code, calls, badResponse.Body.String())
	}
}

func TestAdministrationAreaCursorIsExactPair(t *testing.T) {
	t.Parallel()
	services := administrationCompletionTestServices()
	services.ListAreas = func(_ context.Context, _ auth.AccessContext, order int32, id int64) (administration.AreaPage, error) {
		if order != 7 || id != 19 {
			t.Fatalf("area cursor = (%d,%d)", order, id)
		}
		return administration.AreaPage{}, nil
	}
	handler, err := newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
	if err != nil {
		t.Fatal(err)
	}
	admin := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 1, Role: auth.RoleAdministrator}}
	request := areaAdministrationTestRequest(http.MethodGet, "/admin/areas?after_order=7&after_id=19", nil, admin)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("area cursor response = (%d,%q)", response.Code, response.Body.String())
	}
	for _, target := range []string{"/admin/areas?after=19", "/admin/areas?after_order=7", "/admin/areas?after_order=07&after_id=19", "/admin/areas?after_order=-1&after_id=19", "/admin/areas?after_order=7&after_id=019"} {
		invalid := httptest.NewRequest(http.MethodGet, target, nil)
		invalidResponse := httptest.NewRecorder()
		withAdministrationPreflight(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatalf("delegated %s", target) })).ServeHTTP(invalidResponse, invalid)
		if invalidResponse.Code != http.StatusNotFound {
			t.Fatalf("invalid cursor %s = %d", target, invalidResponse.Code)
		}
	}
}

func TestAdministrationAreaMutationsTakeImmutableTargetsFromPaths(t *testing.T) {
	t.Parallel()
	services := administrationCompletionTestServices()
	updateCalls, groupCalls := 0, 0
	services.UpdateArea = func(_ context.Context, actor auth.AccessContext, areaID int64, input administration.AreaCoreInput, requestID pgtype.UUID) (administration.AreaCompletionResult, error) {
		updateCalls++
		if actor.UserID != 1 || areaID != 3 || input.Slug != "" || input.Name != "General archive" || input.Visibility != policy.VisibilityPublic || input.PostingMode != policy.PostingArchived || input.Revision != 2 || !requestID.Valid {
			t.Fatalf("area update args = (%+v,%d,%+v,%+v)", actor, areaID, input, requestID)
		}
		return administration.AreaCompletionResult{AreaID: 3, Slug: "general", Revision: 3, AuditID: 9}, nil
	}
	services.ChangeAreaGroup = func(_ context.Context, actor auth.AccessContext, areaID, groupID int64, grant bool, reason string, revision int64, requestID pgtype.UUID) (administration.AreaCompletionResult, error) {
		groupCalls++
		if actor.UserID != 1 || areaID != 3 || groupID != 4 || grant || reason != "Remove access" || revision != 3 || !requestID.Valid {
			t.Fatalf("area group args = (%+v,%d,%d,%t,%q,%d,%+v)", actor, areaID, groupID, grant, reason, revision, requestID)
		}
		return administration.AreaCompletionResult{AreaID: 3, Slug: "general", Revision: 4, AuditID: 10}, nil
	}
	handler, err := newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
	if err != nil {
		t.Fatal(err)
	}
	handler = withModerationTestRequestID(t, handler)
	admin := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 1, Role: auth.RoleAdministrator}}
	updateForm := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "name": {"General archive"}, "description": {"Read only"}, "display_order": {"7"}, "visibility": {"public"}, "posting_mode": {"archived"}, "initial_group_id": {""}, "reason": {"Archive general"}, "revision": {"2"}}
	updateResponse := httptest.NewRecorder()
	handler.ServeHTTP(updateResponse, areaAdministrationTestRequest(http.MethodPost, "/admin/areas/3", updateForm, admin))
	if updateResponse.Code != http.StatusSeeOther || updateCalls != 1 {
		t.Fatalf("area update = (%d,%d,%q)", updateResponse.Code, updateCalls, updateResponse.Body.String())
	}
	groupForm := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "action": {"revoke"}, "reason": {"Remove access"}, "revision": {"3"}}
	groupResponse := httptest.NewRecorder()
	handler.ServeHTTP(groupResponse, areaAdministrationTestRequest(http.MethodPost, "/admin/areas/3/groups/4", groupForm, admin))
	if groupResponse.Code != http.StatusSeeOther || groupCalls != 1 {
		t.Fatalf("area group = (%d,%d,%q)", groupResponse.Code, groupCalls, groupResponse.Body.String())
	}
	bad := cloneValues(groupForm)
	bad.Set("group_id", "5")
	badResponse := httptest.NewRecorder()
	handler.ServeHTTP(badResponse, areaAdministrationTestRequest(http.MethodPost, "/admin/areas/3/groups/4", bad, admin))
	if badResponse.Code != http.StatusBadRequest || groupCalls != 1 {
		t.Fatalf("area body target = (%d,%d,%q)", badResponse.Code, groupCalls, badResponse.Body.String())
	}
}

func TestAdministrationCompletionRemainingMutationFormsAreExact(t *testing.T) {
	t.Parallel()
	admin := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 1, Role: auth.RoleAdministrator}}
	token := validCSRFTokenForTest(0x51)

	t.Run("account role", func(t *testing.T) {
		services := administrationCompletionTestServices()
		calls := 0
		services.ChangeRole = func(_ context.Context, actor auth.AccessContext, userID int64, role, expected policy.Role, reason string, revision int64, requestID pgtype.UUID) (administration.AccountMutationResult, error) {
			calls++
			if actor.UserID != 1 || userID != 2 || role != policy.RoleModerator || expected != policy.RoleMember || reason != "Promote reviewer" || revision != 3 || !requestID.Valid {
				t.Fatalf("role args = (%+v,%d,%q,%q,%q,%d,%+v)", actor, userID, role, expected, reason, revision, requestID)
			}
			return administration.AccountMutationResult{UserID: 2, Revision: 4, AuditID: 8}, nil
		}
		handler, err := newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
		if err != nil {
			t.Fatal(err)
		}
		handler = withModerationTestRequestID(t, handler)
		form := url.Values{"_csrf": {token}, "role": {"moderator"}, "expected_role": {"member"}, "reason": {"Promote reviewer"}, "revision": {"3"}}
		assertAdministrationFormResult(t, handler, "/admin/accounts/2/role", form, admin, "/bb/admin/accounts/2", &calls)
		assertAdministrationRejectedForms(t, handler, "/admin/accounts/2/role", form, admin, &calls)
	})

	t.Run("group create", func(t *testing.T) {
		services := administrationCompletionTestServices()
		calls := 0
		services.CreateGroup = func(_ context.Context, actor auth.AccessContext, name, reason string, requestID pgtype.UUID) (administration.GroupMutationResult, error) {
			calls++
			if actor.UserID != 1 || name != "Reviewers" || reason != "Create review group" || !requestID.Valid {
				t.Fatalf("group args = (%+v,%q,%q,%+v)", actor, name, reason, requestID)
			}
			return administration.GroupMutationResult{GroupID: 6, Revision: 1, AuditID: 9}, nil
		}
		handler, err := newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
		if err != nil {
			t.Fatal(err)
		}
		handler = withModerationTestRequestID(t, handler)
		form := url.Values{"_csrf": {token}, "name": {"Reviewers"}, "reason": {"Create review group"}}
		assertAdministrationFormResult(t, handler, "/admin/groups", form, admin, "/bb/admin/groups", &calls)
		assertAdministrationRejectedForms(t, handler, "/admin/groups", form, admin, &calls)
	})

	for _, test := range []struct {
		name, initial string
	}{
		{name: "area create with initial group", initial: "4"},
		{name: "area create without initial group", initial: ""},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			services := administrationCompletionTestServices()
			calls := 0
			services.CreateArea = func(_ context.Context, actor auth.AccessContext, input administration.AreaCoreInput, requestID pgtype.UUID) (administration.AreaCompletionResult, error) {
				calls++
				wantInitial := int64(0)
				if test.initial != "" {
					wantInitial = 4
				}
				if actor.UserID != 1 || input.Slug != "reviews" || input.Name != "Reviews" || input.Description != "Review discussion" || input.DisplayOrder != 7 || input.Visibility != policy.VisibilityGroups || input.PostingMode != policy.PostingNormal || input.InitialGroupID != wantInitial || input.Reason != "Create review area" || input.Revision != 0 || !requestID.Valid {
					t.Fatalf("area args = (%+v,%+v,%+v)", actor, input, requestID)
				}
				return administration.AreaCompletionResult{AreaID: 9, Slug: "reviews", Revision: 1, AuditID: 10}, nil
			}
			handler, err := newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
			if err != nil {
				t.Fatal(err)
			}
			handler = withModerationTestRequestID(t, handler)
			form := url.Values{"_csrf": {token}, "slug": {"reviews"}, "name": {"Reviews"}, "description": {"Review discussion"}, "display_order": {"7"}, "visibility": {"groups"}, "posting_mode": {"normal"}, "initial_group_id": {test.initial}, "reason": {"Create review area"}}
			assertAdministrationFormResult(t, handler, "/admin/areas", form, admin, "/bb/admin/areas/9", &calls)
			assertAdministrationRejectedForms(t, handler, "/admin/areas", form, admin, &calls)
		})
	}
}

func TestAdministrationCompletionRejectsBeforeReadingMutationBodies(t *testing.T) {
	t.Parallel()
	services := administrationCompletionTestServices()
	services.ChangeRole = func(context.Context, auth.AccessContext, int64, policy.Role, policy.Role, string, int64, pgtype.UUID) (administration.AccountMutationResult, error) {
		panic("role mutation called")
	}
	services.CreateArea = func(context.Context, auth.AccessContext, administration.AreaCoreInput, pgtype.UUID) (administration.AreaCompletionResult, error) {
		panic("area mutation called")
	}
	handler, err := newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
	if err != nil {
		t.Fatal(err)
	}
	admin := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 1, Role: auth.RoleAdministrator}}
	stale := admin
	stale.RequiresRevalidation = true
	member := admin
	member.Access.Role = auth.RoleMember
	token := validCSRFTokenForTest(0x51)
	for _, test := range []struct {
		name           string
		path           string
		authentication auth.SessionAuthentication
		header         string
		contentLength  int64
		wantStatus     int
	}{
		{name: "missing session", path: "/admin/accounts/2/role", wantStatus: http.StatusSeeOther},
		{name: "stale session", path: "/admin/accounts/2/role", authentication: stale, wantStatus: http.StatusSeeOther},
		{name: "member", path: "/admin/accounts/2/role", authentication: member, wantStatus: http.StatusForbidden},
		{name: "invalid header csrf", path: "/admin/accounts/2/role", authentication: admin, header: validCSRFTokenForTest(0x52), wantStatus: http.StatusForbidden},
		{name: "small form declared oversized", path: "/admin/accounts/2/role", authentication: admin, header: token, contentLength: maximumAdministrationSmallFormBytes + 1, wantStatus: http.StatusBadRequest},
		{name: "area form declared oversized", path: "/admin/areas", authentication: admin, header: token, contentLength: maximumAdministrationAreaFormBytes + 1, wantStatus: http.StatusBadRequest},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			body := &countingAdministrationBody{Reader: strings.NewReader("secret=body")}
			request := httptest.NewRequest(http.MethodPost, test.path, body)
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if test.header != "" {
				request.Header.Set(csrfHeaderName, test.header)
			}
			if test.contentLength > 0 {
				request.ContentLength = test.contentLength
			}
			ctx := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, test.authentication)
			ctx = context.WithValue(ctx, csrfTokenContextKey{}, token)
			request = request.WithContext(ctx)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || body.reads != 0 {
				t.Fatalf("response = (status %d, reads %d, body %q)", response.Code, body.reads, response.Body.String())
			}
		})
	}
}

func TestAdministrationCompletionAcceptsExactWorstCaseWireLimits(t *testing.T) {
	t.Parallel()
	admin := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 1, Role: auth.RoleAdministrator}}
	token := validCSRFTokenForTest(0x51)
	for _, test := range []struct {
		name, path, padField, location string
		limit                          int64
		form                           url.Values
	}{
		{name: "small form", path: "/admin/groups", padField: "reason", location: "/bb/admin/groups", limit: maximumAdministrationSmallFormBytes, form: url.Values{"_csrf": {token}, "name": {"Reviewers"}, "reason": {""}}},
		{name: "area form", path: "/admin/areas", padField: "description", location: "/bb/admin/areas/3", limit: maximumAdministrationAreaFormBytes, form: url.Values{"_csrf": {token}, "slug": {"reviews"}, "name": {"Reviews"}, "description": {""}, "display_order": {"7"}, "visibility": {"groups"}, "posting_mode": {"normal"}, "initial_group_id": {"4"}, "reason": {"Create review area"}}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			services := administrationCompletionTestServices()
			calls := 0
			if test.path == "/admin/groups" {
				services.CreateGroup = func(context.Context, auth.AccessContext, string, string, pgtype.UUID) (administration.GroupMutationResult, error) {
					calls++
					return administration.GroupMutationResult{GroupID: 4, Revision: 1, AuditID: 1}, nil
				}
			} else {
				services.CreateArea = func(context.Context, auth.AccessContext, administration.AreaCoreInput, pgtype.UUID) (administration.AreaCompletionResult, error) {
					calls++
					return administration.AreaCompletionResult{AreaID: 3, Revision: 1, AuditID: 1}, nil
				}
			}
			handler, err := newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
			if err != nil {
				t.Fatal(err)
			}
			handler = withModerationTestRequestID(t, handler)
			body := encodedAdministrationFormAtLimit(t, test.form, test.padField, test.limit)
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(body))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			ctx := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, admin)
			ctx = context.WithValue(ctx, csrfTokenContextKey{}, token)
			request = request.WithContext(ctx)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusSeeOther || response.Header().Get("Location") != test.location || calls != 1 || response.Body.Len() != 0 {
				t.Fatalf("exact limit = (status %d, location %q, calls %d, length %d, body %q)", response.Code, response.Header().Get("Location"), calls, len(body), response.Body.String())
			}
		})
	}
}

func encodedAdministrationFormAtLimit(t *testing.T, form url.Values, field string, limit int64) string {
	t.Helper()
	values := cloneValues(form)
	values.Set(field, "")
	remaining := int(limit) - len(values.Encode())
	if remaining < 0 {
		t.Fatalf("base form exceeds limit: %d", remaining)
	}
	values.Set(field, strings.Repeat("\x00", remaining/3)+strings.Repeat("a", remaining%3))
	encoded := values.Encode()
	if len(encoded) != int(limit) {
		t.Fatalf("encoded form length = %d, want %d", len(encoded), limit)
	}
	return encoded
}

func assertAdministrationFormResult(t *testing.T, handler http.Handler, path string, form url.Values, authentication auth.SessionAuthentication, location string, calls *int) {
	t.Helper()
	before := *calls
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, areaAdministrationTestRequest(http.MethodPost, path, form, authentication))
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != location || *calls != before+1 || response.Body.Len() != 0 {
		t.Fatalf("valid form = (status %d, location %q, calls %d, body %q)", response.Code, response.Header().Get("Location"), *calls, response.Body.String())
	}
}

func assertAdministrationRejectedForms(t *testing.T, handler http.Handler, path string, form url.Values, authentication auth.SessionAuthentication, calls *int) {
	t.Helper()
	before := *calls
	unknown := cloneValues(form)
	unknown.Set("target_id", "9")
	duplicate := cloneValues(form)
	for key := range duplicate {
		if key != "_csrf" {
			duplicate[key] = append(duplicate[key], duplicate[key][0])
			break
		}
	}
	for _, invalid := range []url.Values{unknown, duplicate} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, areaAdministrationTestRequest(http.MethodPost, path, invalid, authentication))
		if response.Code != http.StatusBadRequest || *calls != before {
			t.Fatalf("invalid form = (status %d, calls %d, body %q)", response.Code, *calls, response.Body.String())
		}
	}
}

func TestAdministrationPreflightRejectsBeforeBodyOrDelegation(t *testing.T) {
	t.Parallel()
	calls := 0
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ })
	for _, target := range []string{"/admin/accounts?after=01", "/admin/accounts?after=1&after=2", "/admin/accounts?after=%zz", "/admin/areas/01", "/admin/areas/2/unknown", "/admin/accounts/2/groups/04", "/admin/areas/2/groups/04", "/admin?x=1"} {
		body := &countingAdministrationBody{Reader: bytes.NewBufferString("secret")}
		request := httptest.NewRequest(http.MethodPost, target, body)
		response := httptest.NewRecorder()
		withAdministrationPreflight(next).ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || calls != 0 || body.reads != 0 {
			t.Fatalf("preflight %s = (%d,calls %d,reads %d)", target, response.Code, calls, body.reads)
		}
	}
}

type countingAdministrationBody struct {
	io.Reader
	reads int
}

func (body *countingAdministrationBody) Read(buffer []byte) (int, error) {
	body.reads++
	return body.Reader.Read(buffer)
}
func (*countingAdministrationBody) Close() error { return nil }

func administrationCompletionTestServices() AdministrationHTTPServices {
	observed := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	return AdministrationHTTPServices{
		Dashboard: func(context.Context, auth.AccessContext) (administration.Dashboard, error) {
			return administration.Dashboard{ObservedAt: observed, Users: 3, Members: 1, Moderators: 1, Administrators: 1, ActiveUsers: 2, SuspendedUsers: 1, Topics: 4, Posts: 8, OpenReports: 1, InReviewReports: 2}, nil
		},
		ListAccounts: func(context.Context, auth.AccessContext, int64) (administration.AccountPage, error) {
			return administration.AccountPage{Accounts: []administration.AccountSummary{{ID: 2, DisplayName: "Local Member", Role: policy.RoleMember, Revision: 3}}}, nil
		},
		LoadAccount: func(context.Context, auth.AccessContext, int64) (administration.AccountSummary, error) {
			return administration.AccountSummary{ID: 2, DisplayName: "Local Member", Role: policy.RoleMember, Revision: 3}, nil
		},
		ListAccountGroups: func(context.Context, auth.AccessContext, int64, int64) (administration.AccountGroupPage, error) {
			return administration.AccountGroupPage{Groups: []administration.AccountGroup{{ID: 4, Name: "Members", Member: true}}}, nil
		},
		ListGroups: func(context.Context, auth.AccessContext, int64) (administration.GroupPage, error) {
			return administration.GroupPage{Groups: []administration.GroupSummary{{ID: 4, Name: "Members", Revision: 5}}}, nil
		},
		CreateGroup: func(context.Context, auth.AccessContext, string, string, pgtype.UUID) (administration.GroupMutationResult, error) {
			return administration.GroupMutationResult{GroupID: 4, Revision: 1, AuditID: 1}, nil
		},
		RenameGroup: func(context.Context, auth.AccessContext, int64, string, string, int64, pgtype.UUID) (administration.GroupMutationResult, error) {
			return administration.GroupMutationResult{GroupID: 4, Revision: 6, AuditID: 1}, nil
		},
		ChangeMembership: func(context.Context, auth.AccessContext, int64, int64, bool, string, int64, pgtype.UUID) (administration.AccountMutationResult, error) {
			return administration.AccountMutationResult{UserID: 2, Revision: 4, AuditID: 1}, nil
		},
		ChangeRole: func(context.Context, auth.AccessContext, int64, policy.Role, policy.Role, string, int64, pgtype.UUID) (administration.AccountMutationResult, error) {
			return administration.AccountMutationResult{UserID: 2, Revision: 4, AuditID: 1}, nil
		},
		ListAreas: func(context.Context, auth.AccessContext, int32, int64) (administration.AreaPage, error) {
			return administration.AreaPage{Areas: []administration.AreaSummary{{ID: 3, Slug: "general", Name: "General", Visibility: policy.VisibilityPublic, PostingMode: policy.PostingNormal, Revision: 2}}}, nil
		},
		LoadArea: func(context.Context, auth.AccessContext, int64, int64) (administration.AreaDetail, error) {
			return administration.AreaDetail{Area: administration.AreaSummary{ID: 3, Slug: "general", Name: "General", Visibility: policy.VisibilityGroups, PostingMode: policy.PostingNormal, Revision: 2}, Groups: []administration.AreaGroup{{ID: 4, Name: "Members", Assigned: true}}}, nil
		},
		CreateArea: func(context.Context, auth.AccessContext, administration.AreaCoreInput, pgtype.UUID) (administration.AreaCompletionResult, error) {
			return administration.AreaCompletionResult{AreaID: 3, Revision: 1, AuditID: 1}, nil
		},
		UpdateArea: func(context.Context, auth.AccessContext, int64, administration.AreaCoreInput, pgtype.UUID) (administration.AreaCompletionResult, error) {
			return administration.AreaCompletionResult{AreaID: 3, Revision: 3, AuditID: 1}, nil
		},
		ChangeAreaGroup: func(context.Context, auth.AccessContext, int64, int64, bool, string, int64, pgtype.UUID) (administration.AreaCompletionResult, error) {
			return administration.AreaCompletionResult{AreaID: 3, Revision: 3, AuditID: 1}, nil
		},
	}
}
