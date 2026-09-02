package httpui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/moderation"
	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestUserModerationHandlerRendersExactActiveAndSuspendedStatus(t *testing.T) {
	t.Parallel()

	actor := auth.AccessContext{Authenticated: true, UserID: 11, Role: auth.RoleModerator}
	base := moderationUserTestStatus()
	for _, test := range []struct {
		name       string
		status     store.ModerationUserStatus
		htmx       bool
		wantAction string
		wantLabel  string
	}{
		{name: "active", status: base, wantAction: `/bb/moderation/users/41/suspend`, wantLabel: "Suspend account"},
		{name: "suspended HTMX", status: suspendedModerationUserTestStatus(), htmx: true, wantAction: `/bb/moderation/users/41/reinstate`, wantLabel: "Reinstate account"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			loads := 0
			handler, err := newUserModerationHandler(callbackTestURLBuilder(t), func(ctx context.Context, access auth.AccessContext, userID int64) (store.ModerationUserStatus, error) {
				loads++
				if ctx == nil || !reflect.DeepEqual(access, actor) || userID != 41 {
					t.Fatalf("status load = (%v, %+v, %d)", ctx, access, userID)
				}
				return test.status, nil
			}, func(context.Context, auth.AccessContext, int64, bool, string, pgtype.UUID) (moderation.UserSuspensionResult, error) {
				panic("mutation called by status GET")
			})
			if err != nil {
				t.Fatalf("newUserModerationHandler() returned error: %v", err)
			}
			request := userModerationGetRequest("/moderation/users/41", auth.SessionAuthentication{SessionID: 7, Access: actor}, validCSRFTokenForTest(0x51))
			if test.htmx {
				request.Header.Set("HX-Request", "true")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body := response.Body.String()
			if response.Code != http.StatusOK || loads != 1 || !strings.Contains(response.Header().Get("Cache-Control"), "no-store") ||
				!strings.Contains(body, test.wantAction) || !strings.Contains(body, test.wantLabel) ||
				!strings.Contains(body, "Local member") || !strings.Contains(body, "Member") || !strings.Contains(body, "Sep 2, 2026 08:00 UTC") ||
				strings.Contains(body, "member@example") || strings.Count(body, `name="reason"`) != 1 {
				t.Fatalf("status response = (%d, loads %d, body %q)", response.Code, loads, body)
			}
			if test.status.Suspended {
				if !strings.Contains(body, "Repeated abuse") || !strings.Contains(body, "Suspended") {
					t.Fatalf("suspended status missing exact state: %s", body)
				}
			} else if strings.Contains(body, "Repeated abuse") || !strings.Contains(body, "Active") {
				t.Fatalf("active status contains stale suspension state: %s", body)
			}
			if test.htmx && strings.Contains(body, "<!doctype html>") {
				t.Fatalf("HTMX status rendered a full document: %s", body)
			}
		})
	}
}

func TestUserModerationHandlerChangesExactStateAndRedirects(t *testing.T) {
	t.Parallel()

	actor := auth.AccessContext{Authenticated: true, UserID: 11, Role: auth.RoleModerator}
	wantUUID := pgtype.UUID{Bytes: [16]byte{0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51}, Valid: true}
	for _, test := range []struct {
		name, action  string
		suspend, htmx bool
		wantStatus    int
	}{
		{name: "suspend", action: "suspend", suspend: true, wantStatus: http.StatusSeeOther},
		{name: "reinstate HTMX", action: "reinstate", htmx: true, wantStatus: http.StatusNoContent},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			handler, err := newUserModerationHandler(callbackTestURLBuilder(t), func(context.Context, auth.AccessContext, int64) (store.ModerationUserStatus, error) {
				panic("status loader called by mutation")
			}, func(ctx context.Context, access auth.AccessContext, userID int64, suspend bool, reason string, requestID pgtype.UUID) (moderation.UserSuspensionResult, error) {
				calls++
				if ctx == nil || !reflect.DeepEqual(access, actor) || userID != 41 || suspend != test.suspend || reason != "Clear reason" || requestID != wantUUID {
					t.Fatalf("change call = (%v, %+v, %d, %t, %q, %+v)", ctx, access, userID, suspend, reason, requestID)
				}
				return moderation.UserSuspensionResult{UserID: 41, Suspended: suspend, AuditID: 81}, nil
			})
			if err != nil {
				t.Fatalf("newUserModerationHandler() returned error: %v", err)
			}
			handler = withModerationTestRequestID(t, handler)
			request := moderationTestRequest("/moderation/users/41/"+test.action, validModerationForm(), auth.SessionAuthentication{SessionID: 7, Access: actor})
			if test.htmx {
				request.Header.Set("HX-Request", "true")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || calls != 1 || !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
				t.Fatalf("mutation response = (%d, calls %d, headers %v, body %q)", response.Code, calls, response.Header(), response.Body.String())
			}
			if test.htmx {
				if response.Header().Get("HX-Redirect") != "/bb/moderation/users/41" || response.Header().Get("Location") != "" {
					t.Fatalf("HTMX redirect headers = %v", response.Header())
				}
			} else if response.Header().Get("Location") != "/bb/moderation/users/41" || response.Header().Get("HX-Redirect") != "" {
				t.Fatalf("ordinary redirect headers = %v", response.Header())
			}
		})
	}
}

