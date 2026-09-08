package httpui

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/abuse"
	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/forum"
	"github.com/gotthboard/gotth-bb/internal/observability"
	"github.com/gotthboard/gotth-bb/internal/site"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestPublishingDestinationRejectionsPreserveDraftAndObserveOnce(t *testing.T) {
	policy := blockedHTTPDestinationPolicy(t)
	source := "draft <private> [link](https://blocked.example/path)"
	for _, test := range []struct {
		name    string
		target  string
		form    url.Values
		route   abuse.RejectionRoute
		preview bool
		topic   bool
	}{
		{name: "topic preview", target: "/topics/preview", form: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "area": {"news"}, "title": {"Title"}, "markdown": {source}}, route: abuse.RouteTopicPublication, preview: true, topic: true},
		{name: "reply preview", target: "/topics/41/replies/preview", form: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "parent_post_id": {"91"}, "markdown": {source}}, route: abuse.RouteReplyPublication, preview: true},
		{name: "topic mutation", target: "/topics", form: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "area": {"news"}, "title": {"Title"}, "markdown": {source}}, route: abuse.RouteTopicPublication, topic: true},
		{name: "reply mutation", target: "/topics/41/replies", form: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "parent_post_id": {"91"}, "markdown": {source}}, route: abuse.RouteReplyPublication},
	} {
		for _, htmx := range []bool{false, true} {
			observer := &captureAbuseObserver{}
			topicCalls, replyCalls := 0, 0
			topic := TopicPublisher(func(context.Context, auth.AccessContext, string, string, string) (forum.PublishResult, error) {
				topicCalls++
				return forum.PublishResult{}, forum.BlockedDestinationError{}
			})
			reply := ReplyPublisher(func(context.Context, auth.AccessContext, int64, int64, string) (forum.PublishResult, error) {
				replyCalls++
				return forum.PublishResult{}, forum.BlockedDestinationError{}
			})
			handler, err := newPublishingHandler(callbackTestURLBuilder(t), policy, observer, topic, reply)
			if err != nil {
				t.Fatalf("newPublishingHandler() returned error: %v", err)
			}
			handler = withModerationTestRequestID(t, handler)
			request := publishingTestRequest(http.MethodPost, test.target, test.form.Encode(), true)
			if htmx {
				request.Header.Set("HX-Request", "true")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body := response.Body.String()
			wantTopicCalls, wantReplyCalls := 0, 0
			if !test.preview && test.topic {
				wantTopicCalls = 1
			} else if !test.preview {
				wantReplyCalls = 1
			}
			if response.Code != http.StatusUnprocessableEntity || response.Header().Get("Cache-Control") != "private, no-store" ||
				!strings.Contains(body, "This draft contains a blocked link") || !strings.Contains(body, "draft &lt;private&gt;") ||
				strings.Contains(body, "<private>") || topicCalls != wantTopicCalls || replyCalls != wantReplyCalls {
				t.Fatalf("%s HTMX=%t = status %d cache %q calls %d/%d body %q", test.name, htmx, response.Code, response.Header().Get("Cache-Control"), topicCalls, replyCalls, body)
			}
			assertAbuseEvent(t, observer.events, abuse.RejectionBlocked, test.route, http.StatusUnprocessableEntity, 0)
		}
	}
}

func TestPublicationRateRejectionsPreserveDraftAndRetry(t *testing.T) {
	for _, test := range []struct {
		name, target string
		form         url.Values
		route        abuse.RejectionRoute
		topic        bool
	}{
		{name: "topic", target: "/topics", form: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "area": {"news"}, "title": {"Title"}, "markdown": {"retained topic"}}, route: abuse.RouteTopicPublication, topic: true},
		{name: "reply", target: "/topics/41/replies", form: url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "parent_post_id": {"91"}, "markdown": {"retained reply"}}, route: abuse.RouteReplyPublication},
	} {
		for _, htmx := range []bool{false, true} {
			observer := &captureAbuseObserver{}
			limited := forum.PublicationRateLimitError{RetryAfterSeconds: 17}
			topic := TopicPublisher(func(context.Context, auth.AccessContext, string, string, string) (forum.PublishResult, error) {
				return forum.PublishResult{}, limited
			})
			reply := ReplyPublisher(func(context.Context, auth.AccessContext, int64, int64, string) (forum.PublishResult, error) {
				return forum.PublishResult{}, limited
			})
			handler, err := newPublishingHandler(callbackTestURLBuilder(t), abuse.NewEmptyDestinationPolicy(), observer, topic, reply)
			if err != nil {
				t.Fatalf("newPublishingHandler() returned error: %v", err)
			}
			handler = withModerationTestRequestID(t, handler)
			request := publishingTestRequest(http.MethodPost, test.target, test.form.Encode(), true)
			if htmx {
				request.Header.Set("HX-Request", "true")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body := response.Body.String()
			if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "17" || response.Header().Get("Cache-Control") != "private, no-store" ||
				!strings.Contains(body, "Please wait before publishing again") || !strings.Contains(body, test.form.Get("markdown")) {
				t.Fatalf("%s HTMX=%t rate response = status %d headers %#v body %q", test.name, htmx, response.Code, response.Header(), body)
			}
			assertAbuseEvent(t, observer.events, abuse.RejectionPublicationRate, test.route, http.StatusTooManyRequests, 17)
		}
	}
}

