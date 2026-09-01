package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/config"
	"git.dannyhunn.com/agents/gotth-bb/internal/httpui"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type notifyingListener struct {
	net.Listener
	once      sync.Once
	accepting chan struct{}
}

type closeFailingListener struct {
	net.Listener
	err error
}

type fakeDatabasePool struct {
	mutex  sync.Mutex
	closes int
}

func (pool *fakeDatabasePool) Close() {
	pool.mutex.Lock()
	defer pool.mutex.Unlock()
	pool.closes++
}

func (pool *fakeDatabasePool) closeCount() int {
	pool.mutex.Lock()
	defer pool.mutex.Unlock()
	return pool.closes
}

func (*fakeDatabasePool) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("database execution is not expected")
}

func (*fakeDatabasePool) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("database query is not expected")
}

func (*fakeDatabasePool) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("database query is not expected")
}

func (*fakeDatabasePool) Begin(context.Context) (pgx.Tx, error) {
	panic("database transaction is not expected")
}

func returnPool(pool databasePool) poolFactory {
	return func(context.Context, *pgxpool.Config) (databasePool, error) {
		return pool, nil
	}
}

func validAuthenticationFactory() authenticationFactory {
	return func(context.Context, config.Config, auth.SessionDatabase, httpui.URLBuilder) (httpui.AuthenticationService, error) {
		return fakeAuthenticationService{}, nil
	}
}

type fakeAuthenticationService struct{}

func (fakeAuthenticationService) BeginInitialLogin(context.Context, string) (string, string, error) {
	const state = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	return "https://auth.example/authorize?state=" + state, state, nil
}

func (fakeAuthenticationService) BeginRevalidation(context.Context, int64, string) (string, string, error) {
	return "", "", errors.New("revalidation is not expected")
}

func (fakeAuthenticationService) CompleteInitialLogin(context.Context, string, string) (string, string, time.Time, error) {
	return "", "", time.Time{}, errors.New("callback is not expected")
}

func (fakeAuthenticationService) AuthenticateSession(context.Context, string) (auth.SessionAuthentication, error) {
	return auth.SessionAuthentication{}, nil
}

func (fakeAuthenticationService) RevokeSession(context.Context, string) (bool, error) {
	return false, nil
}

func (listener *notifyingListener) Accept() (net.Conn, error) {
	listener.once.Do(func() { close(listener.accepting) })
	return listener.Listener.Accept()
}

func (listener *closeFailingListener) Close() error {
	return listener.err
}

func TestRunStartsAndStopsWithValidatedConfiguration(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() returned error: %v", err)
	}
	accepting := make(chan struct{})
	wrapped := &notifyingListener{Listener: listener, accepting: accepting}
	values := validEnvironment("127.0.0.1:8080")
	ctx, cancel := context.WithCancel(context.Background())
	var logs bytes.Buffer
	pool := &fakeDatabasePool{}
	authenticationCalls := 0
	authenticationReceivedPool := false
	authenticationCookiePath := ""
	authentication := func(_ context.Context, _ config.Config, database auth.SessionDatabase, builder httpui.URLBuilder) (httpui.AuthenticationService, error) {
		authenticationCalls++
		authenticationReceivedPool = database == pool
		authenticationCookiePath, _ = builder.CookiePath()
		return fakeAuthenticationService{}, nil
	}
	result := make(chan error, 1)
	go func() {
		result <- run(ctx, mapLookup(values), &logs, returnPool(pool), authentication, func(string, string) (net.Listener, error) {
			return wrapped, nil
		})
	}()
	select {
	case <-accepting:
	case <-time.After(time.Second):
		t.Fatal("server did not begin accepting")
	}
	if authenticationCalls != 1 || !authenticationReceivedPool || authenticationCookiePath != "/bb/" {
		t.Fatalf("authentication wiring = (calls %d, pool %t, cookie path %q)", authenticationCalls, authenticationReceivedPool, authenticationCookiePath)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get("http://" + listener.Addr().String() + "/login")
	if err != nil {
		t.Fatalf("GET /login returned error: %v", err)
	}
	_ = response.Body.Close()
	cookies := response.Cookies()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "https://auth.example/authorize?state=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" ||
		len(cookies) != 1 || cookies[0].Name != "gotth_bb_session_oidc_state" || cookies[0].Path != "/bb/" || cookies[0].Secure {
		t.Fatalf("GET /login = (status %d, location %q)", response.StatusCode, response.Header.Get("Location"))
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("run() returned error: %v", err)
	}
	if !strings.Contains(logs.String(), `"msg":"service stopped"`) {
		t.Fatalf("lifecycle logs = %q", logs.String())
	}
	if pool.closeCount() != 1 {
		t.Fatalf("database pool close count = %d, want 1", pool.closeCount())
	}
}

