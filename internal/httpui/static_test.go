package httpui

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedStaticAssetsMatchPinnedGeneration(t *testing.T) {
	t.Parallel()

	const stylesheetSHA256 = "48b7cfc575316424079ce4a4df6fffea3db5f7cfeda25761bc11dedd4528caf5"
	if want := "app-" + stylesheetSHA256 + ".css"; appStylesheetFilename != want {
		t.Fatalf("stylesheet filename = %q, want content-addressed %q", appStylesheetFilename, want)
	}

	tests := []struct {
		name       string
		content    []byte
		wantSHA256 string
		contains   string
	}{
		{name: "Tailwind CSS", content: appStylesheet, wantSHA256: stylesheetSHA256, contains: ".focus\\:not-sr-only"},
		{name: "HTMX", content: htmxScript, wantSHA256: "71ea67185bfa8c98c39d31717c6fce5d852370fcdfd129db4543774d3145c0de", contains: "htmx"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			digest := sha256.Sum256(test.content)
			if got := hex.EncodeToString(digest[:]); got != test.wantSHA256 {
				t.Fatalf("SHA-256 = %s, want %s", got, test.wantSHA256)
			}
			if !strings.Contains(string(test.content), test.contains) {
				t.Fatalf("asset does not contain %q", test.contains)
			}
		})
	}
}

func TestStaticAssetHandlerServesImmutableGetHeadAndRange(t *testing.T) {
	t.Parallel()

	handler := staticAssetHandler("text/css; charset=utf-8", []byte("0123456789"))
	tests := []struct {
		name       string
		method     string
		rangeValue string
		wantStatus int
		wantBody   string
	}{
		{name: "get", method: http.MethodGet, wantStatus: http.StatusOK, wantBody: "0123456789"},
		{name: "head", method: http.MethodHead, wantStatus: http.StatusOK},
		{name: "range", method: http.MethodGet, rangeValue: "bytes=2-5", wantStatus: http.StatusPartialContent, wantBody: "2345"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(test.method, "/static/test.css", nil)
			if test.rangeValue != "" {
				request.Header.Set("Range", test.rangeValue)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || response.Body.String() != test.wantBody {
				t.Fatalf("response = (%d, %q), want (%d, %q)", response.Code, response.Body.String(), test.wantStatus, test.wantBody)
			}
			if got := response.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
				t.Fatalf("Content-Type = %q", got)
			}
			if got := response.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
				t.Fatalf("Cache-Control = %q", got)
			}
		})
	}
}
