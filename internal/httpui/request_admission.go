package httpui

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"unicode"

	"github.com/gotthboard/gotth-bb/internal/abuse"
	"github.com/gotthboard/gotth-bb/internal/observability"
)

// NewRequestAdmissionHandler applies the closed health/static exemption and
// charges every other request before session, body, router, or database work.
func NewRequestAdmissionHandler(next http.Handler, limiter *abuse.RequestLimiter, observer abuse.Observer, production bool) (http.Handler, error) {
	if next == nil || limiter == nil || observer == nil {
		return nil, fmt.Errorf("request admission dependencies are required")
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if requestAdmissionExempt(request.Method, request.URL.Path) {
			next.ServeHTTP(response, request)
			return
		}
		request.Pattern = "request-admission"
		address, err := admittedClientAddress(request, production)
		if err != nil {
			writeAdmissionFailure(response, http.StatusBadRequest, 0, "bad request")
			return
		}
		decision, retry := limiter.Admit(address)
		if decision == abuse.RequestAllowed {
			next.ServeHTTP(response, request)
			return
		}
		status := http.StatusTooManyRequests
		class := abuse.RejectionRequestRate
		message := "too many requests"
		if decision == abuse.RequestCapacityLimited {
			status = http.StatusServiceUnavailable
			class = abuse.RejectionRequestCapacity
			message = "service unavailable"
		}
		seconds := int64(retry.Seconds())
		writeAdmissionFailure(response, status, seconds, message)
		requestID, _ := observability.RequestID(request.Context())
		observer.Observe(request.Context(), abuse.Event{
			Class: class, Route: abuse.RouteRequestAdmission, RequestID: requestID,
			Status: status, RetrySeconds: seconds,
		})
	}), nil
}

func admittedClientAddress(request *http.Request, production bool) (netip.Addr, error) {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid peer")
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || peer.Zone() != "" || peer.String() != host {
		return netip.Addr{}, fmt.Errorf("invalid peer")
	}
	peer = peer.Unmap()
	forwarded := request.Header.Values("X-Forwarded-For")
	if len(request.Header.Values("Forwarded")) != 0 || len(request.Header.Values("X-Real-IP")) != 0 {
		return netip.Addr{}, fmt.Errorf("alternative forwarded identity is forbidden")
	}
	if !production {
		if len(forwarded) != 0 {
			return netip.Addr{}, fmt.Errorf("forwarded identity is forbidden")
		}
		return peer, nil
	}
	if !peer.IsLoopback() || len(forwarded) != 1 {
		return netip.Addr{}, fmt.Errorf("trusted proxy identity is unavailable")
	}
	raw := forwarded[0]
	if raw == "" || strings.Contains(raw, ",") || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return netip.Addr{}, fmt.Errorf("forwarded identity is ambiguous")
	}
	address, err := netip.ParseAddr(raw)
	if err != nil || address.Zone() != "" || address.String() != raw {
		return netip.Addr{}, fmt.Errorf("forwarded identity is invalid")
	}
	return address.Unmap(), nil
}

func requestAdmissionExempt(method, path string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	switch path {
	case "/health/live", "/health/ready",
		"/static/" + appStylesheetFilename,
		"/static/htmx-2.0.10.min.js",
		"/static/" + discoveryResponseFilename,
		"/static/" + markdownToolbarFilename:
		return true
	default:
		return false
	}
}

func writeAdmissionFailure(response http.ResponseWriter, status int, retrySeconds int64, message string) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if retrySeconds > 0 {
		response.Header().Set("Retry-After", strconv.FormatInt(retrySeconds, 10))
	}
	response.WriteHeader(status)
	_, _ = response.Write([]byte(message + "\n"))
}
