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
