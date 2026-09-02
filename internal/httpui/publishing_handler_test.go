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
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/forum"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
)

func TestAuthenticatedPublishingRouterLoadsSessionOnlyForCanonicalRoutes(t *testing.T) {
	t.Parallel()

	builder := callbackTestURLBuilder(t)
	sessionToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x61}, 32))
	csrfToken, err := deriveCSRFToken(sessionToken)
	if err != nil {
		t.Fatalf("deriveCSRFToken() returned error: %v", err)
	}
	service := &authenticatedHandlerTestService{}
	service.authenticate = func(context.Context, string) (auth.SessionAuthentication, error) {
		service.authenticateCalls++
		return auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleMember}}, nil
	}
	topicCalls := 0
	handler, err := NewAuthenticatedPublishingHandler(
		builder, service, emptyAreaIndexLister,
		func(context.Context, auth.AccessContext, string, int32) (store.VisibleAreaTopicPage, error) {
			panic("area topics not expected")
		}, store.MaximumTopicPage,
		func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
			panic("topic posts not expected")
		}, store.MaximumPostPage,
		func(context.Context, auth.AccessContext, string, string, string) (forum.PublishResult, error) {
			topicCalls++
			return forum.PublishResult{TopicID: 41, PostID: 91, PostNumber: 1}, nil
		},
		func(context.Context, auth.AccessContext, int64, int64, string) (forum.PublishResult, error) {
			panic("reply not expected")
		},
		"gotth_bb_session", true,
	)
	if err != nil {
		t.Fatalf("NewAuthenticatedPublishingHandler() returned error: %v", err)
	}
	getRequest := httptest.NewRequest(http.MethodGet, "/topics/new?area=news", nil)
	getRequest.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusOK || service.authenticateCalls != 1 {
		t.Fatalf("authenticated GET = (status %d auth calls %d)", getResponse.Code, service.authenticateCalls)
	}
	malformed := httptest.NewRequest(http.MethodPost, "/topics/041/replies", strings.NewReader("markdown=body"))
	malformed.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	malformedResponse := httptest.NewRecorder()
	handler.ServeHTTP(malformedResponse, malformed)
	if malformedResponse.Code != http.StatusNotFound || service.authenticateCalls != 1 {
		t.Fatalf("malformed reply = (status %d auth calls %d)", malformedResponse.Code, service.authenticateCalls)
	}
	malformedPreview := httptest.NewRequest(http.MethodPost, "/topics/041/replies/preview", strings.NewReader("markdown=body"))
	malformedPreview.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	malformedPreviewResponse := httptest.NewRecorder()
	handler.ServeHTTP(malformedPreviewResponse, malformedPreview)
	if malformedPreviewResponse.Code != http.StatusNotFound || service.authenticateCalls != 1 {
		t.Fatalf("malformed reply preview = (status %d auth calls %d)", malformedPreviewResponse.Code, service.authenticateCalls)
	}
	form := url.Values{"_csrf": {csrfToken}, "area": {"news"}, "title": {"Title"}, "markdown": {"body"}}
	previewRequest := httptest.NewRequest(http.MethodPost, "/topics/preview", strings.NewReader(form.Encode()))
	previewRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	previewRequest.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	previewResponse := httptest.NewRecorder()
	handler.ServeHTTP(previewResponse, previewRequest)
	if previewResponse.Code != http.StatusOK || service.authenticateCalls != 2 || topicCalls != 0 {
		t.Fatalf("authenticated preview = (status %d auth calls %d topic calls %d body %q)", previewResponse.Code, service.authenticateCalls, topicCalls, previewResponse.Body.String())
	}
	replyPreviewForm := url.Values{"_csrf": {csrfToken}, "parent_post_id": {"91"}, "markdown": {"reply body"}}
	replyPreviewRequest := httptest.NewRequest(http.MethodPost, "/topics/41/replies/preview", strings.NewReader(replyPreviewForm.Encode()))
	replyPreviewRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	replyPreviewRequest.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	replyPreviewResponse := httptest.NewRecorder()
	handler.ServeHTTP(replyPreviewResponse, replyPreviewRequest)
	if replyPreviewResponse.Code != http.StatusOK || service.authenticateCalls != 3 || topicCalls != 0 {
		t.Fatalf("authenticated reply preview = (status %d auth calls %d topic calls %d body %q)", replyPreviewResponse.Code, service.authenticateCalls, topicCalls, replyPreviewResponse.Body.String())
	}
	postRequest := httptest.NewRequest(http.MethodPost, "/topics", strings.NewReader(form.Encode()))
	postRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postRequest.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	postResponse := httptest.NewRecorder()
	handler.ServeHTTP(postResponse, postRequest)
	if postResponse.Code != http.StatusSeeOther || service.authenticateCalls != 4 || topicCalls != 1 {
		t.Fatalf("authenticated POST = (status %d auth calls %d topic calls %d body %q)", postResponse.Code, service.authenticateCalls, topicCalls, postResponse.Body.String())
	}
}