func TestRunRejectsInvalidDependenciesAndRedactsConfigFailure(t *testing.T) {
	const secret = "do-not-expose-service-secret"
	validPool := returnPool(&fakeDatabasePool{})
	validAuthentication := validAuthenticationFactory()
	if err := run(nil, mapLookup(validEnvironment("127.0.0.1:8080")), &bytes.Buffer{}, validPool, validAuthentication, net.Listen); err == nil {
		t.Fatal("run(nil, lookup, output) accepted nil context")
	}
	if err := run(context.Background(), mapLookup(validEnvironment("127.0.0.1:8080")), nil, validPool, validAuthentication, net.Listen); err == nil {
		t.Fatal("run(context, lookup, nil) accepted nil output")
	}
	values := validEnvironment("127.0.0.1:8080")
	values["OIDC_ISSUER_URL"] = "https://" + secret + "%zz.example.com/application/o/gotth-bb/"
	if err := run(context.Background(), mapLookup(validEnvironment("127.0.0.1:8080")), &bytes.Buffer{}, validPool, validAuthentication, nil); err == nil {
		t.Fatal("run(context, lookup, output, nil) accepted nil listener factory")
	}
	if err := run(context.Background(), mapLookup(validEnvironment("127.0.0.1:8080")), &bytes.Buffer{}, nil, validAuthentication, net.Listen); err == nil {
		t.Fatal("run(context, lookup, output, nil, listener) accepted nil pool factory")
	}
	if err := run(context.Background(), mapLookup(validEnvironment("127.0.0.1:8080")), &bytes.Buffer{}, validPool, nil, net.Listen); err == nil {
		t.Fatal("run(context, lookup, output, pool, nil, listener) accepted nil authentication factory")
	}
	err := run(context.Background(), mapLookup(values), &bytes.Buffer{}, validPool, validAuthentication, net.Listen)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("run() error = %v", err)
	}
	values = validEnvironment("127.0.0.1:8080")
	values["DATABASE_URL"] = "postgres://" + secret + "%zz"
	err = run(context.Background(), mapLookup(values), &bytes.Buffer{}, validPool, validAuthentication, net.Listen)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("run() database configuration error = %v", err)
	}
}

func TestRunReportsListenFailure(t *testing.T) {
	values := validEnvironment("127.0.0.1:8080")
	pool := &fakeDatabasePool{}
	err := run(context.Background(), mapLookup(values), &bytes.Buffer{}, returnPool(pool), validAuthenticationFactory(), func(string, string) (net.Listener, error) {
		return nil, errors.New("bind failed")
	})
	if err == nil || !strings.Contains(err.Error(), "listen for HTTP") {
		t.Fatalf("run() error = %v", err)
	}
	if pool.closeCount() != 1 {
		t.Fatalf("database pool close count = %d, want 1", pool.closeCount())
	}
}

func TestRunRejectsInvalidPoolResultsWithoutLeakingCause(t *testing.T) {
	const secret = "do-not-expose-pool-open-cause"
	values := validEnvironment("127.0.0.1:8080")
	failed := func(context.Context, *pgxpool.Config) (databasePool, error) {
		return nil, errors.New(secret)
	}
	if err := run(context.Background(), mapLookup(values), &bytes.Buffer{}, failed, validAuthenticationFactory(), net.Listen); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("run() pool-open error = %v", err)
	}
	empty := func(context.Context, *pgxpool.Config) (databasePool, error) {
		return nil, nil
	}
	if err := run(context.Background(), mapLookup(values), &bytes.Buffer{}, empty, validAuthenticationFactory(), net.Listen); err == nil {
		t.Fatal("run() accepted a nil database pool")
	}
	returned := &fakeDatabasePool{}
	returnedWithError := func(context.Context, *pgxpool.Config) (databasePool, error) {
		return returned, errors.New(secret)
	}
	if err := run(context.Background(), mapLookup(values), &bytes.Buffer{}, returnedWithError, validAuthenticationFactory(), net.Listen); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("run() pool-and-error result = %v", err)
	}
	if returned.closeCount() != 1 {
		t.Fatalf("failed database pool close count = %d, want 1", returned.closeCount())
	}
	ctx, cancel := context.WithCancel(context.Background())
	canceled := func(context.Context, *pgxpool.Config) (databasePool, error) {
		cancel()
		return nil, errors.New(secret)
	}
	if err := run(ctx, mapLookup(values), &bytes.Buffer{}, canceled, validAuthenticationFactory(), net.Listen); !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), secret) {
		t.Fatalf("run() canceled pool-open error = %v", err)
	}
}

