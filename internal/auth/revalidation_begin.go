package auth

import (
	"context"
	"fmt"
	"io"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5/pgtype"
)

// beginRevalidation creates one protected OIDC attempt whose durable metadata
// is bound to an already authenticated server-side session ID. Browser input
// supplies only the return path; it cannot select the database session.
//
// Complexity: local validation and metadata replacement are tight Theta(1)
// time and auxiliary space. Generation, protection, path validation, and one
// insert retain beginInitialLogin's documented bounds. No work is retried or
// detached.
func beginRevalidation(
	ctx context.Context,
	insert insertOIDCLoginAttempt,
	entropy io.Reader,
	clock func() time.Time,
	validateReturnPath func(string) (string, error),
	sessionID int64,
	rawReturnPath string,
) (loginMaterial, error) {
	if insert == nil {
		return loginMaterial{}, fmt.Errorf("revalidation-attempt insert is required")
	}
	if sessionID <= 0 {
		return loginMaterial{}, fmt.Errorf("revalidation session ID must be positive")
	}
	return beginInitialLogin(
		ctx,
		func(insertContext context.Context, params db.InsertOIDCLoginAttemptParams) error {
			params.Purpose = "revalidate"
			params.SessionID = pgtype.Int8{Int64: sessionID, Valid: true}
			return insert(insertContext, params)
		},
		entropy,
		clock,
		validateReturnPath,
		rawReturnPath,
	)
}
