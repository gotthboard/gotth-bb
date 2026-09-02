package httpui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/forum"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"github.com/jackc/pgx/v5"
)

func TestAuthenticatedForumRouterLoadsSessionOnlyForCanonicalEditRoutes(t *testing.T) {
	t.Parallel()

	sessionToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x62}, 32))
	service := &authenticatedHandlerTestService{}
	service.authenticate = func(context.Context, string) (auth.SessionAuthentication, error) {
		service.authenticateCalls++
		return auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleMember}}, nil
	}
	loadCalls, deleteCalls := 0, 0
	handler, err := NewAuthenticatedForumHandler(
		callbackTestURLBuilder(t), service, emptyAreaIndexLister,
		func(context.Context, auth.AccessContext, string, int32) (store.VisibleAreaTopicPage, error) {
			panic("area topics not expected")
		},
		store.MaximumTopicPage,
		func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
			panic("topic posts not expected")
		},
		store.MaximumPostPage,
		func(context.Context, auth.AccessContext, string, string, string) (forum.PublishResult, error) {
			panic("topic creation not expected")
		},
		func(context.Context, auth.AccessContext, int64, int64, string) (forum.PublishResult, error) {
			panic("reply creation not expected")
		},
		func(context.Context, auth.AccessContext, int64) (store.EditablePost, error) {
			loadCalls++
			return validEditablePost(), nil
		},
		func(context.Context, auth.AccessContext, int64, int32, string) (forum.EditResult, error) {
			panic("edit not expected")
		},
		func(context.Context, auth.AccessContext, int64, int32) (forum.DeleteResult, error) {
			deleteCalls++
			return forum.DeleteResult{TopicID: 41, PostID: 91, PostNumber: 2, Revision: 3}, nil
		},
		"gotth_bb_session", true,
	)
	if err != nil {
		t.Fatalf("NewAuthenticatedForumHandler() returned error: %v", err)
	}
	malformed := httptest.NewRequest(http.MethodGet, "/posts/091/edit", nil)
	malformed.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	malformedResponse := httptest.NewRecorder()
	handler.ServeHTTP(malformedResponse, malformed)
	if malformedResponse.Code != http.StatusNotFound || service.authenticateCalls != 0 || loadCalls != 0 {
		t.Fatalf("malformed edit route = (%d, auth %d, loads %d)", malformedResponse.Code, service.authenticateCalls, loadCalls)
	}
	request := httptest.NewRequest(http.MethodGet, "/posts/91/edit", nil)
	request.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.authenticateCalls != 1 || loadCalls != 1 || request.Pattern != "GET /posts/{postID}/edit" {
		t.Fatalf("canonical edit route = (%d, auth %d, loads %d, pattern %q, body %q)", response.Code, service.authenticateCalls, loadCalls, request.Pattern, response.Body.String())
	}
	csrfToken, err := deriveCSRFToken(sessionToken)
	if err != nil {
		t.Fatalf("deriveCSRFToken() returned error: %v", err)
	}
	deleteRequest := httptest.NewRequest(http.MethodPost, "/posts/91/delete", strings.NewReader(url.Values{"_csrf": {csrfToken}, "revision": {"3"}}.Encode()))
	deleteRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	deleteRequest.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	deleteResponse := httptest.NewRecorder()
	handler.ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusSeeOther || deleteResponse.Header().Get("Location") != "/bb/topics/41" || service.authenticateCalls != 2 || deleteCalls != 1 {
		t.Fatalf("canonical delete route = (%d, %q, auth %d, deletes %d)", deleteResponse.Code, deleteResponse.Header().Get("Location"), service.authenticateCalls, deleteCalls)
	}
}

func TestEditingHandlerLoadsCanonicalAuthorForm(t *testing.T) {
	t.Parallel()

	var gotAccess auth.AccessContext
	handler := newEditingTestHandler(t, func(_ context.Context, access auth.AccessContext, postID int64) (store.EditablePost, error) {
		gotAccess = access
		return store.EditablePost{PostID: postID, TopicID: 41, PostNumber: 27, MarkdownSource: "Original <body>", Revision: 3}, nil
	}, nil)
	request := publishingTestRequest(http.MethodGet, "/posts/91/edit", "", true)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || request.Pattern != "GET /posts/{postID}/edit" || gotAccess.UserID != 42 {
		t.Fatalf("edit form = (status %d pattern %q access %+v body %q)", response.Code, request.Pattern, gotAccess, body)
	}
	for _, required := range []string{
		`action="/bb/posts/91/edit"`, `formaction="/bb/posts/91/edit/preview"`,
		`name="revision" value="3"`, `Original &lt;body&gt;`, `href="/bb/topics/41?page=2#post-91"`, `Save edit`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("edit form lacks %q: %s", required, body)
		}
	}
	if strings.Contains(body, `name="title"`) || strings.Contains(body, `name="area"`) {
		t.Fatalf("edit form contains topic-only fields: %s", body)
	}
}