func TestRunClosesPoolAndRedactsAuthenticationConstructionFailures(t *testing.T) {
	const secret = "do-not-expose-authentication-construction-cause"
	for _, test := range []struct {
		name    string
		factory authenticationFactory
		cancel  bool
	}{
		{name: "failure", factory: func(context.Context, config.Config, auth.SessionDatabase, httpui.URLBuilder) (httpui.AuthenticationService, error) {
			return nil, errors.New(secret)
		}},
		{name: "empty service", factory: func(context.Context, config.Config, auth.SessionDatabase, httpui.URLBuilder) (httpui.AuthenticationService, error) {
			return nil, nil
		}},
		{name: "cancellation", cancel: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			factory := test.factory
			if test.cancel {
				factory = func(context.Context, config.Config, auth.SessionDatabase, httpui.URLBuilder) (httpui.AuthenticationService, error) {
					cancel()
					return nil, errors.New(secret)
				}
			}
			pool := &fakeDatabasePool{}
			listened := false
			err := run(ctx, mapLookup(validEnvironment("127.0.0.1:8080")), &bytes.Buffer{}, returnPool(pool), factory, func(string, string) (net.Listener, error) {
				listened = true
				return nil, errors.New("must not listen")
			})
			if err == nil || strings.Contains(err.Error(), secret) || listened || pool.closeCount() != 1 {
				t.Fatalf("run() = (error %v, listened %t, pool closes %d)", err, listened, pool.closeCount())
			}
			if test.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled run error = %v", err)
			}
		})
	}
}

func TestRunClosesPoolWhenStartupIsCanceledAfterOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pool := &fakeDatabasePool{}
	openAndCancel := func(context.Context, *pgxpool.Config) (databasePool, error) {
		cancel()
		return pool, nil
	}
	listened := false
	err := run(ctx, mapLookup(validEnvironment("127.0.0.1:8080")), &bytes.Buffer{}, openAndCancel, validAuthenticationFactory(), func(string, string) (net.Listener, error) {
		listened = true
		return nil, errors.New("must not listen")
	})
	if !errors.Is(err, context.Canceled) || listened {
		t.Fatalf("run() error = %v, listener called = %v", err, listened)
	}
	if pool.closeCount() != 1 {
		t.Fatalf("database pool close count = %d, want 1", pool.closeCount())
	}
}

func TestRunDoesNotBindAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := run(ctx, mapLookup(validEnvironment("127.0.0.1:8080")), &bytes.Buffer{}, returnPool(&fakeDatabasePool{}), validAuthenticationFactory(), func(string, string) (net.Listener, error) {
		called = true
		return nil, errors.New("must not bind")
	})
	if err == nil || !strings.Contains(err.Error(), "startup canceled") || called {
		t.Fatalf("run() error = %v, listener called = %v", err, called)
	}
}

func TestRunClosesListenerWhenCanceledDuringBind(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() returned error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err = run(ctx, mapLookup(validEnvironment("127.0.0.1:8080")), &bytes.Buffer{}, returnPool(&fakeDatabasePool{}), validAuthenticationFactory(), func(string, string) (net.Listener, error) {
		cancel()
		return listener, nil
	})
	if err == nil || !strings.Contains(err.Error(), "startup canceled") {
		t.Fatalf("run() error = %v", err)
	}
	if closeErr := listener.Close(); !errors.Is(closeErr, net.ErrClosed) {
		t.Fatalf("listener remained open after cancellation: %v", closeErr)
	}
}

func TestRunReportsListenerCloseFailureAfterCancellationDuringBind(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() returned error: %v", err)
	}
	defer func() { _ = listener.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	err = run(ctx, mapLookup(validEnvironment("127.0.0.1:8080")), &bytes.Buffer{}, returnPool(&fakeDatabasePool{}), validAuthenticationFactory(), func(string, string) (net.Listener, error) {
		cancel()
		return &closeFailingListener{Listener: listener, err: errors.New("close failed")}, nil
	})
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "close canceled listener") {
		t.Fatalf("run() error = %v", err)
	}
}

func TestRunReturnsHTTPServeFailureAndClosesPool(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() returned error: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("listener.Close() returned error: %v", err)
	}
	pool := &fakeDatabasePool{}
	err = run(context.Background(), mapLookup(validEnvironment("127.0.0.1:8080")), &bytes.Buffer{}, returnPool(pool), validAuthenticationFactory(), func(string, string) (net.Listener, error) {
		return listener, nil
	})
	if err == nil || pool.closeCount() != 1 {
		t.Fatalf("run() = (error %v, pool closes %d)", err, pool.closeCount())
	}
}

func validEnvironment(listenAddress string) map[string]string {
	return map[string]string{
		"APP_ENV":                  "test",
		"LISTEN_ADDR":              listenAddress,
		"PUBLIC_BASE_URL":          "http://127.0.0.1:8080/bb",
		"BASE_PATH":                "/bb",
		"DATABASE_URL":             "postgres://gotth:database-password@127.0.0.1/gotth_bb",
		"OIDC_ISSUER_URL":          "http://127.0.0.1:9000/application/o/gotth-bb/",
		"OIDC_CLIENT_ID":           "gotth-bb",
		"SESSION_MAX_AGE":          "24h",
		"SESSION_IDLE_TIMEOUT":     "30m",
		"AUTH_REVALIDATE_INTERVAL": "15m",
		"LOG_LEVEL":                "debug",
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
