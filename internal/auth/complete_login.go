package auth

import (
	"context"
	"fmt"
	"time"
)

type completedInitialLogin struct {
	token      string
	returnPath string
	expiresAt  time.Time
}

// completeInitialLogin composes the one-time attempt, one-shot OIDC exchange,
// and atomic identity/session creation in that order. It returns browser state
// only after every stage succeeds and collapses all non-context causes at this
// trust boundary.
//
// Complexity: local validation, dispatch, and result assembly are tight
// Theta(1) time and auxiliary space. With delegated stage times Ct, Et, St and
// spaces Cs, Es, Ss for consume, exchange, and create, total time is
// O(Ct+Et+St), Omega(Ct), and auxiliary space O(Cs+Es+Ss), Omega(1); no tighter
// Theta bound is established because later stages may not run and all three
// include external I/O. No retry or background work occurs.
func completeInitialLogin(
	ctx context.Context,
	consume func(context.Context, string) (consumedInitialLogin, error),
	exchange func(context.Context, string, loginMaterial) (verifiedIdentityClaims, error),
	create func(context.Context, verifiedIdentityClaims) (createdInitialSession, error),
	state string,
	code string,
) (completedInitialLogin, error) {
	if ctx == nil {
		return completedInitialLogin{}, fmt.Errorf("initial login completion context is required")
	}
	if consume == nil {
		return completedInitialLogin{}, fmt.Errorf("initial login consumer is required")
	}
	if exchange == nil {
		return completedInitialLogin{}, fmt.Errorf("initial login exchanger is required")
	}
	if create == nil {
		return completedInitialLogin{}, fmt.Errorf("initial session creator is required")
	}
	if state == "" || code == "" {
		return completedInitialLogin{}, fmt.Errorf("OIDC callback state and code are required")
	}
	if err := ctx.Err(); err != nil {
		return completedInitialLogin{}, fmt.Errorf("complete initial login: %w", err)
	}
	consumed, err := consume(ctx, state)
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return completedInitialLogin{}, fmt.Errorf("consume initial login: %w", contextError)
		}
		return completedInitialLogin{}, fmt.Errorf("consume initial login failed")
	}
	if consumed.returnPath == "" {
		return completedInitialLogin{}, fmt.Errorf("consume initial login returned no navigation target")
	}
	claims, err := exchange(ctx, code, consumed.material)
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return completedInitialLogin{}, fmt.Errorf("exchange initial login: %w", contextError)
		}
		return completedInitialLogin{}, fmt.Errorf("exchange initial login failed")
	}
	created, err := create(ctx, claims)
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return completedInitialLogin{}, fmt.Errorf("create initial session: %w", contextError)
		}
		return completedInitialLogin{}, fmt.Errorf("create initial session failed")
	}
	if created.token == "" || created.userID <= 0 || created.sessionID <= 0 || created.expiresAt.IsZero() {
		return completedInitialLogin{}, fmt.Errorf("create initial session returned an incomplete result")
	}
	return completedInitialLogin{token: created.token, returnPath: consumed.returnPath, expiresAt: created.expiresAt}, nil
}
