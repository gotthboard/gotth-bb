package httpui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

func TestRenderDiscoveryResponseBuffersBeforeCommitAndBoundsEnvelope(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, hxRequest string
		wantBody        string
	}{
		{name: "page", wantBody: "complete"},
		{name: "fragment", hxRequest: "true", wantBody: "fragment"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, "/search?q=term", nil)
			request.Header.Set("HX-Request", test.hxRequest)
			response := httptest.NewRecorder()
			err := renderDiscoveryResponse(response, request, http.StatusOK, textComponent("complete"), textComponent("fragment"))
			if err != nil || response.Code != http.StatusOK || response.Body.String() != test.wantBody || response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("response = (%d, %v, %q), err %v", response.Code, response.Header(), response.Body.String(), err)
			}
		})
	}

	request := httptest.NewRequest(http.MethodGet, "/search?q=term", nil)
	response := httptest.NewRecorder()
	err := renderDiscoveryResponse(response, request, http.StatusOK, textComponent(strings.Repeat("x", maximumDiscoveryResponseBytes+1)), textComponent("fragment"))
	if !errors.Is(err, errDiscoveryResponseTooLarge) || response.Body.Len() != 0 || response.Header().Get("Content-Type") != "" {
		t.Fatalf("overflow response = (%d, %v, %d bytes), err %v", response.Code, response.Header(), response.Body.Len(), err)
	}
}

func TestRenderDiscoveryResponseRejectsCanceledContextBeforeCommit(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/activity", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	err := renderDiscoveryResponse(response, request, http.StatusOK, textComponent("must not commit"), textComponent("fragment"))
	if !errors.Is(err, context.Canceled) || response.Body.Len() != 0 || response.Header().Get("Content-Type") != "" {
		t.Fatalf("canceled response = (%d, %v, %q), err %v", response.Code, response.Header(), response.Body.String(), err)
	}
}

func TestRenderDiscoveryResponseRejectsInvalidInputAndPropagatesFailures(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/search?q=term", nil)
	component := textComponent("body")
	for _, test := range []struct {
		name     string
		request  *http.Request
		status   int
		page     templ.Component
		fragment templ.Component
	}{
		{name: "request", status: http.StatusOK, page: component, fragment: component},
		{name: "status", request: request, status: http.StatusNoContent, page: component, fragment: component},
		{name: "page", request: request, status: http.StatusOK, fragment: component},
		{name: "fragment", request: request, status: http.StatusOK, page: component},
	} {
		response := httptest.NewRecorder()
		if err := renderDiscoveryResponse(response, test.request, test.status, test.page, test.fragment); err == nil || response.Body.Len() != 0 {
			t.Fatalf("%s = (error %v, body %q)", test.name, err, response.Body.String())
		}
	}

	renderCause := errors.New("render failed")
	failed := templ.ComponentFunc(func(context.Context, io.Writer) error { return renderCause })
	response := httptest.NewRecorder()
	if err := renderDiscoveryResponse(response, request, http.StatusOK, failed, failed); !errors.Is(err, renderCause) || response.Body.Len() != 0 {
		t.Fatalf("render failure = %v, body = %q", err, response.Body.String())
	}

	writeCause := errors.New("write failed")
	writer := &failingRenderResponseWriter{header: make(http.Header), cause: writeCause}
	if err := renderDiscoveryResponse(writer, request, http.StatusOK, component, component); !errors.Is(err, writeCause) || writer.status != http.StatusOK {
		t.Fatalf("write failure = %v, status = %d", err, writer.status)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelDuringRender := templ.ComponentFunc(func(_ context.Context, writer io.Writer) error {
		cancel()
		_, err := io.WriteString(writer, "discard")
		return err
	})
	response = httptest.NewRecorder()
	err := renderDiscoveryResponse(response, request.WithContext(ctx), http.StatusOK, cancelDuringRender, cancelDuringRender)
	if !errors.Is(err, context.Canceled) || response.Body.Len() != 0 {
		t.Fatalf("mid-render cancellation = %v, body = %q", err, response.Body.String())
	}
}

func textComponent(value string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, writer io.Writer) error {
		_, err := io.WriteString(writer, value)
		return err
	})
}