func TestUserModerationHandlerFailsClosedBeforeDelegation(t *testing.T) {
	t.Parallel()

	active := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 11, Role: auth.RoleAdministrator}}
	baseStatus := moderationUserTestStatus()
	for _, test := range []struct {
		name, method, target   string
		authentication         auth.SessionAuthentication
		csrf                   string
		loadErr, changeErr     error
		status                 store.ModerationUserStatus
		result                 moderation.UserSuspensionResult
		form                   url.Values
		withRequestID          bool
		wantStatus             int
		wantLoads, wantChanges int
	}{
		{name: "anonymous GET", method: http.MethodGet, target: "/moderation/users/41", csrf: validCSRFTokenForTest(0x51), wantStatus: http.StatusSeeOther},
		{name: "stale GET", method: http.MethodGet, target: "/moderation/users/41", authentication: auth.SessionAuthentication{SessionID: 7, Access: active.Access, RequiresRevalidation: true}, csrf: validCSRFTokenForTest(0x51), wantStatus: http.StatusSeeOther},
		{name: "noncanonical GET", method: http.MethodGet, target: "/moderation/users/041", authentication: active, csrf: validCSRFTokenForTest(0x51), wantStatus: http.StatusNotFound},
		{name: "query GET", method: http.MethodGet, target: "/moderation/users/41?x=1", authentication: active, csrf: validCSRFTokenForTest(0x51), wantStatus: http.StatusNotFound},
		{name: "missing GET", method: http.MethodGet, target: "/moderation/users/41", authentication: active, csrf: validCSRFTokenForTest(0x51), loadErr: pgx.ErrNoRows, wantStatus: http.StatusNotFound, wantLoads: 1},
		{name: "failed GET", method: http.MethodGet, target: "/moderation/users/41", authentication: active, csrf: validCSRFTokenForTest(0x51), loadErr: errors.New("failed"), wantStatus: http.StatusServiceUnavailable, wantLoads: 1},
		{name: "malformed GET", method: http.MethodGet, target: "/moderation/users/41", authentication: active, csrf: validCSRFTokenForTest(0x51), status: store.ModerationUserStatus{UserID: 41}, wantStatus: http.StatusServiceUnavailable, wantLoads: 1},
		{name: "missing token GET", method: http.MethodGet, target: "/moderation/users/41", authentication: active, status: baseStatus, wantStatus: http.StatusServiceUnavailable, wantLoads: 1},
		{name: "anonymous POST", method: http.MethodPost, target: "/moderation/users/41/suspend", form: validModerationForm(), withRequestID: true, wantStatus: http.StatusSeeOther},
		{name: "noncanonical POST", method: http.MethodPost, target: "/moderation/users/041/suspend", authentication: active, csrf: validCSRFTokenForTest(0x51), form: validModerationForm(), withRequestID: true, wantStatus: http.StatusNotFound},
		{name: "bad csrf POST", method: http.MethodPost, target: "/moderation/users/41/suspend", authentication: active, csrf: validCSRFTokenForTest(0x52), form: validModerationForm(), withRequestID: true, wantStatus: http.StatusForbidden},
		{name: "bad form POST", method: http.MethodPost, target: "/moderation/users/41/suspend", authentication: active, csrf: validCSRFTokenForTest(0x51), form: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}}, withRequestID: true, wantStatus: http.StatusBadRequest},
		{name: "missing request ID POST", method: http.MethodPost, target: "/moderation/users/41/suspend", authentication: active, csrf: validCSRFTokenForTest(0x51), form: validModerationForm(), wantStatus: http.StatusServiceUnavailable},
		{name: "input POST", method: http.MethodPost, target: "/moderation/users/41/suspend", authentication: active, csrf: validCSRFTokenForTest(0x51), form: validModerationForm(), withRequestID: true, changeErr: moderation.ErrUserModerationInput, wantStatus: http.StatusUnprocessableEntity, wantChanges: 1},
		{name: "denied POST", method: http.MethodPost, target: "/moderation/users/41/suspend", authentication: active, csrf: validCSRFTokenForTest(0x51), form: validModerationForm(), withRequestID: true, changeErr: moderation.ErrUserModerationDenied, wantStatus: http.StatusForbidden, wantChanges: 1},
		{name: "conflict POST", method: http.MethodPost, target: "/moderation/users/41/suspend", authentication: active, csrf: validCSRFTokenForTest(0x51), form: validModerationForm(), withRequestID: true, changeErr: moderation.ErrUserModerationConflict, wantStatus: http.StatusConflict, wantChanges: 1},
		{name: "continuity POST", method: http.MethodPost, target: "/moderation/users/41/suspend", authentication: active, csrf: validCSRFTokenForTest(0x51), form: validModerationForm(), withRequestID: true, changeErr: moderation.ErrAdministratorContinuity, wantStatus: http.StatusConflict, wantChanges: 1},
		{name: "missing POST", method: http.MethodPost, target: "/moderation/users/41/suspend", authentication: active, csrf: validCSRFTokenForTest(0x51), form: validModerationForm(), withRequestID: true, changeErr: pgx.ErrNoRows, wantStatus: http.StatusNotFound, wantChanges: 1},
		{name: "failed POST", method: http.MethodPost, target: "/moderation/users/41/suspend", authentication: active, csrf: validCSRFTokenForTest(0x51), form: validModerationForm(), withRequestID: true, changeErr: errors.New("failed"), wantStatus: http.StatusServiceUnavailable, wantChanges: 1},
		{name: "malformed result POST", method: http.MethodPost, target: "/moderation/users/41/suspend", authentication: active, csrf: validCSRFTokenForTest(0x51), form: validModerationForm(), withRequestID: true, result: moderation.UserSuspensionResult{UserID: 41, Suspended: true}, wantStatus: http.StatusServiceUnavailable, wantChanges: 1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			loads, changes := 0, 0
			handler, err := newUserModerationHandler(callbackTestURLBuilder(t), func(context.Context, auth.AccessContext, int64) (store.ModerationUserStatus, error) {
				loads++
				return test.status, test.loadErr
			}, func(context.Context, auth.AccessContext, int64, bool, string, pgtype.UUID) (moderation.UserSuspensionResult, error) {
				changes++
				return test.result, test.changeErr
			})
			if err != nil {
				t.Fatalf("newUserModerationHandler() returned error: %v", err)
			}
			if test.withRequestID {
				handler = withModerationTestRequestID(t, handler)
			}
			var request *http.Request
			if test.method == http.MethodGet {
				request = userModerationGetRequest(test.target, test.authentication, test.csrf)
			} else {
				request = moderationTestRequest(test.target, test.form, test.authentication)
				if test.csrf != validCSRFTokenForTest(0x51) {
					request = request.WithContext(context.WithValue(request.Context(), csrfTokenContextKey{}, test.csrf))
				}
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || loads != test.wantLoads || changes != test.wantChanges || !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
				t.Fatalf("failure response = (%d, loads %d, changes %d, headers %v, body %q)", response.Code, loads, changes, response.Header(), response.Body.String())
			}
		})
	}
}

