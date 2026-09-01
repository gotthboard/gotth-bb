package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
)

// SessionDatabase is the exact pgx/sqlc surface required by login-attempt and
// identity/session operations.
type SessionDatabase interface {
	db.DBTX
	Begin(context.Context) (pgx.Tx, error)
}

// Service owns the immutable Authentik and PostgreSQL dependencies used by
// authentication requests.
type Service struct {
	provider             discoveredOIDCProvider
	database             SessionDatabase
	queries              *db.Queries
	entropy              io.Reader
	clock                func() time.Time
	sessionMaximumAge    time.Duration
	sessionIdleTimeout   time.Duration
	revalidationInterval time.Duration
	validateReturnPath   func(string) (string, error)
}

// Format prevents recursive formatting of retained OIDC, PostgreSQL, entropy,
// and callback dependencies for every fmt verb.
//
// Complexity: time and auxiliary space are O(1), Omega(1), and tight Theta(1);
// one fixed marker is delegated directly to the formatter state.
func (Service) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[REDACTED AUTH SERVICE]")
}

// NewService validates all local login/session dependencies before performing
// one hardened OIDC discovery request and returns no partial service on failure.
// PostgreSQL is retained but not queried or mutated during construction.
//
// Complexity: local dependency validation is tight Theta(1). For an n-byte
// discovery document (n <= 512 KiB), delegated discovery time and auxiliary
// space are O(n), Omega(1), with no tighter Theta bound because network I/O may
// fail before a body arrives. The retained provider metadata is O(n); database,
// entropy, clock, and validator are retained by interface/function headers.
func NewService(
	ctx context.Context,
	baseTransport http.RoundTripper,
	issuerURL url.URL,
	clientID string,
	clientSecret string,
	redirectURL string,
	database SessionDatabase,
	entropy io.Reader,
	clock func() time.Time,
	sessionMaximumAge time.Duration,
	sessionIdleTimeout time.Duration,
	revalidationInterval time.Duration,
	validateReturnPath func(string) (string, error),
) (*Service, error) {
	if ctx == nil {
		return nil, fmt.Errorf("authentication service context is required")
	}
	if database == nil {
		return nil, fmt.Errorf("authentication session database is required")
	}
	if entropy == nil {
		return nil, fmt.Errorf("authentication entropy source is required")
	}
	if clock == nil {
		return nil, fmt.Errorf("authentication clock is required")
	}
	if sessionMaximumAge < time.Second {
		return nil, fmt.Errorf("authentication session maximum age is below browser cookie precision")
	}
	if sessionIdleTimeout < time.Second || sessionIdleTimeout > sessionMaximumAge {
		return nil, fmt.Errorf("authentication session idle timeout is outside the supported session lifetime")
	}
	if revalidationInterval < time.Second || revalidationInterval > sessionMaximumAge {
		return nil, fmt.Errorf("authentication revalidation interval is outside the supported session lifetime")
	}
	if validateReturnPath == nil {
		return nil, fmt.Errorf("authentication return-path validator is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("construct authentication service: %w", err)
	}
	provider, err := discoverOIDCProvider(ctx, baseTransport, issuerURL, clientID, clientSecret, redirectURL)
	if err != nil {
		return nil, err
	}
	return &Service{
		provider:             provider,
		database:             database,
		queries:              db.New(database),
		entropy:              entropy,
		clock:                clock,
		sessionMaximumAge:    sessionMaximumAge,
		sessionIdleTimeout:   sessionIdleTimeout,
		revalidationInterval: revalidationInterval,
		validateReturnPath:   validateReturnPath,
	}, nil
}

// BeginInitialLogin persists one validated login attempt and constructs its
// Authentik authorization URL. Only the public state value is returned beside
// the URL; the nonce and PKCE verifier remain protected in PostgreSQL.
//
// Complexity: local dependency validation and result projection are tight
// Theta(1). With delegated begin time Bt and space Bs, total time is O(Bt),
// Omega(1), and auxiliary space O(Bs), Omega(1). The delegated work performs
// fixed-size cryptography, return-path validation, one database insert, and
// bounded URL construction without retries or background work.
func (service *Service) BeginInitialLogin(ctx context.Context, returnPath string) (string, string, error) {
	if service == nil || service.provider.provider == nil || service.provider.verifier == nil || service.provider.httpClient == nil ||
		service.provider.oauth2Config.ClientID == "" || service.provider.oauth2Config.Endpoint.AuthURL == "" ||
		service.provider.oauth2Config.RedirectURL == "" ||
		!slices.Equal(service.provider.oauth2Config.Scopes, []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail}) ||
		service.database == nil || service.queries == nil || service.entropy == nil || service.clock == nil ||
		service.validateReturnPath == nil {
		return "", "", fmt.Errorf("authentication service is not initialized for login start")
	}
	material, err := beginInitialLogin(
		ctx,
		service.queries.InsertOIDCLoginAttempt,
		service.entropy,
		service.clock,
		service.validateReturnPath,
		returnPath,
	)
	if err != nil {
		if ctx != nil {
			if contextError := ctx.Err(); contextError != nil {
				return "", "", fmt.Errorf("begin initial login: %w", contextError)
			}
		}
		return "", "", fmt.Errorf("begin initial login failed")
	}
	authorizationURL, err := service.provider.initialAuthorizationURL(material)
	if err != nil {
		return "", "", fmt.Errorf("build initial authorization URL failed")
	}
	return authorizationURL, material.state, nil
}

// CompleteInitialLogin consumes one live attempt, exchanges its code, and
// commits its verified identity/session through the service-owned dependencies.
// It returns only the browser token, validated navigation path, and expiry.
//
// Complexity: local validation and result projection are tight Theta(1). With
// delegated consume, exchange, and create times Ct, Et, St and spaces Cs, Es,
// Ss, total time is O(Ct+Et+St), Omega(Ct), and auxiliary space O(Cs+Es+Ss),
// Omega(1); no tight Theta bound is established because later network/database
// stages may not run. No stage is retried.
func (service *Service) CompleteInitialLogin(ctx context.Context, state, code string) (string, string, time.Time, error) {
	if service == nil || service.provider.provider == nil || service.provider.verifier == nil || service.provider.httpClient == nil ||
		service.provider.oauth2Config.ClientID == "" || service.provider.oauth2Config.Endpoint.TokenURL == "" ||
		service.provider.oauth2Config.RedirectURL == "" ||
		!slices.Equal(service.provider.oauth2Config.Scopes, []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail}) ||
		service.database == nil || service.queries == nil || service.entropy == nil || service.clock == nil ||
		service.sessionMaximumAge < time.Second || service.validateReturnPath == nil {
		return "", "", time.Time{}, fmt.Errorf("authentication service is not initialized for login completion")
	}
	completed, err := completeInitialLogin(
		ctx,
		func(stageContext context.Context, stateValue string) (consumedInitialLogin, error) {
			return consumeInitialLogin(
				stageContext, service.queries.ConsumeOIDCLoginAttempt, service.clock,
				service.validateReturnPath, stateValue,
			)
		},
		service.provider.exchangeInitialLogin,
		func(stageContext context.Context, claims verifiedIdentityClaims) (createdInitialSession, error) {
			return createInitialSession(
				stageContext, service.database, service.entropy, service.clock,
				service.sessionMaximumAge, claims,
			)
		},
		state,
		code,
	)
	if err != nil {
		return "", "", time.Time{}, err
	}
	return completed.token, completed.returnPath, completed.expiresAt, nil
}
