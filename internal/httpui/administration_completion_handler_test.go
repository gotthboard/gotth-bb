package httpui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/abuse"
	"github.com/gotthboard/gotth-bb/internal/administration"
	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/control"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/registration"
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
		{path: "/admin/accounts/2", wants: []string{"Local Member", "Change role", "Members", "Group membership", "identity accepted", "/bb/admin/accounts/2/groups/4"}, forbids: []string{`name="group_id"`, "/identity/reconcile"}},
		{path: "/admin/groups", wants: []string{"Groups", "Create group", "Members"}},
		{path: "/admin/areas", wants: []string{"Areas", "Create area", "Existing areas", "General", "/bb/admin/areas/3", `sm:grid-cols-2`, `sm:grid-cols-3`, `w-full min-w-0 rounded-md border border-slate-700`, `focus-visible:outline-2`, `maxlength="4000"`, "Initial access group ID"}, forbids: []string{`class="bg-slate-950"`}},
		{path: "/admin/areas/3", wants: []string{"General", "Area settings", "Update area", "Group access", "Members", "/bb/admin/areas/3/groups/4", `sm:grid-cols-3`, `sm:grid-cols-[minmax(0,1fr)_auto]`, `w-full min-w-0 rounded-md border border-slate-700`, `focus-visible:outline-2`, `type="hidden" name="initial_group_id" value=""`}, forbids: []string{`name="group_id"`, `name="slug"`, `class="bg-slate-950"`}},
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

func TestAdministrationIdentityReconciliationIsExactAndBounded(t *testing.T) {
	t.Parallel()
	services := administrationCompletionTestServices()
	services.LoadAccount = func(context.Context, auth.AccessContext, int64) (administration.AccountSummary, error) {
		return administration.AccountSummary{ID: 2, DisplayName: "Local Member", Role: policy.RoleMember, Suspended: true, Revision: 5, AuthentikSyncState: "grant_required"}, nil
	}
	calls := 0
	services.ReconcileIdentity = func(_ context.Context, actor auth.AccessContext, userID int64, reason string, requestID pgtype.UUID) (registration.IdentityReconciliationResult, error) {
		calls++
		if actor.UserID != 1 || userID != 2 || reason != "Retry restricted identity" || !requestID.Valid {
			t.Fatalf("reconcile args = (%+v, %d, %q, %+v)", actor, userID, reason, requestID)
		}
		return registration.IdentityReconciliationResult{UserID: 2, Revision: 7, AuditID: 11, SyncState: "accepted"}, nil
	}
	handler, err := newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
	if err != nil {
		t.Fatal(err)
	}
	handler = withModerationTestRequestID(t, handler)
	admin := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 1, Role: auth.RoleAdministrator}}
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, areaAdministrationTestRequest(http.MethodGet, "/admin/accounts/2", nil, admin))
	for _, want := range []string{"identity grant_required", "/bb/admin/accounts/2/identity/reconcile", "Retry identity reconciliation"} {
		if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), want) {
			t.Fatalf("account reconciliation page missing %q: (%d, %q)", want, getResponse.Code, getResponse.Body.String())
		}
	}
	form := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "reason": {"Retry restricted identity"}}
	request := areaAdministrationTestRequest(http.MethodPost, "/admin/accounts/2/identity/reconcile", form, admin)
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("HX-Location") != `{"path":"/bb/admin/accounts/2","target":"#main-content","swap":"outerHTML"}` || calls != 1 {
		t.Fatalf("identity reconciliation = (%d, %q, %d, %q)", response.Code, response.Header().Get("HX-Location"), calls, response.Body.String())
	}
	assertAdministrationRejectedForms(t, handler, "/admin/accounts/2/identity/reconcile", form, admin, &calls)
}