func TestNewAuthenticatedPublishingHandlerRejectsMissingPublishers(t *testing.T) {
	t.Parallel()

	builder := callbackTestURLBuilder(t)
	service := &authenticatedHandlerTestService{}
	list := func(context.Context, auth.AccessContext) ([]store.VisibleAreaSummary, error) { return nil, nil }
	topics := func(context.Context, auth.AccessContext, string, int32) (store.VisibleAreaTopicPage, error) {
		return store.VisibleAreaTopicPage{}, nil
	}
	posts := func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
		return store.VisibleTopicPostPage{}, nil
	}
	validTopic := TopicPublisher(func(context.Context, auth.AccessContext, string, string, string) (forum.PublishResult, error) {
		return forum.PublishResult{}, nil
	})
	validReply := ReplyPublisher(func(context.Context, auth.AccessContext, int64, int64, string) (forum.PublishResult, error) {
		return forum.PublishResult{}, nil
	})
	for _, publishers := range []struct {
		topic TopicPublisher
		reply ReplyPublisher
	}{{reply: validReply}, {topic: validTopic}} {
		if handler, err := NewAuthenticatedPublishingHandler(builder, service, list, topics, store.MaximumTopicPage, posts, store.MaximumPostPage, publishers.topic, publishers.reply, "gotth_bb_session", true); err == nil || handler != nil {
			t.Fatalf("missing publishers returned (%v, %v)", handler, err)
		}
	}
}

func TestNewPublishingHandlerRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	builder := callbackTestURLBuilder(t)
	validTopic := TopicPublisher(func(context.Context, auth.AccessContext, string, string, string) (forum.PublishResult, error) {
		return forum.PublishResult{}, nil
	})
	validReply := ReplyPublisher(func(context.Context, auth.AccessContext, int64, int64, string) (forum.PublishResult, error) {
		return forum.PublishResult{}, nil
	})
	for _, test := range []struct {
		builder URLBuilder
		topic   TopicPublisher
		reply   ReplyPublisher
	}{
		{builder: builder, reply: validReply},
		{builder: builder, topic: validTopic},
		{topic: validTopic, reply: validReply},
	} {
		if handler, err := newPublishingHandler(test.builder, test.topic, test.reply); err == nil || handler != nil {
			t.Fatalf("newPublishingHandler(missing) = (%v, %v)", handler, err)
		}
	}
}

func TestPublishingHandlerRendersCanonicalNewTopicForm(t *testing.T) {
	t.Parallel()

	handler := newPublishingTestHandler(t, nil, nil)
	request := publishingTestRequest(http.MethodGet, "/topics/new?area=member-news", "", true)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || request.Pattern != "GET /topics/new" {
		t.Fatalf("new-topic response = (status %d pattern %q body %q)", response.Code, request.Pattern, body)
	}
	for _, required := range []string{
		`action="/bb/topics"`, `name="area" value="member-news"`, `name="_csrf"`,
		`name="title"`, `name="markdown"`, `maxlength="200"`, `maxlength="65536"`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("new-topic form lacks %q: %s", required, body)
		}
	}
}

