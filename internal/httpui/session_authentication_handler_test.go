package httpui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
)

func TestSessionAuthenticationHandlerLoadsExactCookieIntoContext(t *testing.T) {
	t.Parallel()

	want := auth.SessionAuthentication{
		Access: auth.AccessContext{
			Authenticated: true,
			UserID:        42,
			Role:          auth.RoleAdministrator,
			ValidatedAt:   time.Date(2026, time.September, 1, 14, 0, 0, 0, time.UTC),
		},
		RequiresRevalidation: true,
	}
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x6a}, sessionCookieTokenBytes))
	wantCSRF, err := deriveCSRFToken(token)
	if err != nil {
		t.Fatalf("deriveCSRFToken() returned error: %v", err)
	}
	calls := 0
	handler, err := newSessionAuthenticationHandler(
		http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if got := sessionAuthenticationFromContext(request.Context()); !reflect.DeepEqual(got, want) {
				t.Fatalf("downstream authentication = %+v, want %+v", got, want)
			}
			if got := csrfTokenFromContext(request.Context()); got != wantCSRF {
				t.Fatalf("downstream CSRF token = %q, want %q", got, wantCSRF)
			}
			request.Pattern = "GET /topics/{topicID}"
			response.WriteHeader(http.StatusAccepted)
		}),
		func(ctx context.Context, credential string) (auth.SessionAuthentication, error) {
			calls++
			if ctx == nil || credential != token {
				t.Fatalf("authenticate input = (%v, %q)", ctx, credential)
			}
			return want, nil
		},
		"gotth_bb_session", callbackTestURLBuilder(t), true,
	)
	if err != nil {
		t.Fatalf("newSessionAuthenticationHandler() returned error: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || calls != 1 || response.Header().Get("Vary") != "Cookie" || response.Header().Get("Set-Cookie") != "" ||
		request.Pattern != "GET /topics/{topicID}" {
		t.Fatalf("response = (status %d, calls %d, pattern %q, headers %v)", response.Code, calls, request.Pattern, response.Header())
	}
}

func TestSessionAuthenticationHandlerPropagatesRoutePatternDuringPanic(t *testing.T) {
	t.Parallel()

	handler, err := newSessionAuthenticationHandler(
		http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			request.Pattern = "POST /topics/{topicID}/replies"
			panic(errTestResponseWrite)
		}),
		func(context.Context, string) (auth.SessionAuthentication, error) {
			panic("authentication must not run")
		},
		"session", callbackTestURLBuilder(t), true,
	)
	if err != nil {
		t.Fatalf("newSessionAuthenticationHandler() returned error: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/topics/7/replies", nil)
	recovered := captureHandlerPanic(func() { handler.ServeHTTP(httptest.NewRecorder(), request) })
	if !errors.Is(asError(recovered), errTestResponseWrite) || request.Pattern != "POST /topics/{topicID}/replies" {
		t.Fatalf("panic/pattern = (%v, %q)", recovered, request.Pattern)
	}
}

func TestSessionAuthenticationHandlerUsesAnonymousContextWithoutCookie(t *testing.T) {
	t.Parallel()

	handler, err := newSessionAuthenticationHandler(
		http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			if got := sessionAuthenticationFromContext(request.Context()); !reflect.DeepEqual(got, auth.SessionAuthentication{}) {
				t.Fatalf("downstream authentication = %+v, want anonymous", got)
			}
			if got := csrfTokenFromContext(request.Context()); got != "" {
				t.Fatalf("anonymous CSRF token = %q, want empty", got)
			}
		}),
		func(context.Context, string) (auth.SessionAuthentication, error) {
			panic("authentication must not run")
		},
		"session", callbackTestURLBuilder(t), false,
	)
	if err != nil {
		t.Fatalf("newSessionAuthenticationHandler() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK || response.Header().Get("Set-Cookie") != "" || response.Header().Get("Vary") != "Cookie" {
		t.Fatalf("anonymous response = (status %d, headers %v)", response.Code, response.Header())
	}
}

func TestSessionAuthenticationHandlerExpiresUnusableBrowserState(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name             string
		cookieHeader     string
		wantAuthenticate int
	}{
		{name: "inactive", cookieHeader: "session=inactive", wantAuthenticate: 1},
		{name: "duplicate", cookieHeader: "session=one; session=two"},
		{name: "quoted", cookieHeader: `session="opaque-token"`},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			handler, err := newSessionAuthenticationHandler(
				http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
					if got := sessionAuthenticationFromContext(request.Context()); !reflect.DeepEqual(got, auth.SessionAuthentication{}) {
						t.Fatalf("downstream authentication = %+v, want anonymous", got)
					}
				}),
				func(context.Context, string) (auth.SessionAuthentication, error) {
					calls++
					return auth.SessionAuthentication{}, nil
				},
				"session", callbackTestURLBuilder(t), true,
			)
			if err != nil {
				t.Fatalf("newSessionAuthenticationHandler() returned error: %v", err)
			}
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Cookie", test.cookieHeader)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			cookies := response.Result().Cookies()
			if response.Code != http.StatusOK || calls != test.wantAuthenticate || len(cookies) != 1 ||
				cookies[0].Name != "session" || cookies[0].Value != "" || cookies[0].Path != "/bb/" ||
				cookies[0].MaxAge != -1 || !cookies[0].HttpOnly || !cookies[0].Secure ||
				cookies[0].SameSite != http.SameSiteLaxMode || !cookies[0].Expires.Equal(time.Unix(1, 0).UTC()) {
				t.Fatalf("response = (status %d, calls %d, cookies %+v)", response.Code, calls, cookies)
			}
		})
	}
}