func TestRegistrationAdministrationRendersAndDecidesExactPendingRow(t *testing.T) {
	t.Parallel()
	services := administrationCompletionTestServices()
	decisionCalls, adoptionCalls := 0, 0
	handle := strings.Repeat("A", 87)
	services.Registrations = &RegistrationAdministrationHTTPServices{
		List: func(_ context.Context, actor auth.AccessContext, after int64) (registration.PendingPage, error) {
			if actor.UserID != 1 || after != 0 {
				t.Fatalf("list args = (%+v, %d)", actor, after)
			}
			return registration.PendingPage{
				Registrations: []registration.Pending{{ID: 17, Revision: 4, DisplayName: "Pending Member", VerifiedEmail: "pending@example.test", Status: "pending", IntakeAt: time.Date(2026, 9, 9, 19, 0, 0, 0, time.UTC)}},
				Orphans:       []registration.Orphan{{DisplayName: "Recovered Member", VerifiedEmail: "recovered@example.test", Handle: handle}},
			}, nil
		},
		Decide: func(_ context.Context, actor auth.AccessContext, input registration.DecisionInput) (registration.DecisionResult, error) {
			decisionCalls++
			if actor.UserID != 1 || input.RegistrationID != 17 || input.Revision != 4 || input.Decision != registration.Approve || input.Reason != "Verified application" || !input.RequestID.Valid {
				t.Fatalf("decision args = (%+v, %+v)", actor, input)
			}
			return registration.DecisionResult{Status: "approved", Revision: 6, AuditID: 19}, nil
		},
		Adopt: func(_ context.Context, actor auth.AccessContext, input registration.AdoptionInput) (registration.AdoptionResult, error) {
			adoptionCalls++
			if actor.UserID != 1 || input.Handle != handle || input.Reason != "Recover verified intake" || !input.RequestID.Valid {
				t.Fatalf("adoption args = (%+v, %+v)", actor, input)
			}
			return registration.AdoptionResult{RegistrationID: 20, Revision: 1, AuditID: 21, Inserted: true}, nil
		},
	}
	handler, err := newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
	if err != nil {
		t.Fatal(err)
	}
	handler = withModerationTestRequestID(t, handler)
	admin := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 1, Role: auth.RoleAdministrator}}
	request := areaAdministrationTestRequest(http.MethodGet, "/admin/registrations", nil, admin)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	for _, want := range []string{"Pending registrations", "Pending Member", "pending@example.test", "/bb/admin/registrations/17/approve", "/bb/admin/registrations/17/reject", "Registrations", "Recovered Member", "recovered@example.test", "/bb/admin/registrations/" + handle + "/adopt"} {
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), want) {
			t.Fatalf("registration page missing %q: (%d, %q)", want, response.Code, response.Body.String())
		}
	}
	form := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "revision": {"4"}, "reason": {"Verified application"}}
	mutation := areaAdministrationTestRequest(http.MethodPost, "/admin/registrations/17/approve", form, admin)
	mutation.Header.Set("HX-Request", "true")
	mutationResponse := httptest.NewRecorder()
	handler.ServeHTTP(mutationResponse, mutation)
	if mutationResponse.Code != http.StatusNoContent || mutationResponse.Header().Get("HX-Location") != `{"path":"/bb/admin/registrations","target":"#main-content","swap":"outerHTML"}` || decisionCalls != 1 {
		t.Fatalf("registration decision = (%d, %q, %d, %q)", mutationResponse.Code, mutationResponse.Header().Get("HX-Location"), decisionCalls, mutationResponse.Body.String())
	}
	adoptionForm := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "reason": {"Recover verified intake"}}
	adoption := areaAdministrationTestRequest(http.MethodPost, "/admin/registrations/"+handle+"/adopt", adoptionForm, admin)
	adoptionResponse := httptest.NewRecorder()
	handler.ServeHTTP(adoptionResponse, adoption)
	if adoptionResponse.Code != http.StatusSeeOther || adoptionResponse.Header().Get("Location") != "/bb/admin/registrations" || adoptionCalls != 1 {
		t.Fatalf("registration adoption = (%d, %q, %d, %q)", adoptionResponse.Code, adoptionResponse.Header().Get("Location"), adoptionCalls, adoptionResponse.Body.String())
	}
}

