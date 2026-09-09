package httpui

import (
	"context"
	"net/http"
	"strings"

	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/control"
	"github.com/gotthboard/gotth-bb/internal/policy"
)

type controlSettingsLoader func(context.Context) (control.Settings, error)
type controlSettingsLoaderContextKey struct{}

func withControlSettingsLoader(next http.Handler, load controlSettingsLoader) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		ctx := context.WithValue(request.Context(), controlSettingsLoaderContextKey{}, load)
		contextualRequest := request.WithContext(ctx)
		defer func() { request.Pattern = contextualRequest.Pattern }()
		next.ServeHTTP(response, contextualRequest)
	})
}

func enforceMaintenance(response http.ResponseWriter, request *http.Request, authentication auth.SessionAuthentication, view pageView) bool {
	load, ok := request.Context().Value(controlSettingsLoaderContextKey{}).(controlSettingsLoader)
	if !ok || load == nil || maintenanceRecoveryRoute(request.URL.Path, authentication) {
		return false
	}
	settings, err := load(request.Context())
	if err == nil && !settings.MaintenanceEnabled {
		return false
	}
	response.Header().Set("Cache-Control", "private, no-store")
	response.Header().Set("Retry-After", "60")
	heading := "Maintenance"
	message := "This site is temporarily unavailable for maintenance."
	if err == nil && settings.MaintenanceMessage != "" {
		message = settings.MaintenanceMessage
	}
	if renderErr := renderResponse(response, request, http.StatusServiceUnavailable,
		errorPage(view, http.StatusServiceUnavailable, heading, message),
		errorContent(view, http.StatusServiceUnavailable, heading, message)); renderErr != nil {
		panic(renderErr)
	}
	return true
}

func maintenanceRecoveryRoute(path string, authentication auth.SessionAuthentication) bool {
	switch path {
	case "/auth/revalidate", "/logout", "/setup", "/setup/administrator":
		return true
	}
	return strings.HasPrefix(path, "/admin") && policy.CanAdminister(authentication.Access)
}
