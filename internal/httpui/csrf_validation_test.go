package httpui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestValidateCSRFRequestAcceptsExactHeaderWithoutReadingBody(t *testing.T) {
	t.Parallel()

	token := validCSRFTokenForTest(0x41)
	request := csrfRequestWithAuthority(http.MethodPatch, token)
	request.Header[http.CanonicalHeaderKey(csrfHeaderName)] = []string{token}
	request.Body = panicCSRFBody{}
	if err := validateCSRFRequest(request, 1024); err != nil {
		t.Fatalf("validateCSRFRequest() returned error: %v", err)
	}
}

func TestValidateCSRFRequestAcceptsAndRestoresExactForm(t *testing.T) {
	t.Parallel()

	token := validCSRFTokenForTest(0x42)
	bodyBytes := []byte("_csrf=" + url.QueryEscape(token) + "&title=hello+world")
	body := &trackingCSRFBody{Reader: bytes.NewReader(bodyBytes)}
	request := csrfRequestWithAuthority(http.MethodPost, token)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Body = body
	request.ContentLength = int64(len(bodyBytes))
	if err := validateCSRFRequest(request, int64(len(bodyBytes))); err != nil {
		t.Fatalf("validateCSRFRequest() returned error: %v", err)
	}
	restored, err := io.ReadAll(request.Body)
	if err != nil || !bytes.Equal(restored, bodyBytes) || !body.closed {
		t.Fatalf("restored body = (%q, %v, original closed %t)", restored, err, body.closed)
	}
}

func TestValidateCSRFRequestRejectsInvalidBoundaryState(t *testing.T) {
	t.Parallel()

	validToken := validCSRFTokenForTest(0x43)
	invalidToken := validToken[:len(validToken)-1] + "*"
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name    string
		request *http.Request
		limit   int64
	}{
		{name: "nil request", limit: 1},
		{name: "zero limit", request: csrfRequestWithAuthority(http.MethodPost, validToken)},
		{name: "large limit", request: csrfRequestWithAuthority(http.MethodPost, validToken), limit: maximumCSRFValidationFormBytes + 1},
		{name: "safe method", request: csrfRequestWithAuthority(http.MethodGet, validToken), limit: 1},
		{name: "canceled", request: csrfRequestWithAuthority(http.MethodPost, validToken).WithContext(canceledContext), limit: 1},
		{name: "missing authority", request: httptest.NewRequest(http.MethodPost, "/", nil), limit: 1},
		{name: "short authority", request: csrfRequestWithAuthority(http.MethodPost, "short"), limit: 1},
		{name: "invalid authority", request: csrfRequestWithAuthority(http.MethodPost, invalidToken), limit: 1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := validateCSRFRequest(test.request, test.limit); err == nil {
				t.Fatal("validateCSRFRequest() accepted invalid boundary state")
			}
		})
	}
}

func TestValidateCSRFRequestRejectsInvalidHeadersAndTokens(t *testing.T) {
	t.Parallel()

	validToken := validCSRFTokenForTest(0x44)
	otherToken := validCSRFTokenForTest(0x45)
	invalidToken := validToken[:len(validToken)-1] + "*"
	for _, values := range [][]string{
		{validToken, validToken},
		{""},
		{"short"},
		{invalidToken},
		{otherToken},
	} {
		values := values
		t.Run(strings.Join(values, ","), func(t *testing.T) {
			t.Parallel()
			request := csrfRequestWithAuthority(http.MethodDelete, validToken)
			request.Header[http.CanonicalHeaderKey(csrfHeaderName)] = values
			if err := validateCSRFRequest(request, 1024); err == nil {
				t.Fatal("validateCSRFRequest() accepted invalid header token")
			}
		})
	}
}