func TestEditingHandlerPreviewsSanitizedSubmittedDraft(t *testing.T) {
	t.Parallel()

	editCalls := 0
	handler := newEditingTestHandler(t, nil, func(context.Context, auth.AccessContext, int64, int32, string) (forum.EditResult, error) {
		editCalls++
		return forum.EditResult{}, nil
	})
	form := url.Values{
		"_csrf": {validCSRFTokenForTest(0x51)}, "revision": {"3"},
		"markdown": {"Edited **safe** <script>alert(1)</script>"},
	}
	for _, fragment := range []bool{false, true} {
		request := publishingTestRequest(http.MethodPost, "/posts/91/edit/preview", form.Encode(), true)
		if fragment {
			request.Header.Set("HX-Request", "true")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		body := response.Body.String()
		if response.Code != http.StatusOK || editCalls != 0 || strings.HasPrefix(body, "<!doctype html>") == fragment {
			t.Fatalf("edit preview fragment=%t = (status %d edits %d body %q)", fragment, response.Code, editCalls, body)
		}
		for _, required := range []string{`value="3"`, `Edited **safe** &lt;script&gt;alert(1)&lt;/script&gt;`, `<strong>safe</strong>`, `alert(1)`} {
			if !strings.Contains(body, required) {
				t.Fatalf("edit preview lacks %q: %s", required, body)
			}
		}
		if strings.Contains(body, "<script>") {
			t.Fatalf("edit preview contains active script: %s", body)
		}
	}
}

func TestEditingHandlerPreviewPreservesInvalidBlankDraft(t *testing.T) {
	t.Parallel()

	form := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "revision": {"3"}, "markdown": {""}}
	response := httptest.NewRecorder()
	newEditingTestHandler(t, nil, nil).ServeHTTP(response, publishingTestRequest(http.MethodPost, "/posts/91/edit/preview", form.Encode(), true))
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "Check the Markdown body.") ||
		!strings.Contains(response.Body.String(), `name="revision" value="3"`) || !strings.Contains(response.Body.String(), `aria-invalid="true"`) {
		t.Fatalf("blank edit preview = (%d, %q)", response.Code, response.Body.String())
	}
}

func TestEditingHandlerAppliesAndRedirectsToExactPost(t *testing.T) {
	t.Parallel()

	handler := newEditingTestHandler(t, nil, func(_ context.Context, access auth.AccessContext, postID int64, revision int32, markdown string) (forum.EditResult, error) {
		if access.UserID != 42 || postID != 91 || revision != 3 || markdown != "Edited body" {
			t.Fatalf("edit arguments = (%+v, %d, %d, %q)", access, postID, revision, markdown)
		}
		return forum.EditResult{TopicID: 41, PostID: 91, PostNumber: 27, Revision: 4}, nil
	})
	form := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "revision": {"3"}, "markdown": {"Edited body"}}
	for _, fragment := range []bool{false, true} {
		request := publishingTestRequest(http.MethodPost, "/posts/91/edit", form.Encode(), true)
		if fragment {
			request.Header.Set("HX-Request", "true")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if fragment {
			wantLocation := `{"path":"/bb/topics/41?page=2#post-91","target":"#main-content","swap":"outerHTML"}`
			if response.Code != http.StatusNoContent || response.Header().Get("HX-Location") != wantLocation || response.Header().Get("HX-Redirect") != "" || response.Header().Get("Location") != "" {
				t.Fatalf("HTMX edit navigation = (%d, headers %v)", response.Code, response.Header())
			}
		} else if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/bb/topics/41?page=2#post-91" {
			t.Fatalf("edit redirect = (%d, %q)", response.Code, response.Header().Get("Location"))
		}
	}
}

