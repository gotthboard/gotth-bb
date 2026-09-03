package auth

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"golang.org/x/oauth2"
)

func TestServiceCompleteRevalidationRejectsUninitializedService(t *testing.T) {
	t.Parallel()

	for _, service := range []*Service{nil, {}} {
		token, returnPath, expiresAt, err := service.CompleteRevalidation(context.Background(), "state", "code", "old-token")
		if err == nil || token != "" || returnPath != "" || !expiresAt.IsZero() {
			t.Fatalf("CompleteRevalidation() = (%q, %q, %s, %v), want zero/error", token, returnPath, expiresAt, err)
		}
	}
}

func TestServiceCompleteRevalidationRejectsInvalidCallbackBeforeDatabaseWork(t *testing.T) {
	t.Parallel()

	database := &constructorSessionDatabase{}
	service := &Service{
		provider: discoveredOIDCProvider{
			provider: new(oidc.Provider), verifier: new(oidc.IDTokenVerifier), httpClient: &http.Client{},
			oauth2Config: oauth2.Config{
				ClientID: "gotth-bb", Endpoint: oauth2.Endpoint{TokenURL: "https://auth.example/token"},
				RedirectURL: "https://forum.example/bb/auth/callback",
				Scopes:      []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail},
			},
		},
		database:             database,
		queries:              db.New(database),
		entropy:              bytes.NewReader(make([]byte, 64)),
		clock:                time.Now,
		sessionMaximumAge:    time.Hour,
		sessionIdleTimeout:   time.Minute,
		validateReturnPath:   func(raw string) (string, error) { return raw, nil },
		revalidationInterval: time.Minute,
	}
	for _, test := range []struct {
		state    string
		code     string
		oldToken string
	}{
		{code: "code", oldToken: "old-token"},
		{state: "state", oldToken: "old-token"},
		{state: "state", code: "code"},
	} {
		token, returnPath, expiresAt, err := service.CompleteRevalidation(context.Background(), test.state, test.code, test.oldToken)
		if err == nil || token != "" || returnPath != "" || !expiresAt.IsZero() {
			t.Fatalf("CompleteRevalidation() = (%q, %q, %s, %v), want zero/error", token, returnPath, expiresAt, err)
		}
	}
}