func TestInvitationAdministrationCreatesOneTimeLinkAndRevokesHandle(t *testing.T) {
	t.Parallel()
	services := administrationCompletionTestServices()
	now := time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)
	clockNow := now
	handle := strings.Repeat("A", 87)
	listCalls, createCalls, revokeCalls := 0, 0, 0
	invitationCalls := make([]string, 0, 5)
	services.Invitations = &InvitationAdministrationHTTPServices{
		List: func(_ context.Context, actor auth.AccessContext) (registration.InvitationPage, error) {
			listCalls++
			invitationCalls = append(invitationCalls, "list")
			if actor.UserID != 1 {
				t.Fatalf("list actor = %+v", actor)
			}
			return registration.InvitationPage{Invitations: []registration.InvitationSummary{{Name: "gotth-bb-invitation", Status: "active", Delivery: "not_requested", ExpiresAt: now.Add(time.Hour), CreatedAt: now, Revision: 2, Handle: handle}}}, nil
		},
		Create: func(_ context.Context, actor auth.AccessContext, input registration.InvitationInput) (registration.InvitationResult, error) {
			createCalls++
			invitationCalls = append(invitationCalls, "create")
			if actor.UserID != 1 || input.Email != "invitee@example.test" || input.DisplayName != "Invited Member" || input.Reason != "Invite participant" || input.ExpiresAt != now.Add(time.Hour) || input.Deliver || !input.RequestID.Valid {
				t.Fatalf("create args = (%+v, %+v)", actor, input)
			}
			return registration.InvitationResult{Status: "active", Delivery: "not_requested", TokenUUID: "66666666-6666-4666-8666-666666666666", Revision: 2, AuditID: 3, Completed: true}, nil
		},
		Revoke: func(_ context.Context, actor auth.AccessContext, input registration.InvitationRevocationInput) (registration.InvitationRevocationResult, error) {
			revokeCalls++
			if actor.UserID != 1 || input.Handle != handle || input.Reason != "Withdraw invitation" || !input.RequestID.Valid {
				t.Fatalf("revoke args = (%+v, %+v)", actor, input)
			}
			return registration.InvitationRevocationResult{Status: "revoked", Result: "confirmed", Revision: 4, AuditID: 5, Completed: true}, nil
		},
		Clock: func() time.Time { return clockNow }, Issuer: url.URL{Scheme: "https", Host: "auth.example.test"}, FlowSlug: "gotth-bb-invitation", SMTPConfigured: true,
	}
	handler, err := newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
	if err != nil {
		t.Fatal(err)
	}
	handler = withModerationTestRequestID(t, handler)
	admin := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 1, Role: auth.RoleAdministrator}}
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, areaAdministrationTestRequest(http.MethodGet, "/admin/invitations", nil, admin))
	for _, want := range []string{"Invitations", "gotth-bb-invitation", "/bb/admin/invitations/" + handle + "/revoke", `name="idempotency_key"`, `name="expires_reference" value="2026-09-09T20:00:00Z"`} {
		if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), want) {
			t.Fatalf("invitation page missing %q: (%d, %q)", want, getResponse.Code, getResponse.Body.String())
		}
	}
	clockNow = now.Add(5 * time.Minute)
	createForm := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "email": {"invitee@example.test"}, "display_name": {"Invited Member"}, "expires_minutes": {"60"}, "expires_reference": {now.Format(time.RFC3339)}, "delivery": {"none"}, "reason": {"Invite participant"}, "idempotency_key": {strings.Repeat("11", 16)}}
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, areaAdministrationTestRequest(http.MethodPost, "/admin/invitations", createForm, admin))
	wantLink := "https://auth.example.test/if/flow/gotth-bb-invitation/?itoken=66666666-6666-4666-8666-666666666666"
	if createResponse.Code != http.StatusOK || !strings.Contains(createResponse.Body.String(), wantLink) || createCalls != 1 || listCalls != 2 || strings.Join(invitationCalls, ",") != "list,list,create" {
		t.Fatalf("create invitation = (%d, create %d, list %d, calls %v, %q)", createResponse.Code, createCalls, listCalls, invitationCalls, createResponse.Body.String())
	}
	clockNow = now.Add(10 * time.Minute)
	retryResponse := httptest.NewRecorder()
	handler.ServeHTTP(retryResponse, areaAdministrationTestRequest(http.MethodPost, "/admin/invitations", createForm, admin))
	if retryResponse.Code != http.StatusOK || createCalls != 2 || listCalls != 3 || strings.Join(invitationCalls, ",") != "list,list,create,list,create" {
		t.Fatalf("retried invitation form = (%d, create %d, list %d, calls %v, %q)", retryResponse.Code, createCalls, listCalls, invitationCalls, retryResponse.Body.String())
	}
	revokeForm := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "reason": {"Withdraw invitation"}}
	revokeRequest := areaAdministrationTestRequest(http.MethodPost, "/admin/invitations/"+handle+"/revoke", revokeForm, admin)
	revokeRequest.Header.Set("HX-Request", "true")
	revokeResponse := httptest.NewRecorder()
	handler.ServeHTTP(revokeResponse, revokeRequest)
	if revokeResponse.Code != http.StatusNoContent || revokeResponse.Header().Get("HX-Location") != `{"path":"/bb/admin/invitations","target":"#main-content","swap":"outerHTML"}` || revokeCalls != 1 {
		t.Fatalf("revoke invitation = (%d, %q, %d, %q)", revokeResponse.Code, revokeResponse.Header().Get("HX-Location"), revokeCalls, revokeResponse.Body.String())
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
	services.ReconcileIdentity = func(context.Context, auth.AccessContext, int64, string, pgtype.UUID) (registration.IdentityReconciliationResult, error) {
		panic("identity reconciliation called")
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
		contentType    string
		contentLength  int64
		wantStatus     int
	}{
		{name: "missing session", path: "/admin/accounts/2/role", wantStatus: http.StatusSeeOther},
		{name: "stale session", path: "/admin/accounts/2/role", authentication: stale, wantStatus: http.StatusSeeOther},
		{name: "member", path: "/admin/accounts/2/role", authentication: member, wantStatus: http.StatusForbidden},
		{name: "invalid header csrf", path: "/admin/accounts/2/role", authentication: admin, header: validCSRFTokenForTest(0x52), wantStatus: http.StatusForbidden},
		{name: "parameterized content type", path: "/admin/accounts/2/role", authentication: admin, header: token, contentType: "application/x-www-form-urlencoded; charset=utf-8", wantStatus: http.StatusBadRequest},
		{name: "small form declared oversized", path: "/admin/accounts/2/role", authentication: admin, header: token, contentLength: maximumAdministrationSmallFormBytes + 1, wantStatus: http.StatusBadRequest},
		{name: "identity missing session", path: "/admin/accounts/2/identity/reconcile", wantStatus: http.StatusSeeOther},
		{name: "identity invalid header csrf", path: "/admin/accounts/2/identity/reconcile", authentication: admin, header: validCSRFTokenForTest(0x52), wantStatus: http.StatusForbidden},
		{name: "area form declared oversized", path: "/admin/areas", authentication: admin, header: token, contentLength: maximumAdministrationAreaFormBytes + 1, wantStatus: http.StatusBadRequest},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			body := &countingAdministrationBody{Reader: strings.NewReader("secret=body")}
			request := httptest.NewRequest(http.MethodPost, test.path, body)
			contentType := test.contentType
			if contentType == "" {
				contentType = "application/x-www-form-urlencoded"
			}
			request.Header.Set("Content-Type", contentType)
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

func TestAdministrationCompletionAcceptsExactParserWireLimits(t *testing.T) {
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

func TestAdministrationCompletionWorstCaseLegalFormsFitWireLimits(t *testing.T) {
	t.Parallel()
	admin := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 1, Role: auth.RoleAdministrator}}
	token := validCSRFTokenForTest(0x51)
	worstName := strings.Repeat("💩", 120)
	worstGroupName := strings.Repeat("💩", 80)
	worstDescription := strings.Repeat("💩", 4000)
	worstReason := strings.Repeat("💩", 500)

	t.Run("small form", func(t *testing.T) {
		services := administrationCompletionTestServices()
		calls := 0
		services.RenameGroup = func(_ context.Context, actor auth.AccessContext, groupID int64, name, reason string, revision int64, requestID pgtype.UUID) (administration.GroupMutationResult, error) {
			calls++
			if actor.UserID != 1 || groupID != 9223372036854775807 || name != worstGroupName || reason != worstReason || revision != 9223372036854775806 || !requestID.Valid {
				t.Fatalf("worst small form args = (%+v,%d,%q,%q,%d,%+v)", actor, groupID, name, reason, revision, requestID)
			}
			return administration.GroupMutationResult{GroupID: groupID, Revision: revision + 1, AuditID: 1}, nil
		}
		handler, err := newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
		if err != nil {
			t.Fatal(err)
		}
		handler = withModerationTestRequestID(t, handler)
		form := url.Values{"_csrf": {token}, "name": {worstGroupName}, "reason": {worstReason}, "revision": {"9223372036854775806"}}
		if length := len(form.Encode()); length > maximumAdministrationSmallFormBytes {
			t.Fatalf("worst legal small form length = %d, limit %d", length, maximumAdministrationSmallFormBytes)
		}
		assertAdministrationFormResult(t, handler, "/admin/groups/9223372036854775807", form, admin, "/bb/admin/groups", &calls)
	})

	t.Run("area form", func(t *testing.T) {
		services := administrationCompletionTestServices()
		calls := 0
		services.CreateArea = func(_ context.Context, actor auth.AccessContext, input administration.AreaCoreInput, requestID pgtype.UUID) (administration.AreaCompletionResult, error) {
			calls++
			if actor.UserID != 1 || input.Slug != strings.Repeat("a", 80) || input.Name != worstName || input.Description != worstDescription || input.DisplayOrder != 2147483647 || input.Visibility != policy.VisibilityGroups || input.PostingMode != policy.PostingReadOnly || input.InitialGroupID != 9223372036854775807 || input.Reason != worstReason || input.Revision != 0 || !requestID.Valid {
				t.Fatalf("worst area form args = (%+v,%+v,%+v)", actor, input, requestID)
			}
			return administration.AreaCompletionResult{AreaID: 9, Slug: input.Slug, Revision: 1, AuditID: 1}, nil
		}
		handler, err := newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
		if err != nil {
			t.Fatal(err)
		}
		handler = withModerationTestRequestID(t, handler)
		form := url.Values{"_csrf": {token}, "slug": {strings.Repeat("a", 80)}, "name": {worstName}, "description": {worstDescription}, "display_order": {"2147483647"}, "visibility": {"groups"}, "posting_mode": {"read_only"}, "initial_group_id": {"9223372036854775807"}, "reason": {worstReason}}
		if length := len(form.Encode()); length > maximumAdministrationAreaFormBytes {
			t.Fatalf("worst legal area form length = %d, limit %d", length, maximumAdministrationAreaFormBytes)
		}
		assertAdministrationFormResult(t, handler, "/admin/areas", form, admin, "/bb/admin/areas/9", &calls)
	})
}

func encodedAdministrationFormAtLimit(t *testing.T, form url.Values, field string, limit int64) string {
	t.Helper()
	values := cloneValues(form)
	values.Set(field, "")
	remaining := int(limit) - len(values.Encode())
	if remaining < 0 {
		t.Fatalf("base form exceeds limit: %d", remaining)
	}
	values.Set(field, strings.Repeat("💩", remaining/12)+strings.Repeat("a", remaining%12))
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
	for _, target := range []string{"/admin/accounts?after=01", "/admin/accounts?after=1&after=2", "/admin/accounts?after=%zz", "/admin/areas/01", "/admin/areas/2/unknown", "/admin/accounts/2/groups/04", "/admin/areas/2/groups/04", "/admin/accounts/2/identity/unknown", "/admin/accounts/02/identity/reconcile", "/admin/accounts/02/sessions", "/admin/accounts/2/sessions?probe=1", "/admin/accounts/2/sessions/unknown", "/admin/sessions/not-a-handle/revoke", "/admin/email?probe=1", "/admin/control?probe=1", "/admin?x=1"} {
		body := &countingAdministrationBody{Reader: bytes.NewBufferString("secret")}
		request := httptest.NewRequest(http.MethodPost, target, body)
		response := httptest.NewRecorder()
		withAdministrationPreflight(next).ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || calls != 0 || body.reads != 0 {
			t.Fatalf("preflight %s = (%d,calls %d,reads %d)", target, response.Code, calls, body.reads)
		}
	}
}

func TestAdministrationControlSessionAndEmailGETRoutes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 12, 19, 0, 0, 0, time.UTC)
	services := administrationControlSessionEmailTestServices(t, now)
	handler, err := newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
	if err != nil {
		t.Fatal(err)
	}
	admin := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 1, Role: auth.RoleAdministrator}}
	for _, test := range []struct {
		path      string
		wants     []string
		forbidden []string
	}{
		{path: "/admin/control", wants: []string{"Control settings", `name="registration_mode"`, `name="session_idle_seconds"`, `name="revision" value="4"`, "/bb/admin/email"}},
		{path: "/admin/accounts/2/sessions", wants: []string{"Local sessions for Local Member", "2026-09-12T18:00:00Z", "More active sessions exist", "/bb/admin/sessions/", "/bb/admin/accounts/2/sessions/revoke"}, forbidden: []string{"token_hash", "user_agent", "ip_address", "192.0.2.1"}},
		{path: "/admin/email", wants: []string{"Shared SMTP transport is configured", "accepted", "2026-09-12T19:00:00Z", "Send test to my verified address", "/bb/admin/email/test"}, forbidden: []string{"administrator@example.test", "SMTP password"}},
	} {
		test := test
		t.Run(test.path, func(t *testing.T) {
			request := administrationPrivilegedTestRequest(http.MethodGet, test.path, nil, admin)
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
			for _, forbidden := range test.forbidden {
				if strings.Contains(response.Body.String(), forbidden) {
					t.Fatalf("GET %s leaked %q: %q", test.path, forbidden, response.Body.String())
				}
			}
		})
	}
}

