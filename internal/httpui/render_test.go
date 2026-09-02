package httpui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

func TestRenderResponseChoosesCompletePageOrFragment(t *testing.T) {
	t.Parallel()

	page := templ.Raw("<html><body>complete</body></html>")
	fragment := templ.Raw(`<main id="main-content">fragment</main>`)
	tests := []struct {
		name       string
		hxRequest  string
		history    string
		status     int
		wantBody   string
		wantStatus int
	}{
		{name: "page", status: http.StatusOK, wantStatus: http.StatusOK, wantBody: "<html><body>complete</body></html>"},
		{name: "fragment error", hxRequest: "true", status: http.StatusUnprocessableEntity, wantStatus: http.StatusUnprocessableEntity, wantBody: `<main id="main-content">fragment</main>`},
		{name: "history page", hxRequest: "true", history: "true", status: http.StatusNotFound, wantStatus: http.StatusNotFound, wantBody: "<html><body>complete</body></html>"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if test.hxRequest != "" {
				request.Header.Set("HX-Request", test.hxRequest)
			}
			if test.history != "" {
				request.Header.Set("HX-History-Restore-Request", test.history)
			}
			response := httptest.NewRecorder()
			if err := renderResponse(response, request, test.status, page, fragment); err != nil {
				t.Fatalf("renderResponse() returned error: %v", err)
			}
			if response.Code != test.wantStatus || response.Body.String() != test.wantBody {
				t.Fatalf("response = (%d, %q), want (%d, %q)", response.Code, response.Body.String(), test.wantStatus, test.wantBody)
			}
			if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
				t.Fatalf("Content-Type = %q", got)
			}
			if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("Cache-Control = %q", got)
			}
		})
	}
}

func TestRenderResponseRejectsInvalidInputBeforeCommit(t *testing.T) {
	t.Parallel()

	valid := templ.Raw("valid")
	tests := []struct {
		name     string
		status   int
		page     templ.Component
		fragment templ.Component
	}{
		{name: "invalid status", status: 99, page: valid, fragment: valid},
		{name: "no-content status", status: http.StatusNoContent, page: valid, fragment: valid},
		{name: "reset-content status", status: http.StatusResetContent, page: valid, fragment: valid},
		{name: "not-modified status", status: http.StatusNotModified, page: valid, fragment: valid},
		{name: "nil page", status: http.StatusOK, fragment: valid},
		{name: "nil fragment", status: http.StatusOK, page: valid},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			response := httptest.NewRecorder()
			if err := renderResponse(response, httptest.NewRequest(http.MethodGet, "/", nil), test.status, test.page, test.fragment); err == nil {
				t.Fatal("renderResponse() accepted invalid input")
			}
			if response.Body.Len() != 0 {
				t.Fatalf("body = %q, want uncommitted response", response.Body.String())
			}
		})
	}
}

func TestRenderResponseReturnsRenderAndWriteFailures(t *testing.T) {
	t.Parallel()

	renderCause := errors.New("render failed")
	failed := templ.ComponentFunc(func(context.Context, io.Writer) error { return renderCause })
	response := httptest.NewRecorder()
	err := renderResponse(response, httptest.NewRequest(http.MethodGet, "/", nil), http.StatusOK, failed, failed)
	if !errors.Is(err, renderCause) || response.Body.Len() != 0 {
		t.Fatalf("render failure = %v, body = %q", err, response.Body.String())
	}

	writeCause := errors.New("write failed")
	writer := &failingRenderResponseWriter{header: make(http.Header), cause: writeCause}
	err = renderResponse(writer, httptest.NewRequest(http.MethodGet, "/", nil), http.StatusOK, templ.Raw("body"), templ.Raw("body"))
	if !errors.Is(err, writeCause) || writer.status != http.StatusOK {
		t.Fatalf("write failure = %v, status = %d", err, writer.status)
	}
}

type failingRenderResponseWriter struct {
	header http.Header
	status int
	cause  error
}

func (writer *failingRenderResponseWriter) Header() http.Header {
	return writer.header
}

func (writer *failingRenderResponseWriter) WriteHeader(status int) {
	writer.status = status
}

func (writer *failingRenderResponseWriter) Write([]byte) (int, error) {
	return 0, writer.cause
}

func TestHTMXConfigurationDisablesUnsafeRuntimeFeaturesAndSwapsValidation(t *testing.T) {
	t.Parallel()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(htmxConfiguration), &parsed); err != nil {
		t.Fatalf("HTMX configuration is invalid JSON: %v", err)
	}

	for _, required := range []string{
		`"allowEval":false`,
		`"allowScriptTags":false`,
		`"historyCacheSize":0`,
		`"historyRestoreAsHxRequest":false`,
		`"includeIndicatorStyles":false`,
		`"reportValidityOfForms":true`,
		`"selfRequestsOnly":true`,
		`{"code":"409","swap":true,"error":true}`,
		`{"code":"422","swap":true,"error":true}`,
		`{"code":"[45]..","swap":false,"error":true}`,
	} {
		if !strings.Contains(htmxConfiguration, required) {
			t.Errorf("HTMX configuration lacks %s", required)
		}
	}
}
