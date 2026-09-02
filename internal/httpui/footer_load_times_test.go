package httpui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
)

func TestFooterLoadTimesHandlerRendersForgejoStyleEvidence(t *testing.T) {
	t.Parallel()

	publicBaseURL, err := url.Parse("https://forum.example.test")
	if err != nil {
		t.Fatalf("url.Parse() returned error: %v", err)
	}
	builder, err := NewURLBuilder(*publicBaseURL, "")
	if err != nil {
		t.Fatalf("NewURLBuilder() returned error: %v", err)
	}
	view, err := newPageView(builder, "Areas")
	if err != nil {
		t.Fatalf("newPageView() returned error: %v", err)
	}
	times := []time.Time{
		time.Unix(100, 0),
		time.Unix(100, int64(50*time.Millisecond)),
		time.Unix(100, int64(57*time.Millisecond)),
	}
	clockIndex := 0
	clock := func() time.Time {
		value := times[clockIndex]
		clockIndex++
		return value
	}
	downstream := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if renderErr := renderResponse(
			response,
			request,
			http.StatusOK,
			document(view, templ.Raw(`<main id="main-content">complete</main>`)),
			templ.Raw(`<main id="main-content">fragment</main>`),
		); renderErr != nil {
			t.Fatalf("renderResponse() returned error: %v", renderErr)
		}
	})
	handler, err := NewFooterLoadTimesHandler(downstream, "1.0.0-alpha.1", clock)
	if err != nil {
		t.Fatalf("NewFooterLoadTimesHandler() returned error: %v", err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusOK || clockIndex != len(times) {
		t.Fatalf("response status/clock reads = (%d, %d), want (%d, %d)", response.Code, clockIndex, http.StatusOK, len(times))
	}
	for _, expected := range []string{
		"Powered by",
		"GOTTH Board",
		"Version:",
		"1.0.0-alpha.1",
		"Page:",
		"57ms",
		"Template:",
		"7ms",
	} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("complete page lacks %q: %q", expected, response.Body.String())
		}
	}
}

func TestFooterLoadTimesHandlerLeavesHTMXFragmentFooterFree(t *testing.T) {
	t.Parallel()

	times := []time.Time{time.Unix(200, 0)}
	clockIndex := 0
	handler, err := NewFooterLoadTimesHandler(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if renderErr := renderResponse(
			response,
			request,
			http.StatusOK,
			templ.Raw("complete"),
			templ.Raw(`<main id="main-content">fragment</main>`),
		); renderErr != nil {
			t.Fatalf("renderResponse() returned error: %v", renderErr)
		}
	}), "1.0.0-alpha.1", func() time.Time {
		value := times[clockIndex]
		clockIndex++
		return value
	})
	if err != nil {
		t.Fatalf("NewFooterLoadTimesHandler() returned error: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Body.String() != `<main id="main-content">fragment</main>` || clockIndex != len(times) {
		t.Fatalf("fragment/clock reads = (%q, %d), want footer-free fragment and %d reads", response.Body.String(), clockIndex, len(times))
	}
}

func TestFooterLoadTimesClampNegativeDurationsAndRejectInvalidInputs(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(context.Background(), footerLoadTimesContextKey{}, footerLoadTimes{
		version:              "development",
		pageStarted:          time.Unix(300, 0),
		templateStarted:      time.Unix(301, 0),
		templateStartedValid: true,
		clock:                func() time.Time { return time.Unix(299, 0) },
	})
	view := footerLoadTimesFromContext(ctx)
	if view.Version != "development" || view.PageTime != "0ms" || view.TemplateTime != "0ms" {
		t.Fatalf("negative duration view = %+v", view)
	}
	if got := footerLoadTimesFromContext(context.Background()); got != (footerLoadTimesView{}) {
		t.Fatalf("missing context view = %+v, want empty", got)
	}
	if got := footerLoadTimesFromContext(nil); got != (footerLoadTimesView{}) {
		t.Fatalf("nil context view = %+v, want empty", got)
	}

	validHandler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	validClock := func() time.Time { return time.Time{} }
	tests := []struct {
		name    string
		next    http.Handler
		version string
		clock   func() time.Time
	}{
		{name: "nil handler", version: "development", clock: validClock},
		{name: "empty version", next: validHandler, clock: validClock},
		{name: "oversized version", next: validHandler, version: strings.Repeat("v", maximumFooterVersionBytes+1), clock: validClock},
		{name: "invalid UTF-8 version", next: validHandler, version: string([]byte{0xff}), clock: validClock},
		{name: "control version", next: validHandler, version: "alpha\n1", clock: validClock},
		{name: "nil clock", next: validHandler, version: "development"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if handler, handlerErr := NewFooterLoadTimesHandler(test.next, test.version, test.clock); handlerErr == nil || handler != nil {
				t.Fatalf("NewFooterLoadTimesHandler() = (%v, %v), want nil handler and error", handler, handlerErr)
			}
		})
	}
}
