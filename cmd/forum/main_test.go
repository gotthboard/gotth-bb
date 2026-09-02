package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/config"
	"git.dannyhunn.com/agents/gotth-bb/internal/governance"
	"git.dannyhunn.com/agents/gotth-bb/internal/httpui"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"git.dannyhunn.com/agents/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewLoggedInitialAdministratorClaimerReportsOnlyFailures(t *testing.T) {
	t.Parallel()
	authentication := auth.SessionAuthentication{
		SessionID: 91,
		Access:    auth.AccessContext{Authenticated: true, UserID: 41, Role: auth.RoleMember},
	}
	requestID := pgtype.UUID{Bytes: [16]byte{0x61}, Valid: true}
	wantResult := governance.InitialAdministratorClaimResult{UserID: 41, AuditID: 73, RevokedSessionID: 91}
	cause := errors.New("database lock failed")
	for _, test := range []struct {
		name      string
		claimErr  error
		wantError bool
		wantLog   bool
	}{
		{name: "success"},
		{name: "failure", claimErr: cause, wantError: true, wantLog: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			calls := 0
			claimer, err := newLoggedInitialAdministratorClaimer(logger, func(_ context.Context, gotAuthentication auth.SessionAuthentication, gotRequestID pgtype.UUID) (governance.InitialAdministratorClaimResult, error) {
				calls++
				if !reflect.DeepEqual(gotAuthentication, authentication) || gotRequestID != requestID {
					t.Fatalf("claim input = (%+v, %+v)", gotAuthentication, gotRequestID)
				}
				return wantResult, test.claimErr
			})
			if err != nil {
				t.Fatalf("newLoggedInitialAdministratorClaimer() returned error: %v", err)
			}
			gotResult, gotErr := claimer(context.Background(), authentication, requestID)
			if gotResult != wantResult || calls != 1 || errors.Is(gotErr, cause) != test.wantError {
				t.Fatalf("claim result = (%+v, %v, calls %d)", gotResult, gotErr, calls)
			}
			logged := logs.String()
			if strings.Contains(logged, "database lock failed") != test.wantLog || strings.Contains(logged, `"user_id"`) || strings.Contains(logged, `"session_id"`) {
				t.Fatalf("claim log = %q", logged)
			}
			if test.wantLog && !strings.Contains(logged, `"msg":"initial administrator claim failed"`) {
				t.Fatalf("claim failure log = %q", logged)
			}
		})
	}
}

func TestNewLoggedInitialAdministratorClaimerRejectsMissingDependencies(t *testing.T) {
	t.Parallel()
	valid := func(context.Context, auth.SessionAuthentication, pgtype.UUID) (governance.InitialAdministratorClaimResult, error) {
		return governance.InitialAdministratorClaimResult{}, nil
	}
	if claimer, err := newLoggedInitialAdministratorClaimer(nil, valid); err == nil || claimer != nil {
		t.Fatalf("nil logger = (%v, %v)", claimer, err)
	}
	if claimer, err := newLoggedInitialAdministratorClaimer(slog.Default(), nil); err == nil || claimer != nil {
		t.Fatalf("nil claimer = (%v, %v)", claimer, err)
	}
}

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
	mutex               sync.Mutex
	closes              int
	areaQueryCalls      int
	areaQueryArgs       []any
	topicPostQueryCalls int
	topicPostQueryArgs  []any
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

func (pool *fakeDatabasePool) areaQuerySnapshot() (int, []any) {
	pool.mutex.Lock()
	defer pool.mutex.Unlock()
	return pool.areaQueryCalls, append([]any(nil), pool.areaQueryArgs...)
}

func (pool *fakeDatabasePool) topicPostQuerySnapshot() (int, []any) {
	pool.mutex.Lock()
	defer pool.mutex.Unlock()
	return pool.topicPostQueryCalls, append([]any(nil), pool.topicPostQueryArgs...)
}

func (*fakeDatabasePool) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("database execution is not expected")
}

func (pool *fakeDatabasePool) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	pool.mutex.Lock()
	defer pool.mutex.Unlock()
	switch {
	case strings.Contains(query, "FROM public.areas AS a"):
		pool.areaQueryCalls++
		pool.areaQueryArgs = append([]any(nil), args...)
		return &fakeAreaRows{}, nil
	case strings.Contains(query, "WITH visible_topic AS"):
		pool.topicPostQueryCalls++
		pool.topicPostQueryArgs = append([]any(nil), args...)
		return &fakeTopicPostRows{}, nil
	case strings.Contains(query, "FROM public.gotth_schema_migrations"):
		return newFakeMigrationRows(), nil
	default:
		panic("unexpected database query")
	}
}