func TestValidateCSRFRequestRejectsInvalidFormsAndRestoresReadBodies(t *testing.T) {
	t.Parallel()

	validToken := validCSRFTokenForTest(0x46)
	otherToken := validCSRFTokenForTest(0x47)
	invalidToken := validToken[:len(validToken)-1] + "*"
	for _, test := range []struct {
		name          string
		contentType   string
		body          string
		contentLength int64
		limit         int64
	}{
		{name: "missing content type", body: "_csrf=" + validToken, limit: 1024},
		{name: "wrong content type", contentType: "multipart/form-data; boundary=x", body: "_csrf=" + validToken, limit: 1024},
		{name: "content type parameter", contentType: "application/x-www-form-urlencoded; charset=utf-8", body: "_csrf=" + validToken, limit: 1024},
		{name: "declared oversized", contentType: "application/x-www-form-urlencoded", body: "_csrf=" + validToken, contentLength: 1025, limit: 1024},
		{name: "streamed oversized", contentType: "application/x-www-form-urlencoded", body: strings.Repeat("a", 1025), limit: 1024},
		{name: "malformed encoding", contentType: "application/x-www-form-urlencoded", body: "_csrf=%zz", limit: 1024},
		{name: "missing token", contentType: "application/x-www-form-urlencoded", body: "title=hello", limit: 1024},
		{name: "duplicate token", contentType: "application/x-www-form-urlencoded", body: "_csrf=" + validToken + "&_csrf=" + validToken, limit: 1024},
		{name: "empty token", contentType: "application/x-www-form-urlencoded", body: "_csrf=", limit: 1024},
		{name: "short token", contentType: "application/x-www-form-urlencoded", body: "_csrf=short", limit: 1024},
		{name: "invalid token", contentType: "application/x-www-form-urlencoded", body: "_csrf=" + url.QueryEscape(invalidToken), limit: 1024},
		{name: "mismatch", contentType: "application/x-www-form-urlencoded", body: "_csrf=" + otherToken, limit: 1024},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := csrfRequestWithAuthority(http.MethodPut, validToken)
			request.Header.Set("Content-Type", test.contentType)
			request.Body = io.NopCloser(strings.NewReader(test.body))
			request.ContentLength = test.contentLength
			if err := validateCSRFRequest(request, test.limit); err == nil {
				t.Fatal("validateCSRFRequest() accepted invalid form")
			}
		})
	}
	request := csrfRequestWithAuthority(http.MethodPost, validToken)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Body = nil
	if err := validateCSRFRequest(request, 1024); err == nil {
		t.Fatal("validateCSRFRequest() accepted nil form body")
	}
}

func TestValidateCSRFRequestRejectsBodyReadAndCloseFailures(t *testing.T) {
	t.Parallel()

	validToken := validCSRFTokenForTest(0x48)
	for _, body := range []io.ReadCloser{
		&failingCSRFBody{readError: errors.New("read failed")},
		&failingCSRFBody{Reader: strings.NewReader("_csrf=" + validToken), closeError: errors.New("close failed")},
	} {
		request := csrfRequestWithAuthority(http.MethodPost, validToken)
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Body = body
		if err := validateCSRFRequest(request, 1024); err == nil {
			t.Fatal("validateCSRFRequest() accepted body lifecycle failure")
		}
	}
}

func csrfRequestWithAuthority(method, token string) *http.Request {
	request := httptest.NewRequest(method, "/mutation", nil)
	return request.WithContext(context.WithValue(request.Context(), csrfTokenContextKey{}, token))
}

func validCSRFTokenForTest(value byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{value}, sessionCookieTokenBytes))
}

type panicCSRFBody struct{}

func (panicCSRFBody) Read([]byte) (int, error) { panic("body must not be read") }
func (panicCSRFBody) Close() error             { panic("body must not be closed") }

type trackingCSRFBody struct {
	*bytes.Reader
	closed bool
}

func (body *trackingCSRFBody) Close() error {
	body.closed = true
	return nil
}

type failingCSRFBody struct {
	io.Reader
	readError  error
	closeError error
}

func (body *failingCSRFBody) Read(buffer []byte) (int, error) {
	if body.readError != nil {
		return 0, body.readError
	}
	return body.Reader.Read(buffer)
}

func (body *failingCSRFBody) Close() error { return body.closeError }