func TestUserModerationPresentationHelpersAndConstruction(t *testing.T) {
	t.Parallel()

	validLoad := ModerationUserStatusLoader(func(context.Context, auth.AccessContext, int64) (store.ModerationUserStatus, error) {
		return store.ModerationUserStatus{}, nil
	})
	validChange := UserSuspensionChanger(func(context.Context, auth.AccessContext, int64, bool, string, pgtype.UUID) (moderation.UserSuspensionResult, error) {
		return moderation.UserSuspensionResult{}, nil
	})
	for _, test := range []struct {
		builder URLBuilder
		load    ModerationUserStatusLoader
		change  UserSuspensionChanger
	}{{builder: callbackTestURLBuilder(t), load: validLoad}, {builder: callbackTestURLBuilder(t), change: validChange}, {load: validLoad, change: validChange}} {
		if handler, err := newUserModerationHandler(test.builder, test.load, test.change); err == nil || handler != nil {
			t.Fatalf("newUserModerationHandler(missing) = (%v, %v)", handler, err)
		}
	}
	if handler, err := newUserModerationHandler(URLBuilder{basePath: "/\x00", initialized: true}, validLoad, validChange); err == nil || handler != nil {
		t.Fatalf("newUserModerationHandler(invalid absolute URL) = (%v, %v)", handler, err)
	}
	for role, want := range map[policy.Role]string{policy.RoleMember: "Member", policy.RoleModerator: "Moderator", policy.RoleAdministrator: "Administrator"} {
		if got, valid := moderationRoleLabel(role); !valid || got != want {
			t.Fatalf("moderationRoleLabel(%v) = (%q, %t)", role, got, valid)
		}
	}
	if got, valid := moderationRoleLabel(policy.Role(99)); valid || got != "" {
		t.Fatalf("moderationRoleLabel(invalid) = (%q, %t)", got, valid)
	}
	value := userModerationTestTimestamp(userModerationTestTime(8))
	if validModerationViewTime(pgtype.Timestamptz{}) || !validModerationViewTime(value) || formatOptionalModerationTime(pgtype.Timestamptz{}) != "" ||
		formatOptionalModerationTime(value) != "Sep 2, 2026 08:00 UTC" || formatModerationTime(value.Time) != "Sep 2, 2026 08:00 UTC" {
		t.Fatal("moderation presentation time helpers are incorrect")
	}
	if identifier, err := parseUserID("41"); err != nil || identifier != 41 {
		t.Fatalf("parseUserID(41) = (%d, %v)", identifier, err)
	}
	valid := moderationUserTestStatus()
	if label, ok := validModerationUserStatus(valid, 41); !ok || label != "Member" {
		t.Fatalf("validModerationUserStatus(valid) = (%q, %t)", label, ok)
	}
	invalidStatuses := []store.ModerationUserStatus{
		func() store.ModerationUserStatus { value := valid; value.UserID = 42; return value }(),
		func() store.ModerationUserStatus {
			value := valid
			value.UpdatedAt = pgtype.Timestamptz{}
			return value
		}(),
		func() store.ModerationUserStatus {
			value := valid
			value.UpdatedAt = userModerationTestTimestamp(userModerationTestTime(7))
			return value
		}(),
		func() store.ModerationUserStatus {
			value := valid
			value.LastLoginAt = userModerationTestTimestamp(userModerationTestTime(7))
			return value
		}(),
		func() store.ModerationUserStatus {
			value := valid
			value.MutedUntil = pgtype.Timestamptz{Valid: true}
			return value
		}(),
		func() store.ModerationUserStatus {
			value := valid
			value.MutedUntil = userModerationTestTimestamp(userModerationTestTime(8))
			return value
		}(),
		func() store.ModerationUserStatus { value := valid; value.Suspended = true; return value }(),
		func() store.ModerationUserStatus {
			value := valid
			value.SuspendedUntil = userModerationTestTimestamp(userModerationTestTime(12))
			return value
		}(),
		func() store.ModerationUserStatus {
			value := valid
			value.SuspendedAt = pgtype.Timestamptz{Valid: true}
			return value
		}(),
		func() store.ModerationUserStatus {
			value := valid
			value.SuspendedAt = userModerationTestTimestamp(userModerationTestTime(7))
			return value
		}(),
		func() store.ModerationUserStatus {
			value := valid
			value.SuspendedAt = userModerationTestTimestamp(userModerationTestTime(11))
			return value
		}(),
		func() store.ModerationUserStatus {
			value := valid
			value.SuspendedAt = userModerationTestTimestamp(userModerationTestTime(11))
			value.SuspensionReason = pgtype.Text{Valid: true}
			return value
		}(),
		func() store.ModerationUserStatus {
			value := suspendedModerationUserTestStatus()
			value.SuspendedUntil = userModerationTestTimestamp(userModerationTestTime(10))
			return value
		}(),
	}
	for index, status := range invalidStatuses {
		if label, ok := validModerationUserStatus(status, 41); ok || label != "" {
			t.Fatalf("validModerationUserStatus(invalid %d) = (%q, %t)", index, label, ok)
		}
	}
	expired := suspendedModerationUserTestStatus()
	expired.Suspended = false
	expired.SuspendedUntil = userModerationTestTimestamp(userModerationTestTime(12))
	if label, ok := validModerationUserStatus(expired, 41); !ok || label != "Member" {
		t.Fatalf("validModerationUserStatus(expired) = (%q, %t)", label, ok)
	}
}

