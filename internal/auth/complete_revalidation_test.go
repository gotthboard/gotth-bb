package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCompleteRevalidationRunsOneOrderedFailClosedWorkflow(t *testing.T) {
	t.Parallel()

	material := loginMaterial{state: "state", nonce: "nonce", pkceVerifier: "verifier"}
	claims := verifiedIdentityClaims{issuer: "issuer", subject: "subject", displayName: "Member"}
	expiresAt := time.Date(2026, time.September, 2, 22, 15, 0, 0, time.UTC)
	var order []string
	got, err := completeRevalidation(
		context.Background(),
		func(_ context.Context, state string) (consumedRevalidation, error) {
			order = append(order, "consume")
			if state != "browser-state" {
				t.Fatalf("consume state = %q", state)
			}
			return consumedRevalidation{material: material, returnPath: "/bb/topics/7", sessionID: 13}, nil
		},
		func(_ context.Context, code string, gotMaterial loginMaterial) (verifiedIdentityClaims, error) {
			order = append(order, "exchange")
			if code != "authorization-code" || gotMaterial != material {
				t.Fatalf("exchange = (%q, %+v)", code, gotMaterial)
			}
			return claims, nil
		},
		func(_ context.Context, sessionID int64, oldToken string, gotClaims verifiedIdentityClaims) (createdRevalidatedSession, error) {
			order = append(order, "rotate")
			if sessionID != 13 || oldToken != "old-session-token" || gotClaims != claims {
				t.Fatalf("rotate = (%d, %q, %+v)", sessionID, oldToken, gotClaims)
			}
			return createdRevalidatedSession{token: "new-session-token", userID: 3, sessionID: 17, expiresAt: expiresAt}, nil
		},
		"browser-state", "authorization-code", "old-session-token",
	)
	if err != nil {
		t.Fatalf("completeRevalidation() returned error: %v", err)
	}
	if got != (completedRevalidation{token: "new-session-token", returnPath: "/bb/topics/7", expiresAt: expiresAt}) {
		t.Fatalf("completeRevalidation() = %+v", got)
	}
	if strings.Join(order, ",") != "consume,exchange,rotate" {
		t.Fatalf("workflow order = %v", order)
	}
}

func TestCompleteRevalidationStopsAtEachFailureAndRedactsCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("do-not-leak-revalidation-cause")
	for _, failingStage := range []string{"consume", "exchange", "rotate"} {
		failingStage := failingStage
		t.Run(failingStage, func(t *testing.T) {
			t.Parallel()
			var order []string
			got, err := completeRevalidation(
				context.Background(),
				func(context.Context, string) (consumedRevalidation, error) {
					order = append(order, "consume")
					if failingStage == "consume" {
						return consumedRevalidation{}, cause
					}
					return consumedRevalidation{material: loginMaterial{state: "state"}, returnPath: "/bb/", sessionID: 7}, nil
				},
				func(context.Context, string, loginMaterial) (verifiedIdentityClaims, error) {
					order = append(order, "exchange")
					if failingStage == "exchange" {
						return verifiedIdentityClaims{}, cause
					}
					return verifiedIdentityClaims{issuer: "issuer", subject: "subject", displayName: "Member"}, nil
				},
				func(context.Context, int64, string, verifiedIdentityClaims) (createdRevalidatedSession, error) {
					order = append(order, "rotate")
					if failingStage == "rotate" {
						return createdRevalidatedSession{}, cause
					}
					return createdRevalidatedSession{token: "token", userID: 1, sessionID: 2, expiresAt: time.Now()}, nil
				},
				"state", "code", "old-token",
			)
			if err == nil || strings.Contains(err.Error(), cause.Error()) || got != (completedRevalidation{}) {
				t.Fatalf("completeRevalidation() = (%+v, %v)", got, err)
			}
			wantOrder := map[string]string{"consume": "consume", "exchange": "consume,exchange", "rotate": "consume,exchange,rotate"}[failingStage]
			if strings.Join(order, ",") != wantOrder {
				t.Fatalf("workflow order = %v, want %s", order, wantOrder)
			}
		})
	}
}

