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

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/forum"
	"git.dannyhunn.com/agents/gotth-bb/internal/moderation"
	"git.dannyhunn.com/agents/gotth-bb/internal/observability"
	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const moderationTestRequestID = "51515151515151515151515151515151"

func TestModerationHandlerLocksAndUnlocksWithExactServerAuthority(t *testing.T) {
	t.Parallel()

	wantActor := auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleModerator}
	wantUUID := pgtype.UUID{Bytes: [16]byte{0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51, 0x51}, Valid: true}
	for _, test := range []struct {
		name, suffix, state string
		lock, htmx          bool
		wantStatus          int
	}{
		{name: "lock", suffix: "lock", state: "locked", lock: true, wantStatus: http.StatusSeeOther},
		{name: "unlock HTMX", suffix: "unlock", state: "open", htmx: true, wantStatus: http.StatusNoContent},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			handler := newModerationTestHandler(t, func(ctx context.Context, actor auth.AccessContext, topicID int64, lock bool, reason string, requestID pgtype.UUID) (moderation.TopicTransitionResult, error) {
				calls++
				if ctx == nil || !reflect.DeepEqual(actor, wantActor) || topicID != 41 || lock != test.lock || reason != "Clear reason" || requestID != wantUUID {
					t.Fatalf("topic lock call = (%v, %+v, %d, %t, %q, %+v)", ctx, actor, topicID, lock, reason, requestID)
				}
				return moderation.TopicTransitionResult{TopicID: 41, State: policy.TopicState(test.state), AuditID: 71}, nil
			})
			request := moderationTestRequest("/topics/41/"+test.suffix, url.Values{
				"_csrf": {validCSRFTokenForTest(0x51)}, "reason": {"Clear reason"},
			}, auth.SessionAuthentication{SessionID: 7, Access: wantActor})
			if test.htmx {
				request.Header.Set("HX-Request", "true")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || calls != 1 || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("moderation response = (status %d, calls %d, headers %v, body %q)", response.Code, calls, response.Header(), response.Body.String())
			}
			if test.htmx {
				if response.Header().Get("HX-Redirect") != "/bb/topics/41" || response.Header().Get("Location") != "" {
					t.Fatalf("HTMX moderation redirect headers = %v", response.Header())
				}
			} else if response.Header().Get("Location") != "/bb/topics/41" || response.Header().Get("HX-Redirect") != "" {
				t.Fatalf("ordinary moderation redirect headers = %v", response.Header())
			}
		})
	}
}

func TestModerationHandlerFailsClosedBeforeMutation(t *testing.T) {
	t.Parallel()

	active := auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleModerator}}
	stale := active
	stale.RequiresRevalidation = true
	for _, test := range []struct {
		name, target   string
		authentication auth.SessionAuthentication
		form           url.Values
		requestID      bool
		wantStatus     int
		wantLocation   string
	}{
		{name: "anonymous", target: "/topics/41/lock", form: validModerationForm(), requestID: true, wantStatus: http.StatusSeeOther, wantLocation: "/bb/login"},
		{name: "stale", target: "/topics/41/lock", authentication: stale, form: validModerationForm(), requestID: true, wantStatus: http.StatusSeeOther, wantLocation: "/bb/auth/revalidate"},
		{name: "query", target: "/topics/41/lock?x=1", authentication: active, form: validModerationForm(), requestID: true, wantStatus: http.StatusNotFound},
		{name: "CSRF", target: "/topics/41/lock", authentication: active, form: url.Values{"reason": {"secret reason"}}, requestID: true, wantStatus: http.StatusForbidden},
		{name: "unknown field", target: "/topics/41/lock", authentication: active, form: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "reason": {"secret reason"}, "extra": {"x"}}, requestID: true, wantStatus: http.StatusBadRequest},
		{name: "missing request ID", target: "/topics/41/lock", authentication: active, form: validModerationForm(), wantStatus: http.StatusServiceUnavailable},
		{name: "oversized", target: "/topics/41/lock", authentication: active, form: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "reason": {strings.Repeat("x", maximumModerationFormBytes)}}, requestID: true, wantStatus: http.StatusForbidden},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			base, err := newModerationHandler(callbackTestURLBuilder(t), func(context.Context, auth.AccessContext, int64, bool, string, pgtype.UUID) (moderation.TopicTransitionResult, error) {
				calls++
				return moderation.TopicTransitionResult{}, nil
			})
			if err != nil {
				t.Fatalf("newModerationHandler() returned error: %v", err)
			}
			var handler http.Handler = base
			if test.requestID {
				handler = withModerationTestRequestID(t, handler)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, moderationTestRequest(test.target, test.form, test.authentication))
			if response.Code != test.wantStatus || response.Header().Get("Location") != test.wantLocation || calls != 0 ||
				strings.Contains(response.Body.String(), "secret reason") {
				t.Fatalf("failure response = (status %d, location %q, calls %d, body %q)", response.Code, response.Header().Get("Location"), calls, response.Body.String())
			}
		})
	}
}