func TestEditingHandlerPreservesValidationAndConflictDrafts(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		markdown   string
		editErr    error
		wantStatus int
		want       string
	}{
		{name: "validation", markdown: "", editErr: forum.InvalidPublishingInput{Field: "markdown"}, wantStatus: http.StatusUnprocessableEntity, want: "Check the Markdown body."},
		{name: "conflict", markdown: "stale draft", editErr: forum.ErrPostEditConflict, wantStatus: http.StatusConflict, want: "This post changed since you opened it."},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := newEditingTestHandler(t, nil, func(context.Context, auth.AccessContext, int64, int32, string) (forum.EditResult, error) {
				return forum.EditResult{}, test.editErr
			})
			form := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "revision": {"3"}, "markdown": {test.markdown}}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, publishingTestRequest(http.MethodPost, "/posts/91/edit", form.Encode(), true))
			body := response.Body.String()
			if response.Code != test.wantStatus || !strings.Contains(body, test.want) || !strings.Contains(body, `name="revision" value="3"`) ||
				!strings.Contains(body, ">"+test.markdown+"</textarea>") {
				t.Fatalf("edit %s = (%d, %q)", test.name, response.Code, body)
			}
		})
	}
}

func TestEditingHandlerFailsClosedAtRequestAndServiceBoundaries(t *testing.T) {
	t.Parallel()

	valid := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "revision": {"3"}, "markdown": {"body"}}.Encode()
	for _, test := range []struct {
		name          string
		method        string
		target        string
		body          string
		authenticated bool
		missingCSRF   bool
		loadErr       error
		editErr       error
		wantStatus    int
		wantLocation  string
	}{
		{name: "anonymous", method: http.MethodGet, target: "/posts/91/edit", wantStatus: http.StatusSeeOther, wantLocation: "/bb/login"},
		{name: "noncanonical", method: http.MethodGet, target: "/posts/091/edit", authenticated: true, wantStatus: http.StatusNotFound},
		{name: "query", method: http.MethodGet, target: "/posts/91/edit?x=1", authenticated: true, wantStatus: http.StatusNotFound},
		{name: "missing", method: http.MethodGet, target: "/posts/91/edit", authenticated: true, loadErr: pgx.ErrNoRows, wantStatus: http.StatusNotFound},
		{name: "preview csrf", method: http.MethodPost, target: "/posts/91/edit/preview", body: "revision=3&markdown=secret", authenticated: true, missingCSRF: true, wantStatus: http.StatusForbidden},
		{name: "denied", method: http.MethodPost, target: "/posts/91/edit", body: valid, authenticated: true, editErr: forum.ErrPostEditDenied, wantStatus: http.StatusForbidden},
		{name: "storage", method: http.MethodPost, target: "/posts/91/edit", body: valid, authenticated: true, editErr: errors.New("database failed"), wantStatus: http.StatusServiceUnavailable},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := newEditingTestHandler(t,
				func(context.Context, auth.AccessContext, int64) (store.EditablePost, error) {
					if test.loadErr != nil {
						return store.EditablePost{}, test.loadErr
					}
					return validEditablePost(), nil
				},
				func(context.Context, auth.AccessContext, int64, int32, string) (forum.EditResult, error) {
					return forum.EditResult{}, test.editErr
				},
			)
			request := publishingTestRequest(test.method, test.target, test.body, test.authenticated)
			if test.missingCSRF {
				request = request.WithContext(context.WithValue(request.Context(), csrfTokenContextKey{}, ""))
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || response.Header().Get("Location") != test.wantLocation || strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("boundary %s = (%d, %q, %q)", test.name, response.Code, response.Header().Get("Location"), response.Body.String())
			}
		})
	}
}

func TestEditingHandlerRevalidatesBeforeLookupOrMutation(t *testing.T) {
	t.Parallel()

	loadCalls, editCalls := 0, 0
	handler := newEditingTestHandler(t,
		func(context.Context, auth.AccessContext, int64) (store.EditablePost, error) {
			loadCalls++
			return validEditablePost(), nil
		},
		func(context.Context, auth.AccessContext, int64, int32, string) (forum.EditResult, error) {
			editCalls++
			return forum.EditResult{}, nil
		},
	)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		body := ""
		if method == http.MethodPost {
			body = url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "revision": {"3"}, "markdown": {"body"}}.Encode()
		}
		request := publishingTestRequest(method, "/posts/91/edit", body, true)
		authentication := sessionAuthenticationFromContext(request.Context())
		authentication.RequiresRevalidation = true
		request = request.WithContext(context.WithValue(request.Context(), sessionAuthenticationContextKey{}, authentication))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/bb/auth/revalidate" {
			t.Fatalf("stale %s edit = (%d, %q)", method, response.Code, response.Header().Get("Location"))
		}
	}
	if loadCalls != 0 || editCalls != 0 {
		t.Fatalf("stale routes called services: loads %d edits %d", loadCalls, editCalls)
	}
}