func TestReadablePagesExposeOnlyEligiblePublishingActions(t *testing.T) {
	t.Parallel()

	builder := areaTopicTestBuilder(t)
	areaHandler, err := newAreaTopicListHandler(builder, store.MaximumTopicPage, func(context.Context, auth.AccessContext, string, int32) (store.VisibleAreaTopicPage, error) {
		return store.VisibleAreaTopicPage{Area: db.Area{ID: 7, Slug: "public", Name: "Public", Visibility: "public", PostingMode: "normal"}, Number: 1}, nil
	})
	if err != nil {
		t.Fatalf("newAreaTopicListHandler() returned error: %v", err)
	}
	areaRequest := areaTopicTestRequest("/areas/public", "public", auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleMember})
	areaResponse := httptest.NewRecorder()
	areaHandler.ServeHTTP(areaResponse, areaRequest)
	if areaResponse.Code != http.StatusOK || !strings.Contains(areaResponse.Body.String(), `href="/bb/topics/new?area=public"`) {
		t.Fatalf("eligible area page = (%d, %q)", areaResponse.Code, areaResponse.Body.String())
	}

	topicHandler, err := newTopicPostListHandler(builder, store.MaximumPostPage, func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
		page := topicPostTestPage(1)
		for index := range page.Rows {
			page.Rows[index].AreaPostingMode = "normal"
			page.Rows[index].TopicState = "open"
		}
		return page, nil
	})
	if err != nil {
		t.Fatalf("newTopicPostListHandler() returned error: %v", err)
	}
	topicRequest := topicPostTestRequest("/topics/42", "42", auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleMember})
	topicRequest = topicRequest.WithContext(context.WithValue(topicRequest.Context(), csrfTokenContextKey{}, validCSRFTokenForTest(0x51)))
	topicResponse := httptest.NewRecorder()
	topicHandler.ServeHTTP(topicResponse, topicRequest)
	if topicResponse.Code != http.StatusOK || !strings.Contains(topicResponse.Body.String(), `action="/bb/topics/42/replies"`) ||
		!strings.Contains(topicResponse.Body.String(), `formaction="/bb/topics/42/replies/preview"`) ||
		!strings.Contains(topicResponse.Body.String(), `name="parent_post_id" value="101"`) ||
		!strings.Contains(topicResponse.Body.String(), `action="/bb/posts/126/delete"`) ||
		!strings.Contains(topicResponse.Body.String(), `name="revision" value="2"`) ||
		!strings.Contains(topicResponse.Body.String(), `name="_csrf" value="`+validCSRFTokenForTest(0x51)+`"`) {
		t.Fatalf("eligible topic page = (%d, %q)", topicResponse.Code, topicResponse.Body.String())
	}

	staffHandler, err := newTopicPostListHandler(builder, store.MaximumPostPage, func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
		page := topicPostTestPage(1)
		for index := range page.Rows {
			page.Rows[index].AreaPostingMode = "read_only"
			page.Rows[index].TopicState = "locked"
		}
		return page, nil
	})
	if err != nil {
		t.Fatalf("newTopicPostListHandler(staff) returned error: %v", err)
	}
	staffRequest := topicPostTestRequest("/topics/42", "42", auth.AccessContext{Authenticated: true, UserID: 7, Role: auth.RoleAdministrator})
	staffRequest = staffRequest.WithContext(context.WithValue(staffRequest.Context(), csrfTokenContextKey{}, validCSRFTokenForTest(0x52)))
	staffResponse := httptest.NewRecorder()
	staffHandler.ServeHTTP(staffResponse, staffRequest)
	if staffResponse.Code != http.StatusOK || !strings.Contains(staffResponse.Body.String(), `action="/bb/topics/42/replies"`) ||
		strings.Contains(staffResponse.Body.String(), `href="/bb/posts/126/edit"`) || strings.Contains(staffResponse.Body.String(), `action="/bb/posts/126/delete"`) {
		t.Fatalf("eligible staff topic page = (%d, %q)", staffResponse.Code, staffResponse.Body.String())
	}
}