func TestModerationHandlerMapsServiceFailuresAndInvalidResults(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		result     moderation.TopicTransitionResult
		err        error
		wantStatus int
		htmx       bool
	}{
		{name: "input", err: moderation.ErrTopicModerationInput, wantStatus: http.StatusUnprocessableEntity},
		{name: "denied", err: moderation.ErrTopicModerationDenied, wantStatus: http.StatusForbidden},
		{name: "conflict", err: moderation.ErrTopicModerationConflict, wantStatus: http.StatusConflict, htmx: true},
		{name: "missing", err: pgx.ErrNoRows, wantStatus: http.StatusNotFound},
		{name: "failure", err: errors.New("secret database failure"), wantStatus: http.StatusServiceUnavailable},
		{name: "wrong topic", result: moderation.TopicTransitionResult{TopicID: 40, State: policy.TopicLocked, AuditID: 71}, wantStatus: http.StatusServiceUnavailable},
		{name: "wrong state", result: moderation.TopicTransitionResult{TopicID: 41, State: policy.TopicOpen, AuditID: 71}, wantStatus: http.StatusServiceUnavailable},
		{name: "missing audit", result: moderation.TopicTransitionResult{TopicID: 41, State: policy.TopicLocked}, wantStatus: http.StatusServiceUnavailable},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := newModerationTestHandler(t, func(context.Context, auth.AccessContext, int64, bool, string, pgtype.UUID) (moderation.TopicTransitionResult, error) {
				return test.result, test.err
			})
			response := httptest.NewRecorder()
			request := moderationTestRequest("/topics/41/lock", validModerationForm(), auth.SessionAuthentication{
				SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleModerator},
			})
			if test.htmx {
				request.Header.Set("HX-Request", "true")
			}
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("service failure = (status %d, body %q)", response.Code, response.Body.String())
			}
			if test.htmx && (!strings.Contains(response.Body.String(), `<main id="main-content"`) || strings.Contains(response.Body.String(), "<!doctype html>")) {
				t.Fatalf("HTMX failure broke main-content contract: %q", response.Body.String())
			}
		})
	}
}

func TestModerationRouterAuthenticatesOnlyCanonicalMutationPaths(t *testing.T) {
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
		return auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleModerator}}, nil
	}
	if missing, missingErr := NewAuthenticatedModeratedForumHandler(
		builder, service, emptyAreaIndexLister, panicAreaTopicPageLoader, store.MaximumTopicPage,
		panicTopicPostPageLoader, store.MaximumPostPage, nil, nil, nil, nil, nil, nil,
		"gotth_bb_session", true,
	); missingErr == nil || missing != nil {
		t.Fatalf("NewAuthenticatedModeratedForumHandler(missing) = (%v, %v)", missing, missingErr)
	}
	changes := 0
	handler, err := NewAuthenticatedModeratedForumHandler(
		builder, service, emptyAreaIndexLister, panicAreaTopicPageLoader, store.MaximumTopicPage,
		panicTopicPostPageLoader, store.MaximumPostPage,
		func(context.Context, auth.AccessContext, string, string, string) (forum.PublishResult, error) {
			panic("publish")
		},
		func(context.Context, auth.AccessContext, int64, string) (forum.PublishResult, error) { panic("reply") },
		func(context.Context, auth.AccessContext, int64) (store.EditablePost, error) { panic("load edit") },
		func(context.Context, auth.AccessContext, int64, int32, string) (forum.EditResult, error) {
			panic("edit")
		},
		func(context.Context, auth.AccessContext, int64, int32) (forum.DeleteResult, error) { panic("delete") },
		func(_ context.Context, _ auth.AccessContext, _ int64, lock bool, _ string, _ pgtype.UUID) (moderation.TopicTransitionResult, error) {
			changes++
			state := policy.TopicLocked
			if !lock {
				state = policy.TopicOpen
			}
			return moderation.TopicTransitionResult{TopicID: 41, State: state, AuditID: 71}, nil
		},
		"gotth_bb_session", true,
	)
	if err != nil {
		t.Fatalf("NewAuthenticatedModeratedForumHandler() returned error: %v", err)
	}
	handler = withModerationTestRequestID(t, handler)
	for _, test := range []struct {
		target                            string
		wantStatus, wantAuth, wantChanges int
	}{
		{target: "/topics/041/lock", wantStatus: http.StatusNotFound},
		{target: "/topics/41/lock", wantStatus: http.StatusSeeOther, wantAuth: 1, wantChanges: 1},
		{target: "/topics/41/unlock", wantStatus: http.StatusSeeOther, wantAuth: 2, wantChanges: 2},
	} {
		form := url.Values{"_csrf": {csrf}, "reason": {"Clear reason"}}
		request := httptest.NewRequest(http.MethodPost, test.target, strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.wantStatus || service.authenticateCalls != test.wantAuth || changes != test.wantChanges {
			t.Fatalf("route %q = (status %d, auth %d, changes %d)", test.target, response.Code, service.authenticateCalls, changes)
		}
	}
}