func TestAdministrationControlSessionAndEmailMutations(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 12, 19, 0, 0, 0, time.UTC)
	services := administrationControlSessionEmailTestServices(t, now)
	controlCalls, oneCalls, allCalls, emailCalls := 0, 0, 0, 0
	services.Control.Update = func(_ context.Context, actor auth.AccessContext, input control.Input, requestID pgtype.UUID) (control.MutationResult, error) {
		controlCalls++
		if actor.UserID != 1 || input.Registration != control.RegistrationAdministratorApproval || !input.MaintenanceEnabled || input.MaintenanceMessage != "Brief maintenance" || input.PublishLimit != 8 || input.NewAccountLimit != 3 || input.PublishWindow != time.Minute || input.NewAccountPeriod != 24*time.Hour || input.SessionIdle != 30*time.Minute || input.AuthRevalidate != 15*time.Minute || input.Revision != 4 || input.Reason != "Apply bounded policy" || !requestID.Valid {
			t.Fatalf("control input = (%+v, %+v, %+v)", actor, input, requestID)
		}
		return control.MutationResult{Revision: 5, AuditID: 10}, nil
	}
	services.Sessions.RevokeOne = func(_ context.Context, actor auth.AccessContext, target, session int64, reason string, requestID pgtype.UUID) (administration.SessionMutationResult, error) {
		oneCalls++
		if actor.UserID != 1 || target != 2 || session != 13 || reason != "Revoke compromised session" || !requestID.Valid {
			t.Fatalf("revoke one input = (%+v, %d, %d, %q, %+v)", actor, target, session, reason, requestID)
		}
		return administration.SessionMutationResult{TargetUserID: target, SessionID: session, Revoked: 1, AuditID: 11}, nil
	}
	services.Sessions.RevokeAll = func(_ context.Context, actor auth.AccessContext, target, revision int64, reason string, requestID pgtype.UUID) (administration.SessionMutationResult, error) {
		allCalls++
		if actor.UserID != 1 || target != 1 || revision != 4 || reason != "Revoke my local sessions" || !requestID.Valid {
			t.Fatalf("revoke all input = (%+v, %d, %d, %q, %+v)", actor, target, revision, reason, requestID)
		}
		return administration.SessionMutationResult{TargetUserID: target, Revoked: 2, AuditID: 12}, nil
	}
	services.Email.Test = func(_ context.Context, actor auth.AccessContext, reason string, requestID pgtype.UUID) (administration.EmailTestResult, error) {
		emailCalls++
		if actor.UserID != 1 || reason != "Verify delivery" || !requestID.Valid {
			t.Fatalf("email input = (%+v, %q, %+v)", actor, reason, requestID)
		}
		return administration.EmailTestResult{Status: "accepted", RequestedAt: now, CompletedAt: now.Add(time.Second), AuditID: 13}, nil
	}
	handler, err := newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
	if err != nil {
		t.Fatal(err)
	}
	handler = withModerationTestRequestID(t, handler)
	admin := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 1, Role: auth.RoleAdministrator}}

	controlForm := url.Values{
		"_csrf": {validCSRFTokenForTest(0x51)}, "registration_mode": {"administrator_approval"},
		"maintenance_enabled": {"enabled"}, "maintenance_message": {"Brief maintenance"},
		"publish_rate_limit": {"8"}, "new_account_publish_rate_limit": {"3"},
		"publish_window_seconds": {"60"}, "new_account_period_seconds": {"86400"},
		"session_idle_seconds": {"1800"}, "auth_revalidate_seconds": {"900"},
		"revision": {"4"}, "reason": {"Apply bounded policy"},
	}
	controlRequest := administrationPrivilegedTestRequest(http.MethodPost, "/admin/control", controlForm, admin)
	controlRequest.Header.Set("HX-Request", "true")
	controlResponse := httptest.NewRecorder()
	handler.ServeHTTP(controlResponse, controlRequest)
	if controlResponse.Code != http.StatusNoContent || controlResponse.Header().Get("HX-Location") != `{"path":"https://forum.example/bb/admin/control","target":"#main-content","swap":"outerHTML"}` || controlCalls != 1 {
		t.Fatalf("control response = (%d, %q, calls %d, %q)", controlResponse.Code, controlResponse.Header().Get("HX-Location"), controlCalls, controlResponse.Body.String())
	}
	services.Control.Update = func(context.Context, auth.AccessContext, control.Input, pgtype.UUID) (control.MutationResult, error) {
		return control.MutationResult{Revision: 4}, nil
	}
	invalidControlResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidControlResponse, administrationPrivilegedTestRequest(http.MethodPost, "/admin/control", controlForm, admin))
	if invalidControlResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid control result response = (%d, %q)", invalidControlResponse.Code, invalidControlResponse.Body.String())
	}

	handle, err := issueAdministrationSessionHandle(now, [32]byte{0x41}, 1, 2, 13)
	if err != nil {
		t.Fatal(err)
	}
	oneResponse := httptest.NewRecorder()
	handler.ServeHTTP(oneResponse, administrationPrivilegedTestRequest(http.MethodPost, "/admin/sessions/"+handle+"/revoke", url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "reason": {"Revoke compromised session"}}, admin))
	if oneResponse.Code != http.StatusSeeOther || oneResponse.Header().Get("Location") != "/bb/admin/accounts/2/sessions" || oneCalls != 1 {
		t.Fatalf("revoke one response = (%d, %q, calls %d, %q)", oneResponse.Code, oneResponse.Header().Get("Location"), oneCalls, oneResponse.Body.String())
	}

	allResponse := httptest.NewRecorder()
	handler.ServeHTTP(allResponse, administrationPrivilegedTestRequest(http.MethodPost, "/admin/accounts/1/sessions/revoke", url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "revision": {"4"}, "reason": {"Revoke my local sessions"}}, admin))
	cookies := allResponse.Result().Cookies()
	if allResponse.Code != http.StatusSeeOther || allResponse.Header().Get("Location") != "/bb/login?return=%2Fbb%2Fadmin" || allCalls != 1 || len(cookies) != 1 || cookies[0].Name != "gotth_bb_session" || cookies[0].MaxAge != -1 || cookies[0].Path != "/bb/" {
		t.Fatalf("revoke all response = (%d, %q, calls %d, cookies %+v)", allResponse.Code, allResponse.Header().Get("Location"), allCalls, cookies)
	}

	emailRequest := administrationPrivilegedTestRequest(http.MethodPost, "/admin/email/test", url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "reason": {"Verify delivery"}}, admin)
	emailRequest.Header.Set("HX-Request", "true")
	emailResponse := httptest.NewRecorder()
	handler.ServeHTTP(emailResponse, emailRequest)
	if emailResponse.Code != http.StatusNoContent || emailResponse.Header().Get("HX-Location") != `{"path":"https://forum.example/bb/admin/email","target":"#main-content","swap":"outerHTML"}` || emailCalls != 1 {
		t.Fatalf("email response = (%d, %q, calls %d, %q)", emailResponse.Code, emailResponse.Header().Get("HX-Location"), emailCalls, emailResponse.Body.String())
	}
}

