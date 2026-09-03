package auth

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
)

type constructorSessionDatabase struct {
	db.DBTX
}

func (*constructorSessionDatabase) Begin(context.Context) (pgx.Tx, error) {
	panic("constructor must not begin a transaction")
}

func TestNewServiceOwnsValidatedLoginDependencies(t *testing.T) {
	harness := newOIDCExchangeHarness(t)
	defer harness.server.Close()
	issuer, err := url.Parse(harness.issuer)
	if err != nil {
		t.Fatalf("url.Parse() returned error: %v", err)
	}
	database := &constructorSessionDatabase{}
	entropy := bytes.NewReader(make([]byte, 512))
	clock := func() time.Time { return time.Date(2026, time.September, 1, 12, 30, 0, 0, time.UTC) }
	validator := func(raw string) (string, error) { return raw, nil }
	for _, maximumAge := range []time.Duration{time.Second, time.Second + time.Nanosecond, 24 * time.Hour} {
		service, err := NewService(
			context.Background(), harness.server.Client().Transport, *issuer, "gotth-bb", "client-secret",
			"https://forum.example/bb/auth/callback", database, entropy, clock, maximumAge, maximumAge, maximumAge, validator,
		)
		if err != nil {
			t.Fatalf("NewService(%s) returned error: %v", maximumAge, err)
		}
		if service == nil || service.provider.provider == nil || service.provider.verifier == nil || service.provider.httpClient == nil ||
			service.database != database || service.queries == nil || service.entropy != entropy || service.clock == nil ||
			service.sessionMaximumAge != maximumAge || service.sessionIdleTimeout != maximumAge || service.revalidationInterval != maximumAge ||
			service.validateReturnPath == nil {
			t.Fatalf("service dependencies are incomplete for %s", maximumAge)
		}
	}
}

func TestServiceFormattingRedactsAllRetainedDependencies(t *testing.T) {
	t.Parallel()

	const secret = "do-not-format-authentication-service"
	service := &Service{
		provider: discoveredOIDCProvider{},
		database: &constructorSessionDatabase{},
		entropy:  bytes.NewBufferString(secret),
	}
	got := fmt.Sprintf("%v|%+v|%#v|%s|%q", service, service, service, service, service)
	if strings.Contains(got, secret) || strings.Count(got, "[REDACTED AUTH SERVICE]") != 5 {
		t.Fatalf("formatted service = %q", got)
	}
}

func TestNewServiceRejectsInvalidLocalDependenciesBeforeDiscovery(t *testing.T) {
	t.Parallel()

	issuer := url.URL{Scheme: "https", Host: "auth.example", Path: "/application/o/gotth-bb/"}
	database := &constructorSessionDatabase{}
	entropy := bytes.NewReader(make([]byte, 512))
	clock := time.Now
	validator := func(raw string) (string, error) { return raw, nil }
	panicTransport := roundTripperFunc(func(*http.Request) (*http.Response, error) { panic("discovery must not run") })
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name      string
		ctx       context.Context
		database  SessionDatabase
		entropy   io.Reader
		clock     func() time.Time
		maxAge    time.Duration
		validator func(string) (string, error)
	}{
		{name: "nil context", database: database, entropy: entropy, clock: clock, maxAge: time.Hour, validator: validator},
		{name: "nil database", ctx: context.Background(), entropy: entropy, clock: clock, maxAge: time.Hour, validator: validator},
		{name: "nil entropy", ctx: context.Background(), database: database, clock: clock, maxAge: time.Hour, validator: validator},
		{name: "nil clock", ctx: context.Background(), database: database, entropy: entropy, maxAge: time.Hour, validator: validator},
		{name: "zero maximum age", ctx: context.Background(), database: database, entropy: entropy, clock: clock, validator: validator},
		{name: "negative maximum age", ctx: context.Background(), database: database, entropy: entropy, clock: clock, maxAge: -time.Second, validator: validator},
		{name: "below cookie precision", ctx: context.Background(), database: database, entropy: entropy, clock: clock, maxAge: time.Second - time.Nanosecond, validator: validator},
		{name: "nil validator", ctx: context.Background(), database: database, entropy: entropy, clock: clock, maxAge: time.Hour},
		{name: "canceled context", ctx: canceledContext, database: database, entropy: entropy, clock: clock, maxAge: time.Hour, validator: validator},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := NewService(
				test.ctx, panicTransport, issuer, "gotth-bb", "secret", "https://forum.example/bb/auth/callback",
				test.database, test.entropy, test.clock, test.maxAge, time.Minute, time.Minute, test.validator,
			)
			if err == nil || got != nil {
				t.Fatalf("NewService() = (%v, %v), want nil/error", got, err)
			}
		})
	}
}

func TestNewServiceReturnsNoPartialServiceWhenDiscoveryFails(t *testing.T) {
	t.Parallel()

	issuer := url.URL{Scheme: "https", Host: "auth.example", Path: "/application/o/gotth-bb/"}
	transport := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})
	got, err := NewService(
		context.Background(), transport, issuer, "gotth-bb", "secret", "https://forum.example/bb/auth/callback",
		&constructorSessionDatabase{}, bytes.NewReader(make([]byte, 512)), time.Now, time.Hour, time.Minute, time.Minute,
		func(raw string) (string, error) { return raw, nil },
	)
	if err == nil || got != nil {
		t.Fatalf("NewService() = (%v, %v), want nil/error", got, err)
	}
}

func TestNewServiceRejectsInvalidSessionPolicyBeforeDiscovery(t *testing.T) {
	t.Parallel()

	issuer := url.URL{Scheme: "https", Host: "auth.example", Path: "/application/o/gotth-bb/"}
	panicTransport := roundTripperFunc(func(*http.Request) (*http.Response, error) { panic("discovery must not run") })
	for _, test := range []struct {
		name       string
		maximumAge time.Duration
		idle       time.Duration
		revalidate time.Duration
	}{
		{name: "subsecond idle", maximumAge: time.Hour, idle: time.Second - time.Nanosecond, revalidate: time.Minute},
		{name: "idle exceeds maximum", maximumAge: time.Hour, idle: time.Hour + time.Nanosecond, revalidate: time.Minute},
		{name: "subsecond revalidation", maximumAge: time.Hour, idle: time.Minute, revalidate: time.Second - time.Nanosecond},
		{name: "revalidation exceeds maximum", maximumAge: time.Hour, idle: time.Minute, revalidate: time.Hour + time.Nanosecond},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service, err := NewService(
				context.Background(), panicTransport, issuer, "gotth-bb", "secret", "https://forum.example/bb/auth/callback",
				&constructorSessionDatabase{}, bytes.NewReader(make([]byte, 512)), time.Now,
				test.maximumAge, test.idle, test.revalidate, func(raw string) (string, error) { return raw, nil },
			)
			if err == nil || service != nil {
				t.Fatalf("NewService() = (%v, %v), want nil/error", service, err)
			}
		})
	}
}
