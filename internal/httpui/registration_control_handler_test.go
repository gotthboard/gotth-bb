package httpui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gotthboard/gotth-bb/internal/control"
	"github.com/gotthboard/gotth-bb/internal/registration"
)

func TestRegistrationAdmissionIsExactAndFailClosed(t *testing.T) {
	loads := 0
	services := RegistrationControlHTTPServices{
		LoadSettings: func(context.Context) (control.Settings, error) {
			loads++
			return control.Settings{Registration: control.RegistrationAdministratorApproval, Revision: 1}, nil
		},
		VerifyApproval: func(context.Context, string) (registration.Intake, error) { panic("unexpected verify") },
		AcceptApproval: func(context.Context, registration.Intake) error { panic("unexpected accept") },
		SMTPConfigured: true,
	}
	fallbackCalls := 0
	handler, err := NewRegistrationControlHandler(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		fallbackCalls++
		response.WriteHeader(http.StatusTeapot)
	}), services)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		method string
		target string
		status int
		loads  int
	}{
		{http.MethodGet, "/registration/admission/administrator_approval", http.StatusNoContent, 1},
		{http.MethodGet, "/registration/admission/verified_email_open", http.StatusNotFound, 1},
		{http.MethodGet, "/registration/admission/closed", http.StatusNotFound, 0},
		{http.MethodGet, "/registration/admission/administrator_approval?mode=open", http.StatusNotFound, 0},
		{http.MethodPost, "/registration/admission/administrator_approval", http.StatusMethodNotAllowed, 0},
		{http.MethodGet, "/unrelated", http.StatusTeapot, 0},
	}
	for _, test := range tests {
		before := loads
		request := httptest.NewRequest(test.method, test.target, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status || loads-before != test.loads || response.Body.Len() != 0 ||
			(response.Code != http.StatusTeapot && response.Header().Get("Cache-Control") != "no-store") {
			t.Fatalf("%s %s = status %d loads %d headers %v body %q", test.method, test.target, response.Code, loads-before, response.Header(), response.Body.String())
		}
	}
	if fallbackCalls != 1 {
		t.Fatalf("fallback calls = %d", fallbackCalls)
	}
}

func TestApprovalIntakeReturnsIndistinguishableAcceptedClass(t *testing.T) {
	verifyCalls, acceptCalls := 0, 0
	verifyErr, acceptErr := error(nil), error(nil)
	services := RegistrationControlHTTPServices{
		LoadSettings: func(context.Context) (control.Settings, error) { panic("unexpected settings") },
		VerifyApproval: func(_ context.Context, raw string) (registration.Intake, error) {
			verifyCalls++
			if raw == "" && verifyErr == nil {
				return registration.Intake{}, errors.New("invalid")
			}
			return registration.Intake{AuthentikUserID: 17}, verifyErr
		},
		AcceptApproval: func(_ context.Context, intake registration.Intake) error {
			acceptCalls++
			if intake.AuthentikUserID != 17 {
				t.Fatalf("unexpected intake: %+v", intake)
			}
			return acceptErr
		},
		SMTPConfigured: true,
	}
	handler, err := NewRegistrationControlHandler(http.NotFoundHandler(), services)
	if err != nil {
		t.Fatal(err)
	}
	serve := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/registration/intake/approval", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/jwt")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Header().Get("Cache-Control") != "no-store" || response.Body.Len() != 0 || request.Pattern != "POST /registration/intake/approval" {
			t.Fatalf("intake response headers/pattern/body = (%v, %q, %q)", response.Header(), request.Pattern, response.Body.String())
		}
		return response
	}
	if response := serve("signed.assertion.value"); response.Code != http.StatusAccepted || verifyCalls != 1 || acceptCalls != 1 {
		t.Fatalf("valid intake = status %d verify/accept %d/%d", response.Code, verifyCalls, acceptCalls)
	}
	verifyErr = errors.New("invalid signature")
	if response := serve("attacker.assertion.value"); response.Code != http.StatusAccepted || verifyCalls != 2 || acceptCalls != 1 {
		t.Fatalf("invalid intake = status %d verify/accept %d/%d", response.Code, verifyCalls, acceptCalls)
	}
	verifyErr, acceptErr = nil, errors.New("database unavailable")
	if response := serve("signed.assertion.value"); response.Code != http.StatusServiceUnavailable || verifyCalls != 3 || acceptCalls != 2 {
		t.Fatalf("failed persistence = status %d verify/accept %d/%d", response.Code, verifyCalls, acceptCalls)
	}
	acceptErr = nil
	if response := serve(""); response.Code != http.StatusAccepted || verifyCalls != 4 || acceptCalls != 2 {
		t.Fatalf("empty assertion = status %d verify/accept %d/%d", response.Code, verifyCalls, acceptCalls)
	}
}

func TestApprovalIntakeRejectsTransportGrammarBeforeServices(t *testing.T) {
	services := RegistrationControlHTTPServices{
		LoadSettings:   func(context.Context) (control.Settings, error) { panic("unexpected settings") },
		VerifyApproval: func(context.Context, string) (registration.Intake, error) { panic("unexpected verify") },
		AcceptApproval: func(context.Context, registration.Intake) error { panic("unexpected accept") },
		SMTPConfigured: true,
	}
	handler, _ := NewRegistrationControlHandler(http.NotFoundHandler(), services)
	tests := []struct {
		method      string
		target      string
		contentType string
		body        string
		status      int
	}{
		{http.MethodGet, "/registration/intake/approval", "application/jwt", "x", http.StatusMethodNotAllowed},
		{http.MethodPost, "/registration/intake/approval?x=1", "application/jwt", "x", http.StatusBadRequest},
		{http.MethodPost, "/registration/intake/approval", "text/plain", "x", http.StatusUnsupportedMediaType},
		{http.MethodPost, "/registration/intake/approval", "application/jwt; charset=utf-8", "x", http.StatusUnsupportedMediaType},
		{http.MethodPost, "/registration/intake/approval", "application/jwt", strings.Repeat("x", maximumApprovalIntakeBytes+1), http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.target, strings.NewReader(test.body))
		request.Header.Set("Content-Type", test.contentType)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status || response.Body.Len() != 0 {
			t.Fatalf("%s %s = %d %q", test.method, test.target, response.Code, response.Body.String())
		}
	}
}
