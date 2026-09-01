package httpui

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
)

const (
	csrfHeaderName                 = "X-CSRF-Token"
	csrfFormFieldName              = "_csrf"
	maximumCSRFValidationFormBytes = 1 << 20
)

// validateCSRFRequest verifies one authenticated unsafe request against the
// session-bound synchronizer token. HTMX submits exactly one header without
// body inspection. Ordinary forms submit exactly one field in a route-bounded
// URL-encoded body that is restored byte-for-byte for later handler parsing.
// Token comparison is fixed-length and constant-time; errors expose no token or
// body bytes.
//
// Complexity: header validation is tight Theta(1) time and auxiliary space over
// fixed 43/32-byte tokens. For an n-byte form body (n <= maximumFormBytes <=
// 1 MiB) with f fields, time is O(n+f) and Omega(n), while auxiliary space is
// O(n+f), Omega(n), because the bounded body and parsed values are retained for
// validation and body restoration. No I/O is retried or detached.
func validateCSRFRequest(request *http.Request, maximumFormBytes int64) error {
	if request == nil {
		return fmt.Errorf("CSRF request is required")
	}
	if maximumFormBytes < 1 || maximumFormBytes > maximumCSRFValidationFormBytes {
		return fmt.Errorf("CSRF form limit is outside the supported bound")
	}
	switch request.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return fmt.Errorf("CSRF validation requires an unsafe HTTP method")
	}
	if err := request.Context().Err(); err != nil {
		return fmt.Errorf("validate CSRF request: %w", err)
	}
	expected := csrfTokenFromContext(request.Context())
	if len(expected) != sessionCookieEncodedBytes {
		return fmt.Errorf("CSRF authority is missing or malformed")
	}
	var expectedEncoded [sessionCookieEncodedBytes]byte
	copy(expectedEncoded[:], expected)
	defer clear(expectedEncoded[:])
	var expectedDecoded [sessionCookieTokenBytes]byte
	expectedLength, err := base64.RawURLEncoding.Strict().Decode(expectedDecoded[:], expectedEncoded[:])
	defer clear(expectedDecoded[:])
	if err != nil || expectedLength != sessionCookieTokenBytes {
		return fmt.Errorf("CSRF authority is missing or malformed")
	}
	headerValues := request.Header.Values(csrfHeaderName)
	if len(headerValues) > 1 {
		return fmt.Errorf("CSRF header is ambiguous")
	}
	submitted := ""
	if len(headerValues) == 1 {
		submitted = headerValues[0]
	} else {
		mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/x-www-form-urlencoded" || len(parameters) != 0 {
			return fmt.Errorf("CSRF form content type is invalid")
		}
		if request.Body == nil {
			return fmt.Errorf("CSRF form body is required")
		}
		if request.ContentLength > maximumFormBytes {
			return fmt.Errorf("CSRF form body exceeds its limit")
		}
		body, readErr := io.ReadAll(io.LimitReader(request.Body, maximumFormBytes+1))
		closeErr := request.Body.Close()
		request.Body = io.NopCloser(bytes.NewReader(body))
		if readErr != nil {
			return fmt.Errorf("read CSRF form body failed")
		}
		if closeErr != nil {
			return fmt.Errorf("close CSRF form body failed")
		}
		if int64(len(body)) > maximumFormBytes {
			return fmt.Errorf("CSRF form body exceeds its limit")
		}
		form, err := url.ParseQuery(string(body))
		tokens, present := form[csrfFormFieldName]
		if err != nil || !present || len(tokens) != 1 {
			return fmt.Errorf("CSRF form token is missing or ambiguous")
		}
		submitted = tokens[0]
	}
	if len(submitted) != sessionCookieEncodedBytes {
		return fmt.Errorf("CSRF token is malformed")
	}
	var submittedEncoded [sessionCookieEncodedBytes]byte
	copy(submittedEncoded[:], submitted)
	defer clear(submittedEncoded[:])
	var submittedDecoded [sessionCookieTokenBytes]byte
	submittedLength, err := base64.RawURLEncoding.Strict().Decode(submittedDecoded[:], submittedEncoded[:])
	defer clear(submittedDecoded[:])
	if err != nil || submittedLength != sessionCookieTokenBytes {
		return fmt.Errorf("CSRF token is malformed")
	}
	if subtle.ConstantTimeCompare(submittedEncoded[:], expectedEncoded[:]) != 1 {
		return fmt.Errorf("CSRF token does not match the authenticated session")
	}
	return nil
}