func TestReadablePagesHideIneligiblePublishingActions(t *testing.T) {
	t.Parallel()

	builder := areaTopicTestBuilder(t)
	mutedUntil := time.Date(2026, time.September, 3, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name        string
		access      auth.AccessContext
		postingMode string
		topicState  string
	}{
		{name: "anonymous", postingMode: "normal", topicState: "open"},
		{name: "suspended", access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleMember, Suspended: true}, postingMode: "normal", topicState: "open"},
		{name: "muted", access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleMember, MutedUntil: &mutedUntil}, postingMode: "normal", topicState: "open"},
		{name: "member read only", access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleMember}, postingMode: "read_only", topicState: "open"},
		{name: "member locked", access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleMember}, postingMode: "normal", topicState: "locked"},
		{name: "administrator archived area", access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleAdministrator}, postingMode: "archived", topicState: "open"},
		{name: "administrator archived topic", access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleAdministrator}, postingMode: "normal", topicState: "archived"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			topicHandler, err := newTopicPostListHandler(builder, store.MaximumPostPage, func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
				page := topicPostTestPage(1)
				for index := range page.Rows {
					page.Rows[index].AreaPostingMode = test.postingMode
					page.Rows[index].TopicState = test.topicState
				}
				return page, nil
			})
			if err != nil {
				t.Fatalf("newTopicPostListHandler() returned error: %v", err)
			}
			request := topicPostTestRequest("/topics/42", "42", test.access)
			request = request.WithContext(context.WithValue(request.Context(), csrfTokenContextKey{}, validCSRFTokenForTest(0x51)))
			response := httptest.NewRecorder()
			topicHandler.ServeHTTP(response, request)
			body := response.Body.String()
			if response.Code != http.StatusOK || strings.Contains(body, `action="/bb/topics/42/replies"`) ||
				(test.name == "anonymous" || test.name == "suspended" || test.name == "muted") &&
					(strings.Contains(body, `href="/bb/posts/126/edit"`) || strings.Contains(body, `action="/bb/posts/126/delete"`)) {
				t.Fatalf("ineligible topic page = (%d, %q)", response.Code, response.Body.String())
			}
		})
	}
}

func TestPublishingHandlerCreatesTopicAndRedirectsCanonically(t *testing.T) {
	t.Parallel()

	calls := 0
	handler := newPublishingTestHandler(t, func(_ context.Context, access auth.AccessContext, area, title, markdown string) (forum.PublishResult, error) {
		calls++
		if access.UserID != 42 || area != "member-news" || title != "Careful title" || markdown != "Hello **world**" {
			t.Fatalf("topic publishing input = (%+v, %q, %q, %q)", access, area, title, markdown)
		}
		return forum.PublishResult{TopicID: 41, PostID: 91, PostNumber: 1}, nil
	}, nil)
	form := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "area": {"member-news"}, "title": {"Careful title"}, "markdown": {"Hello **world**"}}
	request := publishingTestRequest(http.MethodPost, "/topics", form.Encode(), true)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/bb/topics/41" || calls != 1 || response.Body.Len() != 0 {
		t.Fatalf("topic response = (status %d location %q calls %d body %q)", response.Code, response.Header().Get("Location"), calls, response.Body.String())
	}
}

func TestPublishingHandlerCreatesReplyAndRedirectsToExactPostPage(t *testing.T) {
	t.Parallel()

	handler := newPublishingTestHandler(t, nil, func(_ context.Context, access auth.AccessContext, topicID, parentPostID int64, markdown string) (forum.PublishResult, error) {
		if access.UserID != 42 || topicID != 41 || parentPostID != 91 || markdown != "Reply body" {
			t.Fatalf("reply publishing input = (%+v, %d, %d, %q)", access, topicID, parentPostID, markdown)
		}
		return forum.PublishResult{TopicID: 41, PostID: 116, PostNumber: 26}, nil
	})
	form := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "parent_post_id": {"91"}, "markdown": {"Reply body"}}
	request := publishingTestRequest(http.MethodPost, "/topics/41/replies", form.Encode(), true)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/bb/topics/41?page=2#post-116" || response.Body.Len() != 0 {
		t.Fatalf("reply response = (status %d location %q body %q)", response.Code, response.Header().Get("Location"), response.Body.String())
	}
}