func TestAdministrationDisabledAndRateLimitedEmailRemainCSRFProtected(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 12, 19, 0, 0, 0, time.UTC)
	services := administrationControlSessionEmailTestServices(t, now)
	calls := 0
	services.Email.Configured = false
	services.Email.Test = func(context.Context, auth.AccessContext, string, pgtype.UUID) (administration.EmailTestResult, error) {
		calls++
		return administration.EmailTestResult{}, errors.New("must not run")
	}
	handler, err := newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
	if err != nil {
		t.Fatal(err)
	}
	handler = withModerationTestRequestID(t, handler)
	admin := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 1, Role: auth.RoleAdministrator}}
	missingCSRF := administrationPrivilegedTestRequest(http.MethodPost, "/admin/email/test", url.Values{"reason": {"Hidden"}}, admin)
	missingResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingResponse, missingCSRF)
	if missingResponse.Code != http.StatusForbidden || calls != 0 {
		t.Fatalf("disabled missing CSRF = (%d, calls %d)", missingResponse.Code, calls)
	}
	disabledResponse := httptest.NewRecorder()
	handler.ServeHTTP(disabledResponse, administrationPrivilegedTestRequest(http.MethodPost, "/admin/email/test", url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "reason": {"Check disabled"}}, admin))
	if disabledResponse.Code != http.StatusConflict || calls != 0 {
		t.Fatalf("disabled valid request = (%d, calls %d)", disabledResponse.Code, calls)
	}

	services.Email.Configured = true
	services.Email.Test = func(context.Context, auth.AccessContext, string, pgtype.UUID) (administration.EmailTestResult, error) {
		calls++
		return administration.EmailTestResult{}, administration.ErrEmailTestRateLimited
	}
	handler, err = newAdministrationCompletionHandler(callbackTestURLBuilder(t), services)
	if err != nil {
		t.Fatal(err)
	}
	handler = withModerationTestRequestID(t, handler)
	rateResponse := httptest.NewRecorder()
	handler.ServeHTTP(rateResponse, administrationPrivilegedTestRequest(http.MethodPost, "/admin/email/test", url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "reason": {"Too soon"}}, admin))
	if rateResponse.Code != http.StatusTooManyRequests || rateResponse.Header().Get("Retry-After") != "300" || calls != 1 {
		t.Fatalf("rate response = (%d, retry %q, calls %d)", rateResponse.Code, rateResponse.Header().Get("Retry-After"), calls)
	}
}

