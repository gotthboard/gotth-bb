package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type notifyingListener struct {
	net.Listener
	once      sync.Once
	accepting chan struct{}
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

func returnPool(pool databasePool) poolFactory {
	return func(context.Context, *pgxpool.Config) (databasePool, error) {
		return pool, nil
	}
}

func (listener *notifyingListener) Accept() (net.Conn, error) {
	listener.once.Do(func() { close(listener.accepting) })
	return listener.Listener.Accept()
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
	result := make(chan error, 1)
	go func() {
		result <- run(ctx, mapLookup(values), &logs, returnPool(pool), func(string, string) (net.Listener, error) {
			return wrapped, nil
		})
	}()
	select {
	case <-accepting:
	case <-time.After(time.Second):
		t.Fatal("server did not begin accepting")
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
	if err := run(nil, mapLookup(validEnvironment("127.0.0.1:8080")), &bytes.Buffer{}, validPool, net.Listen); err == nil {
		t.Fatal("run(nil, lookup, output) accepted nil context")
	}
	if err := run(context.Background(), mapLookup(validEnvironment("127.0.0.1:8080")), nil, validPool, net.Listen); err == nil {
		t.Fatal("run(context, lookup, nil) accepted nil output")
	}
	values := validEnvironment("127.0.0.1:8080")
	values["OIDC_ISSUER_URL"] = "https://" + secret + "%zz.example.com/application/o/gotth-bb/"
	if err := run(context.Background(), mapLookup(validEnvironment("127.0.0.1:8080")), &bytes.Buffer{}, validPool, nil); err == nil {
		t.Fatal("run(context, lookup, output, nil) accepted nil listener factory")
	}
	if err := run(context.Background(), mapLookup(validEnvironment("127.0.0.1:8080")), &bytes.Buffer{}, nil, net.Listen); err == nil {
		t.Fatal("run(context, lookup, output, nil, listener) accepted nil pool factory")
	}
	err := run(context.Background(), mapLookup(values), &bytes.Buffer{}, validPool, net.Listen)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("run() error = %v", err)
	}
	values = validEnvironment("127.0.0.1:8080")
	values["DATABASE_URL"] = "postgres://" + secret + "%zz"
	err = run(context.Background(), mapLookup(values), &bytes.Buffer{}, validPool, net.Listen)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("run() database configuration error = %v", err)
	}
}

func TestRunReportsListenFailure(t *testing.T) {
	values := validEnvironment("127.0.0.1:8080")
	pool := &fakeDatabasePool{}
	err := run(context.Background(), mapLookup(values), &bytes.Buffer{}, returnPool(pool), func(string, string) (net.Listener, error) {
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
	if err := run(context.Background(), mapLookup(values), &bytes.Buffer{}, failed, net.Listen); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("run() pool-open error = %v", err)
	}
	empty := func(context.Context, *pgxpool.Config) (databasePool, error) {
		return nil, nil
	}
	if err := run(context.Background(), mapLookup(values), &bytes.Buffer{}, empty, net.Listen); err == nil {
		t.Fatal("run() accepted a nil database pool")
	}
	returned := &fakeDatabasePool{}
	returnedWithError := func(context.Context, *pgxpool.Config) (databasePool, error) {
		return returned, errors.New(secret)
	}
	if err := run(context.Background(), mapLookup(values), &bytes.Buffer{}, returnedWithError, net.Listen); err == nil || strings.Contains(err.Error(), secret) {
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
	if err := run(ctx, mapLookup(values), &bytes.Buffer{}, canceled, net.Listen); !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), secret) {
		t.Fatalf("run() canceled pool-open error = %v", err)
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
	err := run(ctx, mapLookup(validEnvironment("127.0.0.1:8080")), &bytes.Buffer{}, openAndCancel, func(string, string) (net.Listener, error) {
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
	err := run(ctx, mapLookup(validEnvironment("127.0.0.1:8080")), &bytes.Buffer{}, returnPool(&fakeDatabasePool{}), func(string, string) (net.Listener, error) {
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
	err = run(ctx, mapLookup(validEnvironment("127.0.0.1:8080")), &bytes.Buffer{}, returnPool(&fakeDatabasePool{}), func(string, string) (net.Listener, error) {
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
