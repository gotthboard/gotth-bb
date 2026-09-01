package auth

import (
	"context"
	"fmt"
	"time"
)

type consumedRevalidation struct {
	material   loginMaterial
	returnPath string
	sessionID  int64
}

type completedRevalidation struct {
	token      string
	returnPath string
	expiresAt  time.Time
}

// completeRevalidation composes one purpose-bound attempt consumption, one-shot
// OIDC exchange, and atomic old-to-new session rotation in that order. It
// returns browser state only after every stage succeeds and collapses all
// non-context causes at this trust boundary.
//
// Complexity: local validation, dispatch, and result assembly are tight
// Theta(1). With delegated consume, exchange, and rotation times Ct, Et, Rt and
// spaces Cs, Es, Rs, total time is O(Ct+Et+Rt), Omega(Ct), and auxiliary space
// O(Cs+Es+Rs), Omega(1); later stages may not run and include external I/O. No
// retry or background work occurs.
func completeRevalidation(
	ctx context.Context,
	consume func(context.Context, string) (consumedRevalidation, error),
	exchange func(context.Context, string, loginMaterial) (verifiedIdentityClaims, error),
	rotate func(context.Context, int64, string, verifiedIdentityClaims) (createdRevalidatedSession, error),
	state string,
	code string,
	oldToken string,
) (completedRevalidation, error) {
	if ctx == nil {
		return completedRevalidation{}, fmt.Errorf("revalidation completion context is required")
	}
	if consume == nil {
		return completedRevalidation{}, fmt.Errorf("revalidation consumer is required")
	}
	if exchange == nil {
		return completedRevalidation{}, fmt.Errorf("revalidation exchanger is required")
	}
	if rotate == nil {
		return completedRevalidation{}, fmt.Errorf("revalidated session rotator is required")
	}
	if state == "" || code == "" || oldToken == "" {
		return completedRevalidation{}, fmt.Errorf("OIDC callback state, code, and old session credential are required")
	}
	if err := ctx.Err(); err != nil {
		return completedRevalidation{}, fmt.Errorf("complete revalidation: %w", err)
	}
	consumed, err := consume(ctx, state)
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return completedRevalidation{}, fmt.Errorf("consume revalidation: %w", contextError)
		}
		return completedRevalidation{}, fmt.Errorf("consume revalidation failed")
	}
	if consumed.returnPath == "" || consumed.sessionID <= 0 {
		return completedRevalidation{}, fmt.Errorf("consume revalidation returned an incomplete result")
	}
	claims, err := exchange(ctx, code, consumed.material)
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return completedRevalidation{}, fmt.Errorf("exchange revalidation: %w", contextError)
		}
		return completedRevalidation{}, fmt.Errorf("exchange revalidation failed")
	}
	rotated, err := rotate(ctx, consumed.sessionID, oldToken, claims)
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return completedRevalidation{}, fmt.Errorf("rotate revalidated session: %w", contextError)
		}
		return completedRevalidation{}, fmt.Errorf("rotate revalidated session failed")
	}
	if rotated.token == "" || rotated.userID <= 0 || rotated.sessionID <= 0 ||
		rotated.sessionID == consumed.sessionID || rotated.expiresAt.IsZero() {
		return completedRevalidation{}, fmt.Errorf("rotate revalidated session returned an incomplete result")
	}
	return completedRevalidation{token: rotated.token, returnPath: consumed.returnPath, expiresAt: rotated.expiresAt}, nil
}