func TestPublishingHandlerUsesHTMXLocationWithoutDocumentReload(t *testing.T) {
	t.Parallel()

	handler := newPublishingTestHandler(t, func(context.Context, auth.AccessContext, string, string, string) (forum.PublishResult, error) {
		return forum.PublishResult{TopicID: 41, PostID: 91, PostNumber: 1}, nil
	}, nil)
	form := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "area": {"news"}, "title": {"Title"}, "markdown": {"body"}}
	request := publishingTestRequest(http.MethodPost, "/topics", form.Encode(), true)
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	wantLocation := `{"path":"/bb/topics/41","target":"#main-content","swap":"outerHTML"}`
	if response.Code != http.StatusNoContent || response.Header().Get("HX-Location") != wantLocation || response.Header().Get("HX-Redirect") != "" || response.Header().Get("Location") != "" || response.Body.Len() != 0 {
		t.Fatalf("HTMX navigation = (status %d HX-Location %q HX-Redirect %q Location %q body %q)", response.Code, response.Header().Get("HX-Location"), response.Header().Get("HX-Redirect"), response.Header().Get("Location"), response.Body.String())
	}
}

func TestPublishingValidationPreservesSubmittedFields(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		target     string
		form       url.Values
		topicError string
		replyError string
		want       []string
	}{
		{
			name: "topic title", target: "/topics",
			form:       url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "area": {"member-news"}, "title": {"<bad>"}, "markdown": {"kept **body**"}},
			topicError: "title", want: []string{`value="&lt;bad&gt;"`, `kept **body**`, `Check the topic title.`},
		},
		{
			name: "reply markdown", target: "/topics/41/replies",
			form:       url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "parent_post_id": {"91"}, "markdown": {"kept <script>body</script>"}},
			replyError: "markdown", want: []string{`kept &lt;script&gt;body&lt;/script&gt;`, `Check the Markdown body.`},
		},
		{
			name: "topic area", target: "/topics",
			form:       url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "area": {"bad area"}, "title": {"kept title"}, "markdown": {"kept body"}},
			topicError: "area", want: []string{`name="area" value="bad area"`, `kept title`, `kept body`, `Check the submitted fields.`},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			for _, fragment := range []bool{false, true} {
				fragment := fragment
				t.Run(map[bool]string{false: "page", true: "fragment"}[fragment], func(t *testing.T) {
					t.Parallel()
					handler := newPublishingTestHandler(t,
						func(context.Context, auth.AccessContext, string, string, string) (forum.PublishResult, error) {
							return forum.PublishResult{}, forum.InvalidPublishingInput{Field: test.topicError}
						},
						func(context.Context, auth.AccessContext, int64, int64, string) (forum.PublishResult, error) {
							return forum.PublishResult{}, forum.InvalidPublishingInput{Field: test.replyError}
						},
					)
					request := publishingTestRequest(http.MethodPost, test.target, test.form.Encode(), true)
					if fragment {
						request.Header.Set("HX-Request", "true")
					}
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, request)
					isPage := strings.HasPrefix(response.Body.String(), "<!doctype html>")
					if response.Code != http.StatusUnprocessableEntity || isPage == fragment {
						t.Fatalf("validation response = (%d, page %t, %q)", response.Code, isPage, response.Body.String())
					}
					for _, required := range test.want {
						if !strings.Contains(response.Body.String(), required) {
							t.Fatalf("validation response lacks %q: %s", required, response.Body.String())
						}
					}
				})
			}
		})
	}
}