func TestEditingHandlerRejectsInvalidResultsAndConflictReloadFailure(t *testing.T) {
	t.Parallel()

	validForm := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "revision": {"3"}, "markdown": {"body"}}.Encode()
	invalidResult := newEditingTestHandler(t, nil, func(context.Context, auth.AccessContext, int64, int32, string) (forum.EditResult, error) {
		return forum.EditResult{TopicID: 41, PostID: 91, PostNumber: 2, Revision: 9}, nil
	})
	response := httptest.NewRecorder()
	invalidResult.ServeHTTP(response, publishingTestRequest(http.MethodPost, "/posts/91/edit", validForm, true))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid edit result = (%d, %q)", response.Code, response.Body.String())
	}
	conflict := newEditingTestHandler(t,
		func(context.Context, auth.AccessContext, int64) (store.EditablePost, error) {
			return store.EditablePost{}, pgx.ErrNoRows
		},
		func(context.Context, auth.AccessContext, int64, int32, string) (forum.EditResult, error) {
			return forum.EditResult{}, forum.ErrPostEditConflict
		},
	)
	response = httptest.NewRecorder()
	conflict.ServeHTTP(response, publishingTestRequest(http.MethodPost, "/posts/91/edit", validForm, true))
	if response.Code != http.StatusNotFound {
		t.Fatalf("conflict reload failure = (%d, %q)", response.Code, response.Body.String())
	}
}

func TestEditingHandlerSoftDeletesAndRedirectsToTopic(t *testing.T) {
	t.Parallel()

	deletePost := PostDeleter(func(_ context.Context, access auth.AccessContext, postID int64, revision int32) (forum.DeleteResult, error) {
		if access.UserID != 42 || postID != 91 || revision != 3 {
			t.Fatalf("delete arguments = (%+v, %d, %d)", access, postID, revision)
		}
		return forum.DeleteResult{TopicID: 41, PostID: 91, PostNumber: 27, Revision: 3}, nil
	})
	form := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "revision": {"3"}}.Encode()
	for _, fragment := range []bool{false, true} {
		request := publishingTestRequest(http.MethodPost, "/posts/91/delete", form, true)
		if fragment {
			request.Header.Set("HX-Request", "true")
		}
		response := httptest.NewRecorder()
		newEditingTestHandler(t, nil, nil, deletePost).ServeHTTP(response, request)
		if fragment {
			wantLocation := `{"path":"/bb/topics/41","target":"#main-content","swap":"outerHTML"}`
			if response.Code != http.StatusNoContent || response.Header().Get("HX-Location") != wantLocation || response.Header().Get("HX-Redirect") != "" || response.Header().Get("Location") != "" {
				t.Fatalf("HTMX delete navigation = (%d, headers %v)", response.Code, response.Header())
			}
		} else if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/bb/topics/41" {
			t.Fatalf("delete redirect = (%d, %q)", response.Code, response.Header().Get("Location"))
		}
	}
}

func TestEditingHandlerSoftDeleteFailsClosed(t *testing.T) {
	t.Parallel()

	valid := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "revision": {"3"}}.Encode()
	for _, test := range []struct {
		name        string
		body        string
		missingCSRF bool
		deleteErr   error
		result      forum.DeleteResult
		wantStatus  int
	}{
		{name: "csrf", body: "revision=3", missingCSRF: true, wantStatus: http.StatusForbidden},
		{name: "malformed", body: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "revision": {"03"}}.Encode(), wantStatus: http.StatusBadRequest},
		{name: "denied", body: valid, deleteErr: forum.ErrPostDeleteDenied, wantStatus: http.StatusForbidden},
		{name: "conflict", body: valid, deleteErr: forum.ErrPostDeleteConflict, wantStatus: http.StatusConflict},
		{name: "storage", body: valid, deleteErr: errors.New("database failed"), wantStatus: http.StatusServiceUnavailable},
		{name: "invalid result", body: valid, result: forum.DeleteResult{TopicID: 41, PostID: 91, PostNumber: 2, Revision: 4}, wantStatus: http.StatusServiceUnavailable},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			deletePost := PostDeleter(func(context.Context, auth.AccessContext, int64, int32) (forum.DeleteResult, error) {
				return test.result, test.deleteErr
			})
			request := publishingTestRequest(http.MethodPost, "/posts/91/delete", test.body, true)
			if test.missingCSRF {
				request = request.WithContext(context.WithValue(request.Context(), csrfTokenContextKey{}, ""))
			}
			response := httptest.NewRecorder()
			newEditingTestHandler(t, nil, nil, deletePost).ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("delete %s = (%d, %q)", test.name, response.Code, response.Body.String())
			}
		})
	}
}

