package httpui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gotthboard/gotth-bb/internal/control"
)

func TestDynamicRegistrationRendersExactCurrentMode(t *testing.T) {
	t.Parallel()
	mode := control.RegistrationVerifiedEmailOpen
	handler, err := newDynamicRegistrationHandler(callbackTestURLBuilder(t), RegistrationHTTPServices{
		LoadSettings: func(context.Context) (control.Settings, error) {
			return control.Settings{Registration: mode, Revision: 1}, nil
		},
		Issuer:       url.URL{Scheme: "https", Host: "auth.example", Path: "/application/o/board/"},
		OpenFlowSlug: "gotth-bb-open", ApprovalFlowSlug: "gotth-bb-approval", SMTPConfigured: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		mode       control.RegistrationMode
		want       string
		forbidden  string
		wantStatus int
	}{
		{control.RegistrationClosed, "Registration is currently closed.", "auth.example", http.StatusOK},
		{control.RegistrationInvitationOnly, "Registration is by invitation only.", "auth.example", http.StatusOK},
		{control.RegistrationVerifiedEmailOpen, "https://auth.example/if/flow/gotth-bb-open/?next=https%3A%2F%2Fforum.example%2Fbb%2Flogin", "gotth-bb-approval", http.StatusOK},
		{control.RegistrationAdministratorApproval, "https://auth.example/if/flow/gotth-bb-approval/?next=https%3A%2F%2Fforum.example%2Fbb%2Flogin", "gotth-bb-open", http.StatusOK},
	}
	for _, test := range tests {
		mode = test.mode
		request := httptest.NewRequest(http.MethodGet, "/bb/register", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.wantStatus || !strings.Contains(response.Header().Get("Cache-Control"), "no-store") || !strings.Contains(response.Body.String(), test.want) || strings.Contains(response.Body.String(), test.forbidden) {
			t.Fatalf("mode %q = (%d, headers %v, body %q)", test.mode, response.Code, response.Header(), response.Body.String())
		}
	}
}

func TestDynamicRegistrationFailsClosed(t *testing.T) {
	t.Parallel()
	loadErr := error(nil)
	settings := control.Settings{Registration: control.RegistrationVerifiedEmailOpen, Revision: 1}
	services := RegistrationHTTPServices{
		LoadSettings: func(context.Context) (control.Settings, error) { return settings, loadErr },
		Issuer:       url.URL{Scheme: "https", Host: "auth.example"}, OpenFlowSlug: "open", ApprovalFlowSlug: "approval", SMTPConfigured: false,
	}
	handler, err := newDynamicRegistrationHandler(callbackTestURLBuilder(t), services)
	if err != nil {
		t.Fatal(err)
	}
	serve := func(method, target string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, target, nil))
		return response
	}
	if response := serve(http.MethodGet, "/bb/register"); response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "auth.example") {
		t.Fatalf("disabled SMTP response = (%d, %q)", response.Code, response.Body.String())
	}
	loadErr = errors.New("database")
	if response := serve(http.MethodGet, "/bb/register"); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("database failure = %d", response.Code)
	}
	loadErr = nil
	settings.MaintenanceEnabled = true
	if response := serve(http.MethodGet, "/bb/register"); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("maintenance response = %d", response.Code)
	}
	if response := serve(http.MethodPost, "/bb/register"); response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("wrong method = (%d, %q)", response.Code, response.Header().Get("Allow"))
	}
	if response := serve(http.MethodGet, "/bb/register?next=https://evil.example"); response.Code != http.StatusNotFound {
		t.Fatalf("query response = %d", response.Code)
	}
}

func TestDynamicRegistrationRejectsInvalidConstruction(t *testing.T) {
	valid := RegistrationHTTPServices{
		LoadSettings: func(context.Context) (control.Settings, error) { return control.Settings{}, nil },
		Issuer:       url.URL{Scheme: "https", Host: "auth.example"}, OpenFlowSlug: "open", ApprovalFlowSlug: "approval", SMTPConfigured: true,
	}
	for _, mutate := range []func(*RegistrationHTTPServices){
		func(value *RegistrationHTTPServices) { value.LoadSettings = nil },
		func(value *RegistrationHTTPServices) { value.Issuer.Scheme = "http" },
		func(value *RegistrationHTTPServices) { value.OpenFlowSlug = "../open" },
		func(value *RegistrationHTTPServices) { value.ApprovalFlowSlug = "open" },
	} {
		candidate := valid
		mutate(&candidate)
		if handler, err := newDynamicRegistrationHandler(callbackTestURLBuilder(t), candidate); err == nil || handler != nil {
			t.Fatalf("invalid services accepted: %+v", candidate)
		}
	}
	for _, issuer := range []url.URL{
		{Scheme: "http", Host: "127.0.0.1:39443"},
		{Scheme: "http", Host: "localhost:39443"},
	} {
		candidate := valid
		candidate.Issuer = issuer
		if handler, err := newDynamicRegistrationHandler(callbackTestURLBuilder(t), candidate); err != nil || handler == nil {
			t.Fatalf("loopback issuer rejected: %+v, %v", issuer, err)
		}
	}
}
