package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
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
	provider           discoveredOIDCProvider
	database           SessionDatabase
	queries            *db.Queries
	entropy            io.Reader
	clock              func() time.Time
	sessionMaximumAge  time.Duration
	validateReturnPath func(string) (string, error)
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
		provider:           provider,
		database:           database,
		queries:            db.New(database),
		entropy:            entropy,
		clock:              clock,
		sessionMaximumAge:  sessionMaximumAge,
		validateReturnPath: validateReturnPath,
	}, nil
}
