package authentikgateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestBoardClientUsesOnlyFixedGatewayOperation(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "http" || request.URL.Host != fixedHost || request.Host != fixedHost || request.Method != http.MethodPost || request.URL.Path != "/v1/groups/accepted/add" || request.URL.RawQuery != "" || request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			t.Fatalf("unexpected request: %s %s host=%q", request.Method, request.URL.String(), request.Host)
		}
		body, _ := io.ReadAll(request.Body)
		if string(body) != `{"user_uuid":"`+testUser+`"}` {
			t.Fatalf("unexpected body: %q", body)
		}
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}}
	if err := client.AddUser(context.Background(), "accepted", testUser); err != nil {
		t.Fatal(err)
	}
	if err := client.AddUser(context.Background(), "other", testUser); err != ErrInvalidRequest {
		t.Fatalf("unpinned group error = %v", err)
	}
}

func TestBoardClientRequiresUnixPathAndExactErrors(t *testing.T) {
	for _, path := range []string{"", "127.0.0.1:8080", "/tmp/other.sock", "/tmp/../tmp/authentik-control.sock"} {
		if _, err := NewClient(path); err == nil {
			t.Fatalf("unsafe path accepted: %q", path)
		}
	}
	client := &Client{http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		headers := make(http.Header)
		headers.Set("Content-Type", "application/json")
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("{\"error\":\"remote_unavailable\"}\n")), Header: headers}, nil
	})}}
	if err := client.Health(context.Background()); err != ErrRemoteUnavailable {
		t.Fatalf("exact error = %v", err)
	}
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("leaked remote body")), Header: make(http.Header)}, nil
	})
	if err := client.Health(context.Background()); err != ErrRemoteInvalid {
		t.Fatalf("noncanonical error = %v", err)
	}
}