type fakeAreaRows struct {
	pgx.Rows
	yielded bool
}

func (*fakeAreaRows) Close() {}

func (rows *fakeAreaRows) Next() bool {
	if rows.yielded {
		return false
	}
	rows.yielded = true
	return true
}

func (*fakeAreaRows) Scan(destinations ...any) error {
	*(destinations[0].(*int64)) = 7
	*(destinations[1].(*string)) = "public"
	*(destinations[2].(*string)) = "Public area"
	*(destinations[3].(*string)) = "Visible to everyone"
	*(destinations[4].(*int32)) = 1
	*(destinations[5].(*string)) = "public"
	*(destinations[6].(*string)) = "normal"
	*(destinations[7].(*int64)) = 3
	*(destinations[8].(*int64)) = 3
	*(destinations[9].(*pgtype.Timestamptz)) = pgtype.Timestamptz{Valid: true}
	*(destinations[10].(*pgtype.Timestamptz)) = pgtype.Timestamptz{Valid: true}
	return nil
}

func (*fakeAreaRows) Err() error { return nil }

type fakeTopicPostRows struct {
	pgx.Rows
	yielded bool
}

func (*fakeTopicPostRows) Close() {}

func (rows *fakeTopicPostRows) Next() bool {
	if rows.yielded {
		return false
	}
	rows.yielded = true
	return true
}

func (*fakeTopicPostRows) Scan(destinations ...any) error {
	created := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	*(destinations[0].(*int64)) = 7
	*(destinations[1].(*string)) = "public"
	*(destinations[2].(*string)) = "Public area"
	*(destinations[3].(*string)) = "Visible to everyone"
	*(destinations[4].(*string)) = "normal"
	*(destinations[5].(*int64)) = 42
	*(destinations[6].(*int64)) = 101
	*(destinations[7].(*string)) = "First topic"
	*(destinations[8].(*string)) = "open"
	*(destinations[9].(*pgtype.Timestamptz)) = pgtype.Timestamptz{}
	*(destinations[10].(*pgtype.Timestamptz)) = pgtype.Timestamptz{Time: created, Valid: true}
	*(destinations[11].(*string)) = "Starter"
	*(destinations[12].(*pgtype.Int8)) = pgtype.Int8{Int64: 101, Valid: true}
	*(destinations[13].(*pgtype.Int4)) = pgtype.Int4{Int32: 1, Valid: true}
	*(destinations[14].(*pgtype.Text)) = pgtype.Text{String: "<p>Hello <strong>forum</strong></p>", Valid: true}
	*(destinations[15].(*pgtype.Text)) = pgtype.Text{String: "test-v1", Valid: true}
	*(destinations[16].(*pgtype.Int4)) = pgtype.Int4{Int32: 1, Valid: true}
	*(destinations[17].(*pgtype.Timestamptz)) = pgtype.Timestamptz{Time: created, Valid: true}
	*(destinations[18].(*pgtype.Timestamptz)) = pgtype.Timestamptz{Time: created, Valid: true}
	*(destinations[19].(*pgtype.Timestamptz)) = pgtype.Timestamptz{}
	*(destinations[20].(*pgtype.Int8)) = pgtype.Int8{Int64: 11, Valid: true}
	*(destinations[21].(*pgtype.Text)) = pgtype.Text{String: "Starter", Valid: true}
	*(destinations[22].(*int64)) = 1
	return nil
}

func (*fakeTopicPostRows) Err() error { return nil }

func (*fakeDatabasePool) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	switch {
	case strings.Contains(query, "pg_catalog.pg_class"):
		return fakeBooleanRow(true)
	case strings.Contains(query, "FROM public.governance_state"):
		return fakeBooleanRow(true)
	default:
		panic("unexpected database row query")
	}
}

func (*fakeDatabasePool) Begin(context.Context) (pgx.Tx, error) {
	panic("database transaction is not expected")
}

type fakeBooleanRow bool

func (row fakeBooleanRow) Scan(destinations ...any) error {
	*(destinations[0].(*bool)) = bool(row)
	return nil
}

type fakeMigrationRow struct {
	version int64
	name    string
	digest  [sha256.Size]byte
}

type fakeMigrationRows struct {
	pgx.Rows
	rows  []fakeMigrationRow
	index int
}