func TestPublishingHandlerFailsClosedBeforeMutation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		target        string
		body          string
		authenticated bool
		creatorError  error
		wantStatus    int
	}{
		{name: "unauthenticated", target: "/topics", authenticated: false, wantStatus: http.StatusSeeOther},
		{name: "missing CSRF", target: "/topics", authenticated: true, body: "area=news&title=Title&markdown=body", wantStatus: http.StatusForbidden},
		{name: "unknown field", target: "/topics", authenticated: true, body: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "area": {"news"}, "title": {"Title"}, "markdown": {"body"}, "extra": {"x"}}.Encode(), wantStatus: http.StatusBadRequest},
		{name: "topic query", target: "/topics?extra=1", authenticated: true, body: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "area": {"news"}, "title": {"Title"}, "markdown": {"body"}}.Encode(), wantStatus: http.StatusBadRequest},
		{name: "reply missing CSRF", target: "/topics/41/replies", authenticated: true, body: "markdown=body", wantStatus: http.StatusForbidden},
		{name: "reply unknown field", target: "/topics/41/replies", authenticated: true, body: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "parent_post_id": {"91"}, "markdown": {"body"}, "extra": {"x"}}.Encode(), wantStatus: http.StatusBadRequest},
		{name: "reply missing parent", target: "/topics/41/replies", authenticated: true, body: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "markdown": {"body"}}.Encode(), wantStatus: http.StatusBadRequest},
		{name: "reply noncanonical parent", target: "/topics/41/replies", authenticated: true, body: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "parent_post_id": {"091"}, "markdown": {"body"}}.Encode(), wantStatus: http.StatusBadRequest},
		{name: "denied", target: "/topics", authenticated: true, body: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "area": {"news"}, "title": {"Title"}, "markdown": {"body"}}.Encode(), creatorError: forum.ErrPublishingDenied, wantStatus: http.StatusForbidden},
		{name: "unavailable", target: "/topics", authenticated: true, body: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "area": {"news"}, "title": {"Title"}, "markdown": {"body"}}.Encode(), creatorError: errors.New("database unavailable"), wantStatus: http.StatusServiceUnavailable},
		{name: "noncanonical reply", target: "/topics/041/replies", authenticated: true, body: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "markdown": {"body"}}.Encode(), wantStatus: http.StatusNotFound},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			handler := newPublishingTestHandler(t, func(context.Context, auth.AccessContext, string, string, string) (forum.PublishResult, error) {
				calls++
				return forum.PublishResult{}, test.creatorError
			}, func(context.Context, auth.AccessContext, int64, int64, string) (forum.PublishResult, error) {
				calls++
				return forum.PublishResult{}, test.creatorError
			})
			request := publishingTestRequest(http.MethodPost, test.target, test.body, test.authenticated)
			if test.name == "missing CSRF" || test.name == "reply missing CSRF" {
				request = request.WithContext(context.WithValue(request.Context(), csrfTokenContextKey{}, ""))
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			wantCalls := 0
			if test.creatorError != nil {
				wantCalls = 1
			}
			if response.Code != test.wantStatus || calls != wantCalls || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("failure = (status %d calls %d cache %q body %q)", response.Code, calls, response.Header().Get("Cache-Control"), response.Body.String())
			}
			if test.name == "unauthenticated" && response.Header().Get("Location") != "/bb/login" {
				t.Fatalf("unauthenticated redirect = %q", response.Header().Get("Location"))
			}
		})
	}
}

func TestPublishingHandlerRedirectsUnauthenticatedHTMXToLogin(t *testing.T) {
	t.Parallel()

	handler := newPublishingTestHandler(t, nil, nil)
	request := publishingTestRequest(http.MethodGet, "/topics/new?area=news", "", false)
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("HX-Redirect") != "/bb/login" || response.Header().Get("Location") != "" {
		t.Fatalf("unauthenticated HTMX redirect = (status %d HX-Redirect %q Location %q)", response.Code, response.Header().Get("HX-Redirect"), response.Header().Get("Location"))
	}
}

func TestPublishingHandlerRevalidatesStaleSessionBeforeMutation(t *testing.T) {
	t.Parallel()

	handler := newPublishingTestHandler(t, nil, nil)
	for _, fragment := range []bool{false, true} {
		request := publishingTestRequest(http.MethodPost, "/topics", "", true)
		authentication := sessionAuthenticationFromContext(request.Context())
		authentication.RequiresRevalidation = true
		request = request.WithContext(context.WithValue(request.Context(), sessionAuthenticationContextKey{}, authentication))
		if fragment {
			request.Header.Set("HX-Request", "true")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		wantStatus, wantHeader := http.StatusSeeOther, "Location"
		if fragment {
			wantStatus, wantHeader = http.StatusNoContent, "HX-Redirect"
		}
		if response.Code != wantStatus || response.Header().Get(wantHeader) != "/bb/auth/revalidate" {
			t.Fatalf("stale session fragment=%t = (status %d %s %q)", fragment, response.Code, wantHeader, response.Header().Get(wantHeader))
		}
	}
}

func TestPublishingHandlerRejectsMalformedNewTopicQueries(t *testing.T) {
	t.Parallel()

	handler := newPublishingTestHandler(t, nil, nil)
	for _, target := range []string{"/topics/new", "/topics/new?area=", "/topics/new?area=News", "/topics/new?area=news&area=other", "/topics/new?other=news", "/topics/new?area=news%2fother"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, publishingTestRequest(http.MethodGet, target, "", true))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("GET %q status = %d, want 400", target, response.Code)
		}
	}
}