func TestParseEditFormRejectsNoncanonicalAndAmbiguousFields(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		"markdown=body", "revision=3", "revision=03&markdown=body", "revision=0&markdown=body",
		"revision=2147483647&markdown=body", "revision=2147483648&markdown=body", "revision=3&revision=4&markdown=body",
		"revision=3&markdown=body&extra=x",
	} {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if markdown, revision, err := parseEditForm(request); err == nil || markdown != "" || revision != 0 {
			t.Fatalf("parseEditForm(%q) = (%q, %d, %v)", body, markdown, revision, err)
		}
	}
}

func TestParseEditFormPreservesReadFailure(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Body = failingPublishingBody{}
	if markdown, revision, err := parseEditForm(request); err == nil || markdown != "" || revision != 0 {
		t.Fatalf("parseEditForm(read failure) = (%q, %d, %v)", markdown, revision, err)
	}
}

func TestParseDeleteFormAcceptsMaxAndRejectsMalformedFields(t *testing.T) {
	t.Parallel()

	valid := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("_csrf=x&revision=2147483647"))
	valid.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if revision, err := parseDeleteForm(valid); err != nil || revision != int32(1<<31-1) {
		t.Fatalf("parseDeleteForm(max) = (%d, %v)", revision, err)
	}
	for _, body := range []string{"_csrf=x", "_csrf=x&revision=0", "_csrf=x&revision=03", "_csrf=x&revision=3&markdown=x", "_csrf=x&revision=3&revision=4"} {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if revision, err := parseDeleteForm(request); err == nil || revision != 0 {
			t.Fatalf("parseDeleteForm(%q) = (%d, %v)", body, revision, err)
		}
	}
}

func TestParseDeleteFormPreservesReadFailure(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Body = failingPublishingBody{}
	if revision, err := parseDeleteForm(request); err == nil || revision != 0 {
		t.Fatalf("parseDeleteForm(read failure) = (%d, %v)", revision, err)
	}
}

func TestNewEditingHandlerRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	validLoader := EditablePostLoader(func(context.Context, auth.AccessContext, int64) (store.EditablePost, error) {
		return validEditablePost(), nil
	})
	validEditor := PostEditor(func(context.Context, auth.AccessContext, int64, int32, string) (forum.EditResult, error) {
		return forum.EditResult{}, nil
	})
	validDeleter := PostDeleter(func(context.Context, auth.AccessContext, int64, int32) (forum.DeleteResult, error) {
		return forum.DeleteResult{}, nil
	})
	for _, test := range []struct {
		builder URLBuilder
		loader  EditablePostLoader
		editor  PostEditor
		deleter PostDeleter
	}{
		{builder: callbackTestURLBuilder(t), editor: validEditor, deleter: validDeleter},
		{builder: callbackTestURLBuilder(t), loader: validLoader, deleter: validDeleter},
		{builder: callbackTestURLBuilder(t), loader: validLoader, editor: validEditor},
		{loader: validLoader, editor: validEditor, deleter: validDeleter},
	} {
		if handler, err := newEditingHandler(test.builder, test.loader, test.editor, test.deleter); err == nil || handler != nil {
			t.Fatalf("newEditingHandler(missing) = (%v, %v)", handler, err)
		}
	}
}

func newEditingTestHandler(t *testing.T, load EditablePostLoader, edit PostEditor, deleters ...PostDeleter) http.Handler {
	t.Helper()
	if load == nil {
		load = func(context.Context, auth.AccessContext, int64) (store.EditablePost, error) {
			return validEditablePost(), nil
		}
	}
	if edit == nil {
		edit = func(context.Context, auth.AccessContext, int64, int32, string) (forum.EditResult, error) {
			panic("post edit is not expected")
		}
	}
	deletePost := PostDeleter(func(context.Context, auth.AccessContext, int64, int32) (forum.DeleteResult, error) {
		panic("post delete is not expected")
	})
	if len(deleters) == 1 && deleters[0] != nil {
		deletePost = deleters[0]
	}
	handler, err := newEditingHandler(callbackTestURLBuilder(t), load, edit, deletePost)
	if err != nil {
		t.Fatalf("newEditingHandler() returned error: %v", err)
	}
	return handler
}

func validEditablePost() store.EditablePost {
	return store.EditablePost{PostID: 91, TopicID: 41, PostNumber: 2, MarkdownSource: "original", Revision: 3}
}