func newFakeMigrationRows() *fakeMigrationRows {
	entries, err := fs.ReadDir(migrations.Files(), ".")
	if err != nil {
		panic(err)
	}
	rows := make([]fakeMigrationRow, 0, len(entries))
	for index, entry := range entries {
		body, readErr := fs.ReadFile(migrations.Files(), entry.Name())
		if readErr != nil {
			panic(readErr)
		}
		rows = append(rows, fakeMigrationRow{version: int64(index + 1), name: entry.Name(), digest: sha256.Sum256(body)})
	}
	return &fakeMigrationRows{rows: rows}
}

func (*fakeMigrationRows) Close() {}

func (rows *fakeMigrationRows) Next() bool {
	return rows.index < len(rows.rows)
}

func (rows *fakeMigrationRows) Scan(destinations ...any) error {
	row := rows.rows[rows.index]
	rows.index++
	*(destinations[0].(*int64)) = row.version
	*(destinations[1].(*string)) = row.name
	*(destinations[2].(*[]byte)) = append([]byte(nil), row.digest[:]...)
	return nil
}

func (*fakeMigrationRows) Err() error { return nil }

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

func (fakeAuthenticationService) CompleteRevalidation(context.Context, string, string, string) (string, string, time.Time, error) {
	return "", "", time.Time{}, errors.New("revalidation callback is not expected")
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
	readinessResponse, err := client.Get("http://" + listener.Addr().String() + "/health/ready")
	if err != nil {
		t.Fatalf("GET /health/ready returned error: %v", err)
	}
	readinessBody := new(bytes.Buffer)
	_, readinessReadErr := readinessBody.ReadFrom(readinessResponse.Body)
	_ = readinessResponse.Body.Close()
	if readinessReadErr != nil || readinessResponse.StatusCode != http.StatusOK || readinessBody.String() != "ok\n" {
		t.Fatalf("GET /health/ready = (status %d, body %q, read %v)", readinessResponse.StatusCode, readinessBody.String(), readinessReadErr)
	}
	rootResponse, err := client.Get("http://" + listener.Addr().String() + "/")
	if err != nil {
		t.Fatalf("GET / returned error: %v", err)
	}
	rootBody := new(bytes.Buffer)
	_, readErr := rootBody.ReadFrom(rootResponse.Body)
	_ = rootResponse.Body.Close()
	queryCalls, queryArgs := pool.areaQuerySnapshot()
	var groupIDs []int64
	groupsOK := false
	if len(queryArgs) == 3 {
		groupIDs, groupsOK = queryArgs[2].([]int64)
	}
	if readErr != nil || rootResponse.StatusCode != http.StatusOK || !strings.Contains(rootBody.String(), "Public area") ||
		queryCalls != 1 || len(queryArgs) != 3 || queryArgs[0] != false || queryArgs[1] != false || !groupsOK || len(groupIDs) != 0 {
		t.Fatalf("GET / = (status %d, body %q, query calls %d, args %#v, read %v)",
			rootResponse.StatusCode, rootBody.String(), queryCalls, queryArgs, readErr)
	}
	topicResponse, err := client.Get("http://" + listener.Addr().String() + "/topics/42")
	if err != nil {
		t.Fatalf("GET /topics/42 returned error: %v", err)
	}
	topicBody := new(bytes.Buffer)
	_, topicReadErr := topicBody.ReadFrom(topicResponse.Body)
	_ = topicResponse.Body.Close()
	topicCalls, topicArgs := pool.topicPostQuerySnapshot()
	var topicGroupIDs []int64
	topicGroupsOK := false
	if len(topicArgs) == 6 {
		topicGroupIDs, topicGroupsOK = topicArgs[5].([]int64)
	}
	if topicReadErr != nil || topicResponse.StatusCode != http.StatusOK || !strings.Contains(topicBody.String(), "<strong>forum</strong>") ||
		topicCalls != 1 || len(topicArgs) != 6 || topicArgs[0] != int32(0) || topicArgs[1] != store.PostPageSize ||
		topicArgs[2] != int64(42) || topicArgs[3] != false || topicArgs[4] != false || !topicGroupsOK || len(topicGroupIDs) != 0 {
		t.Fatalf("GET /topics/42 = (status %d, body %q, query calls %d, args %#v, read %v)",
			topicResponse.StatusCode, topicBody.String(), topicCalls, topicArgs, topicReadErr)
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("run() returned error: %v", err)
	}
	if !strings.Contains(logs.String(), `"msg":"service starting","version":"development","commit":"unknown"`) ||
		!strings.Contains(logs.String(), `"msg":"service stopped"`) {
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
		"BOOTSTRAP_ADMIN_SUBJECT":  "subject-1",
		"REGISTRATION_URL":         "http://127.0.0.1:9000/if/flow/gotth-bb-enrollment/",
		"REGISTRATION_ENABLED":     "false",
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
