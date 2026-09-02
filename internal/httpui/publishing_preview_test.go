package httpui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/forum"
)

func TestPublishingFormsExposeProgressivePreviewActions(t *testing.T) {
	t.Parallel()

	handler := newPublishingTestHandler(t, nil, nil)
	request := publishingTestRequest(http.MethodGet, "/topics/new?area=news", "", true)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	for _, want := range []string{`formaction="/bb/topics/preview"`, `hx-post="/bb/topics/preview"`, `hx-target="#main-content"`, `hx-swap="outerHTML"`, `hx-get="/bb/areas/news"`, `hx-push-url="true"`} {
		if response.Code != http.StatusOK || !strings.Contains(body, want) {
			t.Fatalf("new-topic form missing %q = (%d, %q)", want, response.Code, body)
		}
	}
}

func TestPublishingHandlerPreviewsSanitizedTopicAndReplyDrafts(t *testing.T) {
	t.Parallel()

	handler := newPublishingTestHandler(t, nil, nil)
	for _, test := range []struct {
		name   string
		target string
		form   url.Values
		want   []string
	}{
		{
			name: "topic", target: "/topics/preview",
			form: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "area": {"news"}, "title": {"Kept title"}, "markdown": {"Hello **world** <script>alert(1)</script>"}},
			want: []string{`value="Kept title"`, `Hello **world** &lt;script&gt;alert(1)&lt;/script&gt;`, `<strong>world</strong>`, `alert(1)`},
		},
		{
			name: "reply", target: "/topics/41/replies/preview",
			form: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "markdown": {"A [safe](https://example.test) reply"}},
			want: []string{`A [safe](https://example.test) reply`, `<a href="https://example.test" rel="nofollow noreferrer">safe</a>`, `formaction="/bb/topics/41/replies/preview"`},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for _, fragment := range []bool{false, true} {
				request := publishingTestRequest(http.MethodPost, test.target, test.form.Encode(), true)
				if fragment {
					request.Header.Set("HX-Request", "true")
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				body := response.Body.String()
				if response.Code != http.StatusOK || strings.HasPrefix(body, "<!doctype html>") == fragment {
					t.Fatalf("preview fragment=%t = (%d, %q)", fragment, response.Code, body)
				}
				for _, required := range test.want {
					if !strings.Contains(body, required) {
						t.Fatalf("preview lacks %q: %s", required, body)
					}
				}
				if strings.Contains(body, "<script>") || strings.Contains(body, "{true false}") || strings.Contains(body, "aria-invalid") {
					t.Fatalf("preview contains unsafe HTML or malformed attribute: %s", body)
				}
			}
		})
	}
}

func TestPublishingPreviewValidationPreservesDraftWithoutPublishing(t *testing.T) {
	t.Parallel()

	called := false
	handler := newPublishingTestHandler(t, func(context.Context, auth.AccessContext, string, string, string) (forum.PublishResult, error) {
		called = true
		return forum.PublishResult{}, nil
	}, nil)
	form := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "area": {"news"}, "title": {"Kept title"}, "markdown": {" "}}
	request := publishingTestRequest(http.MethodPost, "/topics/preview", form.Encode(), true)
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || called || !strings.Contains(response.Body.String(), "Check the Markdown body.") ||
		!strings.Contains(response.Body.String(), "Kept title") || !strings.Contains(response.Body.String(), `aria-invalid="true"`) {
		t.Fatalf("invalid preview = (status %d published %t body %q)", response.Code, called, response.Body.String())
	}
}

func TestPublishingPreviewRejectsCSRFBeforeRendering(t *testing.T) {
	t.Parallel()

	handler := newPublishingTestHandler(t, nil, nil)
	request := publishingTestRequest(http.MethodPost, "/topics/preview", "area=news&title=secret-title&markdown=secret-body", true)
	request = request.WithContext(context.WithValue(request.Context(), csrfTokenContextKey{}, ""))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("CSRF preview failure = (%d, %q)", response.Code, response.Body.String())
	}
}

func TestPublishingPreviewFailsClosedAtEveryRequestBoundary(t *testing.T) {
	t.Parallel()

	validTopic := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "area": {"news"}, "title": {"Title"}, "markdown": {"body"}}.Encode()
	validReply := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "markdown": {"body"}}.Encode()
	for _, test := range []struct {
		name          string
		target        string
		body          string
		authenticated bool
		stale         bool
		missingCSRF   bool
		wantStatus    int
		wantRedirect  string
	}{
		{name: "anonymous", target: "/topics/preview", authenticated: false, wantStatus: http.StatusSeeOther, wantRedirect: "/bb/login"},
		{name: "stale reply", target: "/topics/41/replies/preview", authenticated: true, stale: true, wantStatus: http.StatusSeeOther, wantRedirect: "/bb/auth/revalidate"},
		{name: "topic query", target: "/topics/preview?extra=1", body: validTopic, authenticated: true, wantStatus: http.StatusBadRequest},
		{name: "topic unknown field", target: "/topics/preview", body: validTopic + "&extra=x", authenticated: true, wantStatus: http.StatusBadRequest},
		{name: "malformed reply", target: "/topics/041/replies/preview", body: validReply, authenticated: true, wantStatus: http.StatusNotFound},
		{name: "reply CSRF", target: "/topics/41/replies/preview", body: "markdown=secret-body", authenticated: true, missingCSRF: true, wantStatus: http.StatusForbidden},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := newPublishingTestHandler(t, nil, nil)
			request := publishingTestRequest(http.MethodPost, test.target, test.body, test.authenticated)
			if test.stale {
				authentication := sessionAuthenticationFromContext(request.Context())
				authentication.RequiresRevalidation = true
				request = request.WithContext(context.WithValue(request.Context(), sessionAuthenticationContextKey{}, authentication))
			}
			if test.missingCSRF {
				request = request.WithContext(context.WithValue(request.Context(), csrfTokenContextKey{}, ""))
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || response.Header().Get("Location") != test.wantRedirect || strings.Contains(response.Body.String(), "secret-body") {
				t.Fatalf("preview boundary = (status %d location %q body %q)", response.Code, response.Header().Get("Location"), response.Body.String())
			}
		})
	}
}

func TestPublishingReplyPreviewReturnsFieldSpecificValidation(t *testing.T) {
	t.Parallel()

	handler := newPublishingTestHandler(t, nil, nil)
	form := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "markdown": {" "}}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, publishingTestRequest(http.MethodPost, "/topics/41/replies/preview", form.Encode(), true))
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "Check the Markdown body.") ||
		!strings.Contains(response.Body.String(), `formaction="/bb/topics/41/replies/preview"`) {
		t.Fatalf("invalid reply preview = (%d, %q)", response.Code, response.Body.String())
	}
}