func TestCompleteRevalidationRejectsInvalidBoundaryBeforeWork(t *testing.T) {
	t.Parallel()

	panicConsume := func(context.Context, string) (consumedRevalidation, error) { panic("consume must not run") }
	panicExchange := func(context.Context, string, loginMaterial) (verifiedIdentityClaims, error) {
		panic("exchange must not run")
	}
	panicRotate := func(context.Context, int64, string, verifiedIdentityClaims) (createdRevalidatedSession, error) {
		panic("rotate must not run")
	}
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name     string
		ctx      context.Context
		consume  func(context.Context, string) (consumedRevalidation, error)
		exchange func(context.Context, string, loginMaterial) (verifiedIdentityClaims, error)
		rotate   func(context.Context, int64, string, verifiedIdentityClaims) (createdRevalidatedSession, error)
		state    string
		code     string
		oldToken string
		cause    error
	}{
		{name: "nil context", consume: panicConsume, exchange: panicExchange, rotate: panicRotate, state: "state", code: "code", oldToken: "token"},
		{name: "nil consume", ctx: context.Background(), exchange: panicExchange, rotate: panicRotate, state: "state", code: "code", oldToken: "token"},
		{name: "nil exchange", ctx: context.Background(), consume: panicConsume, rotate: panicRotate, state: "state", code: "code", oldToken: "token"},
		{name: "nil rotate", ctx: context.Background(), consume: panicConsume, exchange: panicExchange, state: "state", code: "code", oldToken: "token"},
		{name: "empty state", ctx: context.Background(), consume: panicConsume, exchange: panicExchange, rotate: panicRotate, code: "code", oldToken: "token"},
		{name: "empty code", ctx: context.Background(), consume: panicConsume, exchange: panicExchange, rotate: panicRotate, state: "state", oldToken: "token"},
		{name: "empty old token", ctx: context.Background(), consume: panicConsume, exchange: panicExchange, rotate: panicRotate, state: "state", code: "code"},
		{name: "canceled context", ctx: canceledContext, consume: panicConsume, exchange: panicExchange, rotate: panicRotate, state: "state", code: "code", oldToken: "token", cause: context.Canceled},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := completeRevalidation(test.ctx, test.consume, test.exchange, test.rotate, test.state, test.code, test.oldToken)
			if err == nil || got != (completedRevalidation{}) || test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("completeRevalidation() = (%+v, %v)", got, err)
			}
		})
	}
}

func TestCompleteRevalidationRejectsIncompleteSuccessfulStageResults(t *testing.T) {
	t.Parallel()

	claims := verifiedIdentityClaims{issuer: "issuer", subject: "subject", displayName: "Member"}
	validRotated := createdRevalidatedSession{token: "token", userID: 1, sessionID: 2, expiresAt: time.Now()}
	for _, test := range []struct {
		name       string
		returnPath string
		oldID      int64
		rotated    createdRevalidatedSession
	}{
		{name: "empty return path", oldID: 7, rotated: validRotated},
		{name: "invalid old session", returnPath: "/bb/", rotated: validRotated},
		{name: "empty token", returnPath: "/bb/", oldID: 7, rotated: createdRevalidatedSession{userID: 1, sessionID: 2, expiresAt: time.Now()}},
		{name: "empty user", returnPath: "/bb/", oldID: 7, rotated: createdRevalidatedSession{token: "token", sessionID: 2, expiresAt: time.Now()}},
		{name: "empty session", returnPath: "/bb/", oldID: 7, rotated: createdRevalidatedSession{token: "token", userID: 1, expiresAt: time.Now()}},
		{name: "same session ID", returnPath: "/bb/", oldID: 7, rotated: createdRevalidatedSession{token: "token", userID: 1, sessionID: 7, expiresAt: time.Now()}},
		{name: "empty expiry", returnPath: "/bb/", oldID: 7, rotated: createdRevalidatedSession{token: "token", userID: 1, sessionID: 2}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := completeRevalidation(
				context.Background(),
				func(context.Context, string) (consumedRevalidation, error) {
					return consumedRevalidation{returnPath: test.returnPath, sessionID: test.oldID}, nil
				},
				func(context.Context, string, loginMaterial) (verifiedIdentityClaims, error) { return claims, nil },
				func(context.Context, int64, string, verifiedIdentityClaims) (createdRevalidatedSession, error) {
					return test.rotated, nil
				},
				"state", "code", "old-token",
			)
			if got != (completedRevalidation{}) || err == nil {
				t.Fatalf("completeRevalidation() = (%+v, %v), want zero/error", got, err)
			}
		})
	}
}

func TestCompleteRevalidationPreservesCancellationRaisedByEachStage(t *testing.T) {
	t.Parallel()

	const secret = "do-not-leak-stage-cause"
	for _, canceledStage := range []string{"consume", "exchange", "rotate"} {
		canceledStage := canceledStage
		t.Run(canceledStage, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			fail := func(stage string) error {
				if canceledStage != stage {
					return nil
				}
				cancel()
				return errors.New(secret)
			}
			got, err := completeRevalidation(
				ctx,
				func(context.Context, string) (consumedRevalidation, error) {
					return consumedRevalidation{returnPath: "/bb/", sessionID: 7}, fail("consume")
				},
				func(context.Context, string, loginMaterial) (verifiedIdentityClaims, error) {
					return verifiedIdentityClaims{issuer: "issuer", subject: "subject", displayName: "Member"}, fail("exchange")
				},
				func(context.Context, int64, string, verifiedIdentityClaims) (createdRevalidatedSession, error) {
					return createdRevalidatedSession{token: "token", userID: 1, sessionID: 2, expiresAt: time.Now()}, fail("rotate")
				},
				"state", "code", "old-token",
			)
			if got != (completedRevalidation{}) || !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), secret) {
				t.Fatalf("completeRevalidation() = (%+v, %v)", got, err)
			}
		})
	}
}
