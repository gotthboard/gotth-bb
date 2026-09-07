package httpui

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/discovery"
	"github.com/gotthboard/gotth-bb/internal/observability"
)

func TestDiscoveryApplicationLogRetainsRouteWithoutQueryMaterial(t *testing.T) {
	t.Parallel()

	const secretFilter = "do-not-log-filter"
	preflight, err := newDiscoveryPreflightHandler(
		mustURLBuilder(t, ""),
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid request crossed preflight") }),
		func(string) (discovery.AuthenticatedCursor, error) {
			return discovery.AuthenticatedCursor{}, context.Canceled
		},
	)
	if err != nil {
		t.Fatalf("newDiscoveryPreflightHandler() returned error: %v", err)
	}
	var logs bytes.Buffer
	times := []time.Time{time.Unix(100, 0), time.Unix(100, int64(time.Millisecond))}
	clockIndex := 0
	handler, err := observability.NewAccessLogMiddleware(preflight, slog.New(slog.NewJSONHandler(&logs, nil)), func() time.Time {
		value := times[clockIndex]
		clockIndex++
		return value
	})
	if err != nil {
		t.Fatalf("NewAccessLogMiddleware() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/search?area="+secretFilter, nil))
	logged := logs.String()
	if response.Code != http.StatusBadRequest || !strings.Contains(logged, `"route":"GET /search"`) ||
		!strings.Contains(logged, `"status":400`) || strings.Contains(logged, secretFilter) || strings.Contains(logged, "area=") {
		t.Fatalf("discovery log = %q, status = %d", logged, response.Code)
	}
}
