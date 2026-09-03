package auth

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/oauth2"
)

type failingBeginDBTX struct {
	db.DBTX
	cause error
	calls int
}

func (database *failingBeginDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	database.calls++
	return pgconn.CommandTag{}, database.cause
}

func TestServiceBeginInitialLoginRejectsUninitializedService(t *testing.T) {
	t.Parallel()

	for _, service := range []*Service{nil, {}} {
		authorizationURL, state, err := service.BeginInitialLogin(context.Background(), "/bb/")
		if err == nil || authorizationURL != "" || state != "" {
			t.Fatalf("BeginInitialLogin() = (%q, %q, %v), want zero/error", authorizationURL, state, err)
		}
	}
}

func TestServiceBeginInitialLoginRejectsReturnPathBeforeEntropyOrDatabase(t *testing.T) {
	t.Parallel()

	const secret = "do-not-leak-return-path-cause"
	database := &constructorSessionDatabase{}
	service := &Service{
		provider: discoveredOIDCProvider{
			provider: new(oidc.Provider), verifier: new(oidc.IDTokenVerifier), httpClient: &http.Client{},
			oauth2Config: oauth2.Config{
				ClientID: "gotth-bb", Endpoint: oauth2.Endpoint{AuthURL: "https://auth.example/authorize"},
				RedirectURL: "https://forum.example/bb/auth/callback",
				Scopes:      []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail},
			},
		},
		database: database, queries: db.New(database),
		entropy: errReader{cause: errors.New("entropy must not run")}, clock: time.Now,
		validateReturnPath: func(string) (string, error) { return "", errors.New(secret) },
	}
	authorizationURL, state, err := service.BeginInitialLogin(context.Background(), "https://evil.example/")
	if err == nil || authorizationURL != "" || state != "" || strings.Contains(err.Error(), secret) {
		t.Fatalf("BeginInitialLogin() = (%q, %q, %v), want zero/redacted error", authorizationURL, state, err)
	}
}

func TestServiceBeginInitialLoginPreservesCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service := &Service{
		provider: discoveredOIDCProvider{
			provider: new(oidc.Provider), verifier: new(oidc.IDTokenVerifier), httpClient: &http.Client{},
			oauth2Config: oauth2.Config{
				ClientID: "gotth-bb", Endpoint: oauth2.Endpoint{AuthURL: "https://auth.example/authorize"},
				RedirectURL: "https://forum.example/bb/auth/callback",
				Scopes:      []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail},
			},
		},
		database: &constructorSessionDatabase{}, queries: db.New(&constructorSessionDatabase{}),
		entropy: bytes.NewReader(sequentialBytes(128)), clock: time.Now,
		validateReturnPath: func(raw string) (string, error) { return raw, nil },
	}
	authorizationURL, state, err := service.BeginInitialLogin(ctx, "/bb/")
	if !errors.Is(err, context.Canceled) || authorizationURL != "" || state != "" {
		t.Fatalf("BeginInitialLogin() = (%q, %q, %v), want zero/canceled", authorizationURL, state, err)
	}
	authorizationURL, state, err = service.BeginInitialLogin(nil, "/bb/")
	if err == nil || authorizationURL != "" || state != "" {
		t.Fatalf("BeginInitialLogin(nil) = (%q, %q, %v), want zero/error", authorizationURL, state, err)
	}
}

func TestServiceBeginInitialLoginRedactsDatabaseFailure(t *testing.T) {
	t.Parallel()

	const secret = "do-not-leak-database-cause"
	failingDatabase := &failingBeginDBTX{cause: errors.New(secret)}
	database := &constructorSessionDatabase{DBTX: failingDatabase}
	service := &Service{
		provider: discoveredOIDCProvider{
			provider: new(oidc.Provider), verifier: new(oidc.IDTokenVerifier), httpClient: &http.Client{},
			oauth2Config: oauth2.Config{
				ClientID: "gotth-bb", Endpoint: oauth2.Endpoint{AuthURL: "https://auth.example/authorize"},
				RedirectURL: "https://forum.example/bb/auth/callback",
				Scopes:      []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail},
			},
		},
		database: database, queries: db.New(database),
		entropy: bytes.NewReader(sequentialBytes(128)), clock: time.Now,
		validateReturnPath: func(raw string) (string, error) { return raw, nil },
	}
	authorizationURL, state, err := service.BeginInitialLogin(context.Background(), "/bb/")
	if err == nil || authorizationURL != "" || state != "" || strings.Contains(err.Error(), secret) || failingDatabase.calls != 1 {
		t.Fatalf("BeginInitialLogin() = (%q, %q, %v), want zero/redacted error", authorizationURL, state, err)
	}
}