func TestParseControlInputRequiresCanonicalClosedValues(t *testing.T) {
	t.Parallel()
	valid := url.Values{
		"registration_mode": {"closed"}, "maintenance_enabled": {"disabled"}, "maintenance_message": {""},
		"publish_rate_limit": {"8"}, "new_account_publish_rate_limit": {"3"},
		"publish_window_seconds": {"60"}, "new_account_period_seconds": {"86400"},
		"session_idle_seconds": {"1800"}, "auth_revalidate_seconds": {"900"},
		"revision": {"4"}, "reason": {"Canonical control form"},
	}
	input, err := parseControlInput(valid)
	if err != nil || input.Registration != control.RegistrationClosed || input.MaintenanceEnabled || input.Revision != 4 {
		t.Fatalf("valid control input = (%+v, %v)", input, err)
	}
	for _, field := range []string{"publish_rate_limit", "new_account_publish_rate_limit", "publish_window_seconds", "new_account_period_seconds", "session_idle_seconds", "auth_revalidate_seconds"} {
		field := field
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			malformed := cloneValues(valid)
			malformed.Set(field, "0")
			if _, err := parseControlInput(malformed); err == nil {
				t.Fatalf("zero %s accepted", field)
			}
		})
	}
	for _, test := range []struct{ name, field, value string }{
		{name: "noncanonical number", field: "publish_rate_limit", value: "08"},
		{name: "overflow", field: "publish_rate_limit", value: "2147483648"},
		{name: "revision", field: "revision", value: "04"},
		{name: "registration", field: "registration_mode", value: "open"},
		{name: "maintenance", field: "maintenance_enabled", value: "false"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			malformed := cloneValues(valid)
			malformed.Set(test.field, test.value)
			if _, err := parseControlInput(malformed); err == nil {
				t.Fatalf("malformed %s=%q accepted", test.field, test.value)
			}
		})
	}
}