func TestUserModerationHandlerPropagatesCommittedWriteFailure(t *testing.T) {
	t.Parallel()

	actor := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 11, Role: auth.RoleModerator}}
	for _, load := range []ModerationUserStatusLoader{
		func(context.Context, auth.AccessContext, int64) (store.ModerationUserStatus, error) {
			return moderationUserTestStatus(), nil
		},
		func(context.Context, auth.AccessContext, int64) (store.ModerationUserStatus, error) {
			return store.ModerationUserStatus{}, errors.New("load failed")
		},
	} {
		handler, err := newUserModerationHandler(callbackTestURLBuilder(t), load,
			func(context.Context, auth.AccessContext, int64, bool, string, pgtype.UUID) (moderation.UserSuspensionResult, error) {
				panic("mutation called by GET")
			})
		if err != nil {
			t.Fatalf("newUserModerationHandler() returned error: %v", err)
		}
		writer := &failingRenderResponseWriter{header: make(http.Header), cause: errTestResponseWrite}
		recovered := captureHandlerPanic(func() {
			handler.ServeHTTP(writer, userModerationGetRequest("/moderation/users/41", actor, validCSRFTokenForTest(0x51)))
		})
		if !errors.Is(asError(recovered), errTestResponseWrite) {
			t.Fatalf("user moderation panic = %v, want write cause", recovered)
		}
	}
}

