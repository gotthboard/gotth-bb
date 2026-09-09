package httpui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/control"
)

func TestMaintenanceDeniesOrdinaryRoutesAfterSessionResolution(t *testing.T) {
	t.Parallel()

	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x6c}, sessionCookieTokenBytes))
	for _, test := range []struct {
		name   string
		role   auth.Role
		path   string
		cookie bool
		want   int
	}{
		{name: "visitor", path: "/", want: http.StatusServiceUnavailable},
		{name: "member", role: auth.RoleMember, path: "/areas/news", cookie: true, want: http.StatusServiceUnavailable},
		{name: "moderator", role: auth.RoleModerator, path: "/activity", cookie: true, want: http.StatusServiceUnavailable},
		{name: "administrator ordinary", role: auth.RoleAdministrator, path: "/topics/7", cookie: true, want: http.StatusServiceUnavailable},
		{name: "administrator recovery", role: auth.RoleAdministrator, path: "/admin/control", cookie: true, want: http.StatusNoContent},
		{name: "member denied admin", role: auth.RoleMember, path: "/admin/control", cookie: true, want: http.StatusServiceUnavailable},
		{name: "logout recovery", role: auth.RoleMember, path: "/logout", cookie: true, want: http.StatusNoContent},
		{name: "revalidation recovery", role: auth.RoleMember, path: "/auth/revalidate", cookie: true, want: http.StatusNoContent},
		{name: "setup recovery", path: "/setup", want: http.StatusNoContent},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			downstream := 0
			handler, err := newSessionAuthenticationHandler(
				http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
					downstream++
					response.WriteHeader(http.StatusNoContent)
				}),
				func(context.Context, string) (auth.SessionAuthentication, error) {
					return auth.SessionAuthentication{SessionID: 9, Access: auth.AccessContext{
						Authenticated: true, UserID: 7, Role: test.role,
					}}, nil
				}, "gotth_bb_session", callbackTestURLBuilder(t), true,
			)
			if err != nil {
				t.Fatalf("construct handler: %v", err)
			}
			handler = withControlSettingsLoader(handler, func(context.Context) (control.Settings, error) {
				return control.Settings{Registration: control.RegistrationClosed, MaintenanceEnabled: true, MaintenanceMessage: "Planned maintenance"}, nil
			})
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.cookie {
				request.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: token})
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
			if test.want == http.StatusServiceUnavailable {
				if downstream != 0 || response.Header().Get("Retry-After") != "60" ||
					response.Header().Get("Cache-Control") != "private, no-store" ||
					!strings.Contains(response.Body.String(), "Planned maintenance") {
					t.Fatalf("maintenance response = (downstream %d, headers %v, body %q)", downstream, response.Header(), response.Body.String())
				}
			} else if downstream != 1 {
				t.Fatalf("recovery downstream calls = %d, want 1", downstream)
			}
		})
	}
}

func TestMaintenanceFailClosedAndHTMXRepresentation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		load         controlSettingsLoader
		htmx         bool
		wantStatus   int
		wantDocument bool
	}{
		{name: "disabled", load: func(context.Context) (control.Settings, error) {
			return control.Settings{Registration: control.RegistrationClosed}, nil
		}, wantStatus: http.StatusNoContent},
		{name: "load failure", load: func(context.Context) (control.Settings, error) {
			return control.Settings{}, errors.New("secret database failure")
		}, wantStatus: http.StatusServiceUnavailable, wantDocument: true},
		{name: "fragment", load: func(context.Context) (control.Settings, error) {
			return control.Settings{Registration: control.RegistrationClosed, MaintenanceEnabled: true}, nil
		}, htmx: true, wantStatus: http.StatusServiceUnavailable},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler, err := newSessionAuthenticationHandler(
				http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusNoContent) }),
				func(context.Context, string) (auth.SessionAuthentication, error) {
					return auth.SessionAuthentication{}, nil
				},
				"gotth_bb_session", callbackTestURLBuilder(t), true,
			)
			if err != nil {
				t.Fatalf("construct handler: %v", err)
			}
			handler = withControlSettingsLoader(handler, test.load)
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if test.htmx {
				request.Header.Set("HX-Request", "true")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("response = (status %d, body %q)", response.Code, response.Body.String())
			}
			isDocument := strings.Contains(response.Body.String(), "<!doctype html>")
			if isDocument != test.wantDocument {
				t.Fatalf("document representation = %t, want %t", isDocument, test.wantDocument)
			}
		})
	}
}