func TestEditingDestinationRejectionsPreserveDraftAndObserveOnce(t *testing.T) {
	policy := blockedHTTPDestinationPolicy(t)
	source := "edit <private> [link](https://blocked.example/path)"
	for _, preview := range []bool{true, false} {
		observer := &captureAbuseObserver{}
		editCalls := 0
		handler, err := newEditingHandler(
			callbackTestURLBuilder(t), policy, observer,
			func(context.Context, auth.AccessContext, int64) (store.EditablePost, error) {
				return validEditablePost(), nil
			},
			func(context.Context, auth.AccessContext, int64, int32, string) (forum.EditResult, error) {
				editCalls++
				return forum.EditResult{}, forum.BlockedDestinationError{}
			},
			func(context.Context, auth.AccessContext, int64, int32) (forum.DeleteResult, error) {
				panic("delete is not expected")
			},
		)
		if err != nil {
			t.Fatalf("newEditingHandler() returned error: %v", err)
		}
		handler = withModerationTestRequestID(t, handler)
		target := "/posts/91/edit"
		if preview {
			target += "/preview"
		}
		form := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}, "revision": {"3"}, "markdown": {source}}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, publishingTestRequest(http.MethodPost, target, form.Encode(), true))
		body := response.Body.String()
		wantCalls := 1
		if preview {
			wantCalls = 0
		}
		if response.Code != http.StatusUnprocessableEntity || response.Header().Get("Cache-Control") != "private, no-store" || editCalls != wantCalls ||
			!strings.Contains(body, "This draft contains a blocked link") || !strings.Contains(body, "edit &lt;private&gt;") || !strings.Contains(body, `name="revision" value="3"`) {
			t.Fatalf("edit preview=%t = status %d cache %q calls %d body %q", preview, response.Code, response.Header().Get("Cache-Control"), editCalls, body)
		}
		assertAbuseEvent(t, observer.events, abuse.RejectionBlocked, abuse.RoutePostEdit, http.StatusUnprocessableEntity, 0)
	}
}