func TestUserModerationRouterAuthenticatesOnlyCanonicalPaths(t *testing.T) {
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
		return auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 11, Role: auth.RoleModerator}}, nil
	}
	validLoad := ModerationUserStatusLoader(func(context.Context, auth.AccessContext, int64) (store.ModerationUserStatus, error) {
		return moderationUserTestStatus(), nil
	})
	validChange := UserSuspensionChanger(func(context.Context, auth.AccessContext, int64, bool, string, pgtype.UUID) (moderation.UserSuspensionResult, error) {
		return moderation.UserSuspensionResult{}, nil
	})
	for _, services := range []struct {
		load   ModerationUserStatusLoader
		change UserSuspensionChanger
	}{{load: validLoad}, {change: validChange}} {
		missing, missingErr := newAuthenticatedHandler(
			builder, service, emptyAreaIndexLister, panicAreaTopicPageLoader, store.MaximumTopicPage,
			panicTopicPostPageLoader, store.MaximumPostPage, nil, nil, nil, nil, nil, nil, nil,
			services.load, services.change, nil, nil, nil, url.URL{}, false, nil, nil, "gotth_bb_session", true, unavailableReadiness,
		)
		if missingErr == nil || missing != nil {
			t.Fatalf("newAuthenticatedHandler(incomplete user moderation) = (%v, %v)", missing, missingErr)
		}
	}
	loads, changes := 0, 0
	handler, err := newAuthenticatedHandler(
		builder, service, emptyAreaIndexLister, panicAreaTopicPageLoader, store.MaximumTopicPage,
		panicTopicPostPageLoader, store.MaximumPostPage, nil, nil, nil, nil, nil, nil, nil,
		func(context.Context, auth.AccessContext, int64) (store.ModerationUserStatus, error) {
			loads++
			return moderationUserTestStatus(), nil
		},
		func(_ context.Context, _ auth.AccessContext, userID int64, suspend bool, _ string, _ pgtype.UUID) (moderation.UserSuspensionResult, error) {
			changes++
			return moderation.UserSuspensionResult{UserID: userID, Suspended: suspend, AuditID: 81}, nil
		},
		nil, nil, nil,
		url.URL{}, false, nil, nil, "gotth_bb_session", true, unavailableReadiness,
	)
	if err != nil {
		t.Fatalf("newAuthenticatedHandler() returned error: %v", err)
	}
	handler = withModerationTestRequestID(t, handler)
	for _, test := range []struct {
		method, target                               string
		wantStatus, wantAuth, wantLoads, wantChanges int
	}{
		{method: http.MethodPut, target: "/moderation/users/41", wantStatus: http.StatusNotFound},
		{method: http.MethodGet, target: "/moderation/users/041", wantStatus: http.StatusNotFound},
		{method: http.MethodGet, target: "/moderation/users/41/", wantStatus: http.StatusNotFound},
		{method: http.MethodGet, target: "/moderation/users/41/suspend", wantStatus: http.StatusNotFound},
		{method: http.MethodPost, target: "/moderation/users/41/suspends", wantStatus: http.StatusNotFound},
		{method: http.MethodGet, target: "/moderation/users/41", wantStatus: http.StatusOK, wantAuth: 1, wantLoads: 1},
		{method: http.MethodPost, target: "/moderation/users/41/suspend", wantStatus: http.StatusSeeOther, wantAuth: 2, wantLoads: 1, wantChanges: 1},
		{method: http.MethodPost, target: "/moderation/users/41/reinstate", wantStatus: http.StatusSeeOther, wantAuth: 3, wantLoads: 1, wantChanges: 2},
	} {
		var body string
		if test.method == http.MethodPost {
			body = url.Values{"_csrf": {csrf}, "reason": {"Clear reason"}}.Encode()
		}
		request := httptest.NewRequest(test.method, test.target, strings.NewReader(body))
		if test.method == http.MethodPost {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		request.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.wantStatus || service.authenticateCalls != test.wantAuth || loads != test.wantLoads || changes != test.wantChanges {
			t.Fatalf("route %s %q = (status %d, auth %d, loads %d, changes %d)", test.method, test.target, response.Code, service.authenticateCalls, loads, changes)
		}
	}
}

