package auth

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

func TestServiceCompleteInitialLoginRejectsUninitializedService(t *testing.T) {
	t.Parallel()

	for _, service := range []*Service{nil, {}} {
		token, returnPath, expiresAt, err := service.CompleteInitialLogin(context.Background(), "state", "code")
		if err == nil || token != "" || returnPath != "" || !expiresAt.IsZero() {
			t.Fatalf("CompleteInitialLogin() = (%q, %q, %s, %v), want zero/error", token, returnPath, expiresAt, err)
		}
	}
}

func TestServiceCompleteInitialLoginRejectsInvalidCallbackBeforeDatabaseWork(t *testing.T) {
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
		database:           database,
		queries:            db.New(database),
		entropy:            bytes.NewReader(make([]byte, 64)),
		clock:              time.Now,
		sessionMaximumAge:  time.Hour,
		validateReturnPath: func(raw string) (string, error) { return raw, nil },
	}
	for _, test := range []struct {
		state string
		code  string
	}{{code: "code"}, {state: "state"}} {
		token, returnPath, expiresAt, err := service.CompleteInitialLogin(context.Background(), test.state, test.code)
		if err == nil || token != "" || returnPath != "" || !expiresAt.IsZero() {
			t.Fatalf("CompleteInitialLogin() = (%q, %q, %s, %v), want zero/error", token, returnPath, expiresAt, err)
		}
	}
}
