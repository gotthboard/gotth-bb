package auth

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/oauth2"
)

type capturingRevalidationDBTX struct {
	db.DBTX
	args  []any
	calls int
}

func (database *capturingRevalidationDBTX) Exec(_ context.Context, _ string, arguments ...any) (pgconn.CommandTag, error) {
	database.calls++
	database.args = append([]any(nil), arguments...)
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func TestServiceBeginRevalidationPersistsBoundAttemptAndBuildsAuthorization(t *testing.T) {
	t.Parallel()

	captured := &capturingRevalidationDBTX{}
	database := &constructorSessionDatabase{DBTX: captured}
	service := revalidationBeginTestService(database)
	authorizationURL, state, err := service.BeginRevalidation(context.Background(), 73, "/bb/topics/42")
	if err != nil || authorizationURL == "" || state == "" || captured.calls != 1 || len(captured.args) != 8 {
		t.Fatalf("BeginRevalidation() = (URL %q, state %q, calls %d, args %d, error %v)", authorizationURL, state, captured.calls, len(captured.args), err)
	}
	parsed, err := url.Parse(authorizationURL)
	if err != nil || parsed.Query().Get("state") != state {
		t.Fatalf("authorization URL = (%q, %v)", authorizationURL, err)
	}
	sessionID, ok := captured.args[4].(pgtype.Int8)
	if captured.args[3] != "revalidate" || !ok || !sessionID.Valid || sessionID.Int64 != 73 || captured.args[5] != "/bb/topics/42" {
		t.Fatalf("persisted purpose/session/return = (%v, %#v, %v)", captured.args[3], captured.args[4], captured.args[5])
	}
}

func TestServiceBeginRevalidationRejectsInvalidStateAndRedactsFailure(t *testing.T) {
	t.Parallel()

	for _, service := range []*Service{nil, {}} {
		if authorizationURL, state, err := service.BeginRevalidation(context.Background(), 1, "/bb/"); err == nil || authorizationURL != "" || state != "" {
			t.Fatalf("BeginRevalidation() = (%q, %q, %v), want zero/error", authorizationURL, state, err)
		}
	}
	service := revalidationBeginTestService(&constructorSessionDatabase{})
	service.entropy = panicReader{}
	for _, sessionID := range []int64{0, -1} {
		if authorizationURL, state, err := service.BeginRevalidation(context.Background(), sessionID, "/bb/"); err == nil || authorizationURL != "" || state != "" {
			t.Fatalf("BeginRevalidation(%d) = (%q, %q, %v), want zero/error", sessionID, authorizationURL, state, err)
		}
	}
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	authorizationURL, state, err := service.BeginRevalidation(canceledContext, 73, "/bb/")
	if !errors.Is(err, context.Canceled) || authorizationURL != "" || state != "" {
		t.Fatalf("canceled BeginRevalidation() = (%q, %q, %v)", authorizationURL, state, err)
	}

	const secret = "do-not-leak-revalidation-insert-cause"
	failing := &failingBeginDBTX{cause: errors.New(secret)}
	failingDatabase := &constructorSessionDatabase{DBTX: failing}
	service = revalidationBeginTestService(failingDatabase)
	authorizationURL, state, err = service.BeginRevalidation(context.Background(), 73, "/bb/")
	if err == nil || authorizationURL != "" || state != "" || strings.Contains(err.Error(), secret) || failing.calls != 1 {
		t.Fatalf("BeginRevalidation() = (%q, %q, %v), calls %d", authorizationURL, state, err, failing.calls)
	}
}

func revalidationBeginTestService(database *constructorSessionDatabase) *Service {
	return &Service{
		provider: discoveredOIDCProvider{
			provider: new(oidc.Provider), verifier: new(oidc.IDTokenVerifier), httpClient: &http.Client{},
			oauth2Config: oauth2.Config{
				ClientID: "gotth-bb", Endpoint: oauth2.Endpoint{AuthURL: "https://auth.example/authorize"},
				RedirectURL: "https://forum.example/bb/auth/callback",
				Scopes:      []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail},
			},
		},
		database: database,
		queries:  db.New(database),
		entropy:  bytes.NewReader(sequentialBytes(120)),
		clock: func() time.Time {
			return time.Date(2026, time.September, 1, 19, 25, 0, 0, time.UTC)
		},
		validateReturnPath: func(raw string) (string, error) { return raw, nil },
	}
}