func TestSiteSettingsBlockedRulesPreserveSubmittedValuesAndCurrentRevision(t *testing.T) {
	observer := &captureAbuseObserver{}
	services := validSiteHTTPServices()
	services.Editable = func(context.Context, auth.AccessContext) (site.EditableSettings, error) {
		return site.EditableSettings{Shell: site.ShellPresentation{Name: "Current", Description: "Current description", Theme: "blue"}, Revision: 9}, nil
	}
	services.Update = func(context.Context, auth.AccessContext, site.SettingsInput, pgtype.UUID) (site.MutationResult, error) {
		return site.MutationResult{}, errors.Join(site.ErrInput, abuse.ErrBlockedDestination)
	}
	_, handler, err := newSiteSettingsHandler(callbackTestURLBuilder(t), observer, services)
	if err != nil {
		t.Fatalf("newSiteSettingsHandler() returned error: %v", err)
	}
	handler = withModerationTestRequestID(t, handler)
	token := validCSRFTokenForTest(0x51)
	form := url.Values{
		"_csrf": {token}, "site_name": {"Submitted"}, "site_description": {"Submitted description"}, "brand_theme": {"rose"},
		"rules_markdown": {"rules <private> [link](https://blocked.example/path)"}, "reason": {"Submitted reason"}, "revision": {"4"},
	}
	request := publishingTestRequest(http.MethodPost, "/admin/settings", form.Encode(), true)
	request = request.WithContext(context.WithValue(request.Context(), sessionAuthenticationContextKey{}, auth.SessionAuthentication{
		SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleAdministrator},
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	for _, want := range []string{"This draft contains a blocked link", `value="Submitted"`, "Submitted description", "rules &lt;private&gt;", `value="Submitted reason"`, `name="revision" value="9"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("blocked settings body lacks %q: %s", want, body)
		}
	}
	if response.Code != http.StatusUnprocessableEntity || response.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(body, `name="revision" value="4"`) {
		t.Fatalf("blocked settings = status %d cache %q body %q", response.Code, response.Header().Get("Cache-Control"), body)
	}
	assertAbuseEvent(t, observer.events, abuse.RejectionBlocked, abuse.RouteCommunityRules, http.StatusUnprocessableEntity, 0)
}

func TestBlockedDestinationLogsOnlyFixedEvidence(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	observer, err := abuse.NewObserver(logger)
	if err != nil {
		t.Fatalf("NewObserver() returned error: %v", err)
	}
	unexpectedPublisher := func(context.Context, auth.AccessContext, string, string, string) (forum.PublishResult, error) {
		panic("topic publication is not expected for preview")
	}
	unexpectedReply := func(context.Context, auth.AccessContext, int64, int64, string) (forum.PublishResult, error) {
		panic("reply publication is not expected for preview")
	}
	handler, err := newPublishingHandler(callbackTestURLBuilder(t), blockedHTTPDestinationPolicy(t), observer, unexpectedPublisher, unexpectedReply)
	if err != nil {
		t.Fatalf("newPublishingHandler() returned error: %v", err)
	}
	clockCalls := 0
	handler, err = observability.NewAccessLogMiddleware(handler, logger, func() time.Time {
		clockCalls++
		return time.Unix(100, int64(clockCalls)*int64(time.Millisecond))
	})
	if err != nil {
		t.Fatalf("NewAccessLogMiddleware() returned error: %v", err)
	}
	handler = withModerationTestRequestID(t, handler)

	const markdown = "markdown-sentinel [target](https://blocked.example/rule-sentinel?secret=url-secret)"
	form := url.Values{
		"_csrf": {validCSRFTokenForTest(0x51)}, "area": {"news"}, "title": {"Title"}, "markdown": {markdown},
	}
	request := publishingTestRequest(http.MethodPost, "/topics/preview", form.Encode(), true)
	request.RemoteAddr = "198.51.100.71:61234"
	request.Header.Set("Cookie", "session=cookie-secret")
	request.Header.Set("X-Forwarded-For", "203.0.113.77")
	request.Header.Set("X-Debug-Client-Digest", "client-digest-sentinel")
	request.Header.Set("X-Debug-Configured-Count", "configured-count-sentinel")
	request.Header.Set("X-Debug-Map-Occupancy", "map-occupancy-sentinel")
	request = request.WithContext(context.WithValue(request.Context(), sessionAuthenticationContextKey{}, auth.SessionAuthentication{
		SessionID: 987654320, Access: auth.AccessContext{Authenticated: true, UserID: 987654321, Role: auth.RoleMember},
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	queryRequest := publishingTestRequest(http.MethodGet, "/topics/new?area=news&query=query-sentinel", "", true)
	handler.ServeHTTP(httptest.NewRecorder(), queryRequest)

	logged := logs.String()
	for _, evidence := range []string{
		`"msg":"abuse request rejected"`, `"class":"blocked_destination"`, `"route":"topic-publication"`,
		`"request_id":"` + moderationTestRequestID + `"`, `"status":422`, `"retry_seconds":0`,
		`"msg":"request completed"`, `"route":"POST /topics/preview"`, `"method":"POST"`,
	} {
		if !strings.Contains(logged, evidence) {
			t.Fatalf("log %q lacks %q", logged, evidence)
		}
	}
	if strings.Count(logged, `"msg":"abuse request rejected"`) != 1 || strings.Count(logged, `"msg":"request completed"`) != 2 {
		t.Fatalf("log emitted duplicate terminal evidence: %q", logged)
	}
	for _, sensitive := range []string{
		"198.51.100.71", "203.0.113.77", "987654320", "987654321", "query-sentinel",
		"markdown-sentinel", "blocked.example", "rule-sentinel", "url-secret", "cookie-secret",
		"client-digest-sentinel", "configured-count-sentinel", "map-occupancy-sentinel",
	} {
		if strings.Contains(logged, sensitive) {
			t.Fatalf("log exposed sensitive value %q: %q", sensitive, logged)
		}
	}
}

func blockedHTTPDestinationPolicy(t *testing.T) abuse.DestinationPolicy {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules")
	if err := os.WriteFile(path, []byte("domain=blocked.example\n"), 0o400); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	loaded, err := abuse.LoadPolicy(path, abuse.RateProfile{
		RequestLimit: 10, RequestWindow: time.Minute, RequestClientCapacity: 10,
		PublicationLimit: 10, NewAccountLimit: 3, PublicationWindow: time.Minute, NewAccountPeriod: time.Hour,
	})
	if err != nil {
		t.Fatalf("LoadPolicy() returned error: %v", err)
	}
	return loaded.DestinationPolicy()
}

func assertAbuseEvent(t *testing.T, events []abuse.Event, class abuse.RejectionClass, route abuse.RejectionRoute, status int, retry int64) {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	want := abuse.Event{Class: class, Route: route, RequestID: moderationTestRequestID, Status: status, RetrySeconds: retry}
	if events[0] != want {
		t.Fatalf("event = %+v, want %+v", events[0], want)
	}
}