func TestTopicPostListHandlerShowsAccountLinksOnlyToEnabledActiveStaff(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		access  auth.AccessContext
		enabled bool
		want    bool
	}{
		{name: "moderator", access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleModerator}, enabled: true, want: true},
		{name: "administrator", access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleAdministrator}, enabled: true, want: true},
		{name: "disabled", access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleModerator}},
		{name: "member", access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleMember}, enabled: true},
		{name: "self", access: auth.AccessContext{Authenticated: true, UserID: 43, Role: auth.RoleModerator}, enabled: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			page := topicPostTestPage(1)
			page.Rows = page.Rows[:1]
			page.Rows[0].PostAuthorID = pgtype.Int8{Int64: 43, Valid: true}
			page.Rows[0].TotalVisiblePosts = 1
			page.TotalPosts, page.TotalPages = 1, 1
			handler, err := newTopicPostListHandler(areaTopicTestBuilder(t), store.MaximumPostPage, func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
				return page, nil
			})
			if err != nil {
				t.Fatalf("newTopicPostListHandler() returned error: %v", err)
			}
			request := topicPostTestRequest("/topics/42", "42", test.access)
			if test.enabled {
				request = request.WithContext(context.WithValue(request.Context(), userModerationLinksContextKey{}, true))
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body := response.Body.String()
			found := strings.Contains(body, `/bb/moderation/users/43`) && strings.Contains(body, "Account status")
			if response.Code != http.StatusOK || found != test.want {
				t.Fatalf("account link response = (status %d, found %t, body %q)", response.Code, found, body)
			}
		})
	}
}

func moderationUserTestStatus() store.ModerationUserStatus {
	return store.ModerationUserStatus{
		UserID: 41, DisplayName: "Local member", Role: policy.RoleMember,
		CreatedAt:   userModerationTestTimestamp(userModerationTestTime(8)),
		UpdatedAt:   userModerationTestTimestamp(userModerationTestTime(9)),
		LastLoginAt: userModerationTestTimestamp(userModerationTestTime(10)),
	}
}

func suspendedModerationUserTestStatus() store.ModerationUserStatus {
	status := moderationUserTestStatus()
	status.Suspended = true
	status.SuspendedAt = userModerationTestTimestamp(userModerationTestTime(11))
	status.SuspensionReason = pgtype.Text{String: "Repeated abuse", Valid: true}
	status.MutedUntil = userModerationTestTimestamp(userModerationTestTime(13))
	return status
}

func userModerationGetRequest(target string, authentication auth.SessionAuthentication, csrf string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	ctx := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, authentication)
	ctx = context.WithValue(ctx, csrfTokenContextKey{}, csrf)
	return request.WithContext(ctx)
}

func userModerationTestTime(hour int) time.Time {
	return time.Date(2026, time.September, 2, hour, 0, 0, 0, time.UTC)
}

func userModerationTestTimestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}