func administrationControlSessionEmailTestServices(t *testing.T, now time.Time) AdministrationHTTPServices {
	t.Helper()
	services := administrationCompletionTestServices()
	publication, err := abuse.NewPublicationPolicy(8, 3, time.Minute, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	services.Control = &ControlAdministrationHTTPServices{
		Load: func(context.Context, auth.AccessContext) (control.EditableSettings, error) {
			return control.EditableSettings{Settings: control.Settings{Registration: control.RegistrationAdministratorApproval, MaintenanceEnabled: true, MaintenanceMessage: "Brief maintenance", Publication: publication, SessionIdle: 30 * time.Minute, AuthRevalidate: 15 * time.Minute, Revision: 4}}, nil
		},
		Update: func(context.Context, auth.AccessContext, control.Input, pgtype.UUID) (control.MutationResult, error) {
			return control.MutationResult{Revision: 5, AuditID: 1}, nil
		},
	}
	services.Sessions = &SessionAdministrationHTTPServices{
		List: func(context.Context, auth.AccessContext, int64) (administration.SessionPage, error) {
			return administration.SessionPage{DisplayName: "Local Member", Revision: 4, More: true, Sessions: []administration.SessionSummary{{ID: 13, IssuedAt: now.Add(-time.Hour), LastSeenAt: now.Add(-time.Minute), ValidatedAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(time.Hour)}}}, nil
		},
		RevokeOne: func(context.Context, auth.AccessContext, int64, int64, string, pgtype.UUID) (administration.SessionMutationResult, error) {
			return administration.SessionMutationResult{TargetUserID: 2, SessionID: 13, Revoked: 1, AuditID: 1}, nil
		},
		RevokeAll: func(context.Context, auth.AccessContext, int64, int64, string, pgtype.UUID) (administration.SessionMutationResult, error) {
			return administration.SessionMutationResult{TargetUserID: 2, Revoked: 1, AuditID: 1}, nil
		},
		Clock: func() time.Time { return now }, CookieName: "gotth_bb_session", Secure: true,
	}
	services.Email = &EmailAdministrationHTTPServices{
		Load: func(context.Context, auth.AccessContext) (administration.EmailTestState, error) {
			return administration.EmailTestState{Status: "accepted", RequestedAt: now, CompletedAt: now.Add(time.Second), NextAllowedAt: now.Add(5 * time.Minute)}, nil
		},
		Test: func(context.Context, auth.AccessContext, string, pgtype.UUID) (administration.EmailTestResult, error) {
			return administration.EmailTestResult{Status: "accepted", RequestedAt: now, CompletedAt: now.Add(time.Second), AuditID: 1}, nil
		}, Configured: true,
	}
	return services
}

func administrationPrivilegedTestRequest(method, target string, form url.Values, authentication auth.SessionAuthentication) *http.Request {
	request := areaAdministrationTestRequest(method, target, form, authentication)
	return request.WithContext(context.WithValue(request.Context(), administrationSessionActionKeyContextKey{}, [32]byte{0x41}))
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
			return administration.AccountSummary{ID: 2, DisplayName: "Local Member", Role: policy.RoleMember, Revision: 3, AuthentikSyncState: "accepted"}, nil
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
		ReconcileIdentity: func(context.Context, auth.AccessContext, int64, string, pgtype.UUID) (registration.IdentityReconciliationResult, error) {
			return registration.IdentityReconciliationResult{UserID: 2, Revision: 4, AuditID: 1, SyncState: "accepted"}, nil
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