func TestSessionAuthenticationHandlerKeepsParallelRequestsIsolated(t *testing.T) {
	t.Parallel()

	handler, err := newSessionAuthenticationHandler(
		http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			got := sessionAuthenticationFromContext(request.Context())
			wantUserID, _ := request.Context().Value(parallelSessionUserIDContextKey{}).(int64)
			if !got.Access.Authenticated || got.Access.UserID != wantUserID {
				t.Errorf("downstream authentication = %+v, want user %d", got, wantUserID)
			}
			cookies := request.CookiesNamed("session")
			if len(cookies) != 1 {
				t.Errorf("downstream session cookies = %+v", cookies)
				return
			}
			wantCSRF, err := deriveCSRFToken(cookies[0].Value)
			if got := csrfTokenFromContext(request.Context()); err != nil || got != wantCSRF {
				t.Errorf("downstream CSRF token = (%q, %v), want %q", got, err, wantCSRF)
			}
		}),
		func(ctx context.Context, _ string) (auth.SessionAuthentication, error) {
			userID, _ := ctx.Value(parallelSessionUserIDContextKey{}).(int64)
			return auth.SessionAuthentication{Access: auth.AccessContext{
				Authenticated: true,
				UserID:        userID,
				Role:          auth.RoleMember,
				ValidatedAt:   time.Unix(userID, 0).UTC(),
			}}, nil
		},
		"session", callbackTestURLBuilder(t), true,
	)
	if err != nil {
		t.Fatalf("newSessionAuthenticationHandler() returned error: %v", err)
	}
	for userID := int64(1); userID <= 32; userID++ {
		userID := userID
		t.Run(time.Unix(userID, 0).UTC().Format(time.RFC3339), func(t *testing.T) {
			t.Parallel()
			ctx := context.WithValue(context.Background(), parallelSessionUserIDContextKey{}, userID)
			request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
			token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(userID)}, sessionCookieTokenBytes))
			request.AddCookie(&http.Cookie{Name: "session", Value: token})
			handler.ServeHTTP(httptest.NewRecorder(), request)
		})
	}
}

func TestSessionAuthenticationHandlerRejectsAuthenticatedMalformedCredential(t *testing.T) {
	t.Parallel()

	called := false
	handler, err := newSessionAuthenticationHandler(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }),
		func(context.Context, string) (auth.SessionAuthentication, error) {
			return auth.SessionAuthentication{Access: auth.AccessContext{
				Authenticated: true, UserID: 42, Role: auth.RoleMember, ValidatedAt: time.Now(),
			}}, nil
		},
		"session", callbackTestURLBuilder(t), true,
	)
	if err != nil {
		t.Fatalf("newSessionAuthenticationHandler() returned error: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: "malformed"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || called || len(response.Result().Cookies()) != 1 ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response = (status %d, called %v, headers %v)", response.Code, called, response.Header())
	}
}

type parallelSessionUserIDContextKey struct{}

func TestSessionAuthenticationHandlerFailsClosedOnAuthenticationError(t *testing.T) {
	t.Parallel()

	const secret = "do-not-leak-session-authentication-failure"
	called := false
	handler, err := newSessionAuthenticationHandler(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }),
		func(context.Context, string) (auth.SessionAuthentication, error) {
			return auth.SessionAuthentication{}, errors.New(secret)
		},
		"session", callbackTestURLBuilder(t), true,
	)
	if err != nil {
		t.Fatalf("newSessionAuthenticationHandler() returned error: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: "opaque-token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || called || response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("Set-Cookie") != "" || strings.Contains(response.Body.String(), secret) {
		t.Fatalf("failed response = (status %d, called %v, headers %v, body %q)", response.Code, called, response.Header(), response.Body.String())
	}
}

func TestSessionAuthenticationHandlerRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	builder := callbackTestURLBuilder(t)
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	authenticate := sessionAuthenticator(func(context.Context, string) (auth.SessionAuthentication, error) {
		return auth.SessionAuthentication{}, nil
	})
	for _, test := range []struct {
		name         string
		next         http.Handler
		authenticate sessionAuthenticator
		cookie       string
		builder      URLBuilder
	}{
		{name: "downstream", authenticate: authenticate, cookie: "session", builder: builder},
		{name: "authenticator", next: next, cookie: "session", builder: builder},
		{name: "empty cookie", next: next, authenticate: authenticate, builder: builder},
		{name: "invalid cookie", next: next, authenticate: authenticate, cookie: "bad name", builder: builder},
		{name: "builder", next: next, authenticate: authenticate, cookie: "session"},
		{name: "invalid cookie path", next: next, authenticate: authenticate, cookie: "session", builder: URLBuilder{basePath: "/bad;path", initialized: true}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got, err := newSessionAuthenticationHandler(test.next, test.authenticate, test.cookie, test.builder, true); err == nil || got != nil {
				t.Fatalf("newSessionAuthenticationHandler() = (%v, %v), want nil/error", got, err)
			}
		})
	}
}
