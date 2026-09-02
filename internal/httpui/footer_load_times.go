package httpui

import (
	"context"
	"fmt"
	"net/http"
	"time"
	"unicode"
	"unicode/utf8"
)

const maximumFooterVersionBytes = 64

type footerLoadTimesContextKey struct{}

type footerLoadTimes struct {
	version              string
	pageStarted          time.Time
	templateStarted      time.Time
	templateStartedValid bool
	clock                func() time.Time
}

type footerLoadTimesView struct {
	Version      string
	PageTime     string
	TemplateTime string
}

// NewFooterLoadTimesHandler records the start of each browser request and
// carries the validated release version to complete-page templates. The page
// duration therefore includes downstream authentication and data work. HTMX
// fragments have no footer and expose no load-time fields.
//
// Complexity: construction is tight Theta(v+1) time for v version runes and
// tight Theta(1) auxiliary space. Each request adds one clock read, one
// context value, and O(d+1) context-chain work for depth d; downstream work is
// delegated.
func NewFooterLoadTimesHandler(next http.Handler, version string, clock func() time.Time) (http.Handler, error) {
	if next == nil {
		return nil, fmt.Errorf("footer load-time handler requires a downstream handler")
	}
	if !validFooterVersion(version) {
		return nil, fmt.Errorf("footer load-time handler requires a valid release version")
	}
	if clock == nil {
		return nil, fmt.Errorf("footer load-time handler requires a clock")
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		loadTimes := footerLoadTimes{
			version:     version,
			pageStarted: clock(),
			clock:       clock,
		}
		ctx := context.WithValue(request.Context(), footerLoadTimesContextKey{}, loadTimes)
		next.ServeHTTP(response, request.WithContext(ctx))
	}), nil
}

func validFooterVersion(version string) bool {
	if version == "" || len(version) > maximumFooterVersionBytes || !utf8.ValidString(version) {
		return false
	}
	for _, value := range version {
		if unicode.IsControl(value) {
			return false
		}
	}
	return true
}

// withFooterTemplateStart takes the second timestamp immediately before the
// selected Templ component renders. It copies the request-scoped value rather
// than mutating shared state.
func withFooterTemplateStart(ctx context.Context) context.Context {
	loadTimes, ok := ctx.Value(footerLoadTimesContextKey{}).(footerLoadTimes)
	if !ok || loadTimes.clock == nil {
		return ctx
	}
	loadTimes.templateStarted = loadTimes.clock()
	loadTimes.templateStartedValid = true
	return context.WithValue(ctx, footerLoadTimesContextKey{}, loadTimes)
}

// footerLoadTimesFromContext reads one common end time while rendering the
// footer. Like Forgejo's footer, Page is elapsed request time at that point and
// Template is elapsed component-render time at that same point. Neither value
// claims to include the remaining closing markup or response write.
func footerLoadTimesFromContext(ctx context.Context) footerLoadTimesView {
	if ctx == nil {
		return footerLoadTimesView{}
	}
	loadTimes, ok := ctx.Value(footerLoadTimesContextKey{}).(footerLoadTimes)
	if !ok || loadTimes.version == "" || loadTimes.clock == nil || !loadTimes.templateStartedValid {
		return footerLoadTimesView{}
	}
	now := loadTimes.clock()
	return footerLoadTimesView{
		Version:      loadTimes.version,
		PageTime:     formatFooterDuration(loadTimes.pageStarted, now),
		TemplateTime: formatFooterDuration(loadTimes.templateStarted, now),
	}
}

func formatFooterDuration(started, finished time.Time) string {
	duration := finished.Sub(started)
	if duration < 0 {
		duration = 0
	}
	return fmt.Sprintf("%dms", duration.Milliseconds())
}