func TestModerationFormAndRequestIDBoundaries(t *testing.T) {
	t.Parallel()

	for _, form := range []url.Values{
		{},
		{"reason": {"one", "two"}},
		{"reason": {"reason"}, "extra": {"x"}},
	} {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if reason, err := parseModerationForm(request); err == nil || reason != "" {
			t.Fatalf("parseModerationForm(%v) = (%q, %v)", form, reason, err)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Body = failingPublishingBody{}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if reason, err := parseModerationForm(request); err == nil || reason != "" {
		t.Fatalf("parseModerationForm(read failure) = (%q, %v)", reason, err)
	}
	if requestID, err := moderationRequestUUID(nil); err == nil || requestID.Valid {
		t.Fatalf("moderationRequestUUID(nil) = (%+v, %v)", requestID, err)
	}
	for _, invalid := range []string{"", strings.Repeat("1", 31), strings.Repeat("A", 32), strings.Repeat("z", 32)} {
		if requestID, err := decodeModerationRequestID(invalid); err == nil || requestID.Valid {
			t.Fatalf("decodeModerationRequestID(%q) = (%+v, %v)", invalid, requestID, err)
		}
	}
	if requestID, err := decodeModerationRequestID(moderationTestRequestID); err != nil || !requestID.Valid || requestID.Bytes[0] != 0x51 || requestID.Bytes[15] != 0x51 {
		t.Fatalf("decodeModerationRequestID(valid) = (%+v, %v)", requestID, err)
	}
	if requestID, err := decodeModerationRequestID(strings.Repeat("ab", 16)); err != nil || !requestID.Valid || requestID.Bytes[0] != 0xab || requestID.Bytes[15] != 0xab {
		t.Fatalf("decodeModerationRequestID(hex letters) = (%+v, %v)", requestID, err)
	}
}

func TestNewModerationHandlerRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	valid := TopicLockChanger(func(context.Context, auth.AccessContext, int64, bool, string, pgtype.UUID) (moderation.TopicTransitionResult, error) {
		return moderation.TopicTransitionResult{}, nil
	})
	for _, test := range []struct {
		builder URLBuilder
		change  TopicLockChanger
	}{{builder: callbackTestURLBuilder(t)}, {change: valid}} {
		if handler, err := newModerationHandler(test.builder, test.change); err == nil || handler != nil {
			t.Fatalf("newModerationHandler(missing) = (%v, %v)", handler, err)
		}
	}
}

func newModerationTestHandler(t *testing.T, change TopicLockChanger) http.Handler {
	t.Helper()
	handler, err := newModerationHandler(callbackTestURLBuilder(t), change)
	if err != nil {
		t.Fatalf("newModerationHandler() returned error: %v", err)
	}
	return withModerationTestRequestID(t, handler)
}

func withModerationTestRequestID(t *testing.T, handler http.Handler) http.Handler {
	t.Helper()
	wrapped, err := observability.NewRequestIDMiddleware(handler, func() (string, error) { return moderationTestRequestID, nil })
	if err != nil {
		t.Fatalf("NewRequestIDMiddleware() returned error: %v", err)
	}
	return wrapped
}

func moderationTestRequest(target string, form url.Values, authentication auth.SessionAuthentication) *http.Request {
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, authentication)
	ctx = context.WithValue(ctx, csrfTokenContextKey{}, validCSRFTokenForTest(0x51))
	return request.WithContext(ctx)
}

func validModerationForm() url.Values {
	return url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "reason": {"Clear reason"}}
}