func TestPublishingHandlerFailsClosedWithoutFormCSRFOrValidResult(t *testing.T) {
	t.Parallel()

	handler := newPublishingTestHandler(t, func(context.Context, auth.AccessContext, string, string, string) (forum.PublishResult, error) {
		return forum.PublishResult{}, nil
	}, nil)
	form := url.Values{"area": {"news"}, "title": {"Title"}, "markdown": {"body"}}
	request := publishingTestRequest(http.MethodPost, "/topics", form.Encode(), true)
	request.Header.Set(csrfHeaderName, validCSRFTokenForTest(0x51))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid topic result status = %d, want 503", response.Code)
	}

	missingToken := publishingTestRequest(http.MethodGet, "/topics/new?area=news", "", true)
	missingToken = missingToken.WithContext(context.WithValue(missingToken.Context(), csrfTokenContextKey{}, ""))
	missingResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingResponse, missingToken)
	if missingResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing form token status = %d, want 503", missingResponse.Code)
	}
}

func TestParsePublishingFormRejectsMissingFieldsAndReadFailure(t *testing.T) {
	t.Parallel()

	missing := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("markdown=body"))
	missing.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if form, err := parsePublishingForm(missing, false); err == nil || form != (publishingFormView{}) {
		t.Fatalf("missing topic fields = (%+v, %v)", form, err)
	}
	failing := httptest.NewRequest(http.MethodPost, "/", nil)
	failing.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	failing.Body = failingPublishingBody{}
	if form, err := parsePublishingForm(failing, true); err == nil || form != (publishingFormView{}) {
		t.Fatalf("failed form read = (%+v, %v)", form, err)
	}
}

func TestPublishingHandlerMapsMissingTargetsAndInvalidResults(t *testing.T) {
	t.Parallel()

	form := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "parent_post_id": {"91"}, "markdown": {"body"}}
	for _, test := range []struct {
		name       string
		result     forum.PublishResult
		err        error
		wantStatus int
	}{
		{name: "missing", err: pgx.ErrNoRows, wantStatus: http.StatusNotFound},
		{name: "invalid result", result: forum.PublishResult{TopicID: 41, PostID: 0, PostNumber: 2}, wantStatus: http.StatusServiceUnavailable},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := newPublishingTestHandler(t, nil, func(context.Context, auth.AccessContext, int64, int64, string) (forum.PublishResult, error) {
				return test.result, test.err
			})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, publishingTestRequest(http.MethodPost, "/topics/41/replies", form.Encode(), true))
			if response.Code != test.wantStatus {
				t.Fatalf("mapped status = %d, want %d", response.Code, test.wantStatus)
			}
		})
	}
}

func newPublishingTestHandler(t *testing.T, createTopic TopicPublisher, createReply ReplyPublisher) http.Handler {
	t.Helper()
	if createTopic == nil {
		createTopic = func(context.Context, auth.AccessContext, string, string, string) (forum.PublishResult, error) {
			panic("topic creation is not expected")
		}
	}
	if createReply == nil {
		createReply = func(context.Context, auth.AccessContext, int64, int64, string) (forum.PublishResult, error) {
			panic("reply creation is not expected")
		}
	}
	handler, err := newPublishingHandler(callbackTestURLBuilder(t), createTopic, createReply)
	if err != nil {
		t.Fatalf("newPublishingHandler() returned error: %v", err)
	}
	return handler
}

func publishingTestRequest(method, target, body string, authenticated bool) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	access := auth.AccessContext{}
	if authenticated {
		access = auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleMember}
	}
	ctx := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, auth.SessionAuthentication{SessionID: 7, Access: access})
	ctx = context.WithValue(ctx, csrfTokenContextKey{}, validCSRFTokenForTest(0x51))
	return request.WithContext(ctx)
}

type failingPublishingBody struct{}

func (failingPublishingBody) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (failingPublishingBody) Close() error             { return nil }
