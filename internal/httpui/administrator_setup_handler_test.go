package httpui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/governance"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestRegistrationRedirectUsesConfiguredFlowAndFixedLocalLogin(t *testing.T) {
	t.Parallel()
	handler, err := newRegistrationRedirectHandler(callbackTestURLBuilder(t), url.URL{
		Scheme: "https", Host: "auth.example", Path: "/if/flow/gotth-bb-enrollment/",
	})
	if err != nil {
		t.Fatalf("newRegistrationRedirectHandler() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/bb/register", nil))
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "https://auth.example/if/flow/gotth-bb-enrollment/?next=https%3A%2F%2Fforum.example%2Fbb%2Flogin" {
		t.Fatalf("registration response = (%d, %q)", response.Code, response.Header().Get("Location"))
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("registration Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
	for _, test := range []struct {
		method string
		target string
		status int
	}{
		{method: http.MethodPost, target: "/bb/register", status: http.StatusMethodNotAllowed},
		{method: http.MethodGet, target: "/bb/register?next=https%3A%2F%2Fevil.example", status: http.StatusNotFound},
	} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.target, nil))
		if response.Code != test.status || response.Header().Get("Location") != "" {
			t.Fatalf("%s %s = (%d, %q)", test.method, test.target, response.Code, response.Header().Get("Location"))
		}
	}
}

func TestAdministratorSetupGETRequiresOpenFreshExactIdentity(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name           string
		authentication auth.SessionAuthentication
		status         governance.InitialAdministratorSetupStatus
		wantStatus     int
		wantLocation   string
		wantText       string
	}{
		{name: "closed", status: governance.InitialAdministratorSetupStatus{}, wantStatus: http.StatusNotFound, wantText: "Administrator setup is closed"},
		{name: "anonymous", status: governance.InitialAdministratorSetupStatus{Open: true}, wantStatus: http.StatusSeeOther, wantLocation: "/bb/login?return=%2Fbb%2Fsetup"},
		{name: "stale", authentication: setupAuthentication(true), status: governance.InitialAdministratorSetupStatus{Open: true, Eligible: true}, wantStatus: http.StatusSeeOther, wantLocation: "/bb/auth/revalidate?return=%2Fbb%2Fsetup"},
		{name: "wrong identity", authentication: setupAuthentication(false), status: governance.InitialAdministratorSetupStatus{Open: true}, wantStatus: http.StatusForbidden, wantText: "not authorized"},
		{name: "eligible", authentication: setupAuthentication(false), status: governance.InitialAdministratorSetupStatus{Open: true, Eligible: true}, wantStatus: http.StatusOK, wantText: "Claim administrator role"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			loads := 0
			handler, err := newAdministratorSetupHandler(callbackTestURLBuilder(t), func(_ context.Context, got auth.SessionAuthentication) (governance.InitialAdministratorSetupStatus, error) {
				loads++
				if !reflect.DeepEqual(got, test.authentication) {
					t.Fatalf("authentication = %+v, want %+v", got, test.authentication)
				}
				return test.status, nil
			}, func(context.Context, auth.SessionAuthentication, pgtype.UUID) (governance.InitialAdministratorClaimResult, error) {
				panic("claim must not run")
			}, "gotth_bb_session", true)
			if err != nil {
				t.Fatalf("newAdministratorSetupHandler() returned error: %v", err)
			}
			request := setupRequest(http.MethodGet, "/setup", nil, test.authentication)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || response.Header().Get("Location") != test.wantLocation || loads != 1 {
				t.Fatalf("response = (%d, %q, loads %d)", response.Code, response.Header().Get("Location"), loads)
			}
			if test.wantText != "" && !strings.Contains(response.Body.String(), test.wantText) {
				t.Fatalf("response lacks %q: %q", test.wantText, response.Body.String())
			}
		})
	}
}

func TestAdministratorSetupPOSTClaimsOnceExpiresSessionAndRequiresFreshLogin(t *testing.T) {
	t.Parallel()
	authentication := setupAuthentication(false)
	csrf := validCSRFTokenForTest(0x51)
	claimCalls := 0
	handler, err := newAdministratorSetupHandler(callbackTestURLBuilder(t), func(context.Context, auth.SessionAuthentication) (governance.InitialAdministratorSetupStatus, error) {
		panic("GET loader must not run")
	}, func(_ context.Context, got auth.SessionAuthentication, requestID pgtype.UUID) (governance.InitialAdministratorClaimResult, error) {
		claimCalls++
		if !reflect.DeepEqual(got, authentication) || !requestID.Valid || requestID.Bytes == ([16]byte{}) {
			t.Fatalf("claim authority = (%+v, %+v)", got, requestID)
		}
		return governance.InitialAdministratorClaimResult{UserID: 41, AuditID: 73, RevokedSessionID: 91}, nil
	}, "gotth_bb_session", true)
	if err != nil {
		t.Fatalf("newAdministratorSetupHandler() returned error: %v", err)
	}
	handler = withModerationTestRequestID(t, handler)
	form := url.Values{"_csrf": {csrf}}
	request := setupRequest(http.MethodPost, "/setup/administrator", strings.NewReader(form.Encode()), authentication)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/bb/login?return=%2Fbb%2F" || claimCalls != 1 {
		t.Fatalf("response = (%d, %q, claims %d): %q", response.Code, response.Header().Get("Location"), claimCalls, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "gotth_bb_session" || cookies[0].MaxAge != -1 || !cookies[0].Secure || cookies[0].Path != "/bb/" {
		t.Fatalf("expired cookie = %+v", cookies)
	}
}

func setupAuthentication(stale bool) auth.SessionAuthentication {
	return auth.SessionAuthentication{
		SessionID:            91,
		Access:               auth.AccessContext{Authenticated: true, UserID: 41, Role: auth.RoleMember},
		RequiresRevalidation: stale,
	}
}

func setupRequest(method, target string, body *strings.Reader, authentication auth.SessionAuthentication) *http.Request {
	var request *http.Request
	if body == nil {
		request = httptest.NewRequest(method, target, nil)
	} else {
		request = httptest.NewRequest(method, target, body)
	}
	ctx := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, authentication)
	ctx = context.WithValue(ctx, csrfTokenContextKey{}, validCSRFTokenForTest(0x51))
	return request.WithContext(ctx)
}
