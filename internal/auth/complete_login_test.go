package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCompleteInitialLoginRunsOneOrderedFailClosedWorkflow(t *testing.T) {
	t.Parallel()

	material := loginMaterial{state: "state", nonce: "nonce", pkceVerifier: "verifier"}
	claims := verifiedIdentityClaims{issuer: "issuer", subject: "subject", displayName: "Member"}
	expiresAt := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	var order []string
	got, err := completeInitialLogin(
		context.Background(),
		func(_ context.Context, state string) (consumedInitialLogin, error) {
			order = append(order, "consume")
			if state != "browser-state" {
				t.Fatalf("consume state = %q", state)
			}
			return consumedInitialLogin{material: material, returnPath: "/bb/topics/7"}, nil
		},
		func(_ context.Context, code string, gotMaterial loginMaterial) (verifiedIdentityClaims, error) {
			order = append(order, "exchange")
			if code != "authorization-code" || gotMaterial != material {
				t.Fatalf("exchange = (%q, %+v)", code, gotMaterial)
			}
			return claims, nil
		},
		func(_ context.Context, gotClaims verifiedIdentityClaims) (createdInitialSession, error) {
			order = append(order, "create")
			if gotClaims != claims {
				t.Fatalf("create claims = %+v", gotClaims)
			}
			return createdInitialSession{token: "session-token", userID: 3, sessionID: 9, expiresAt: expiresAt}, nil
		},
		"browser-state",
		"authorization-code",
	)
	if err != nil {
		t.Fatalf("completeInitialLogin() returned error: %v", err)
	}
	if got != (completedInitialLogin{token: "session-token", returnPath: "/bb/topics/7", expiresAt: expiresAt}) {
		t.Fatalf("completeInitialLogin() = %+v", got)
	}
	if strings.Join(order, ",") != "consume,exchange,create" {
		t.Fatalf("workflow order = %v", order)
	}
}

func TestCompleteInitialLoginStopsAtEachFailureAndRedactsCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("do-not-leak-callback-cause")
	for _, failingStage := range []string{"consume", "exchange", "create"} {
		failingStage := failingStage
		t.Run(failingStage, func(t *testing.T) {
			t.Parallel()
			var order []string
			got, err := completeInitialLogin(
				context.Background(),
				func(context.Context, string) (consumedInitialLogin, error) {
					order = append(order, "consume")
					if failingStage == "consume" {
						return consumedInitialLogin{}, cause
					}
					return consumedInitialLogin{material: loginMaterial{state: "state"}, returnPath: "/bb/"}, nil
				},
				func(context.Context, string, loginMaterial) (verifiedIdentityClaims, error) {
					order = append(order, "exchange")
					if failingStage == "exchange" {
						return verifiedIdentityClaims{}, cause
					}
					return verifiedIdentityClaims{issuer: "issuer", subject: "subject", displayName: "Member"}, nil
				},
				func(context.Context, verifiedIdentityClaims) (createdInitialSession, error) {
					order = append(order, "create")
					if failingStage == "create" {
						return createdInitialSession{}, cause
					}
					return createdInitialSession{token: "token", expiresAt: time.Now()}, nil
				},
				"state", "code",
			)
			if err == nil || strings.Contains(err.Error(), cause.Error()) || got != (completedInitialLogin{}) {
				t.Fatalf("completeInitialLogin() = (%+v, %v)", got, err)
			}
			wantOrder := map[string]string{"consume": "consume", "exchange": "consume,exchange", "create": "consume,exchange,create"}[failingStage]
			if strings.Join(order, ",") != wantOrder {
				t.Fatalf("workflow order = %v, want %s", order, wantOrder)
			}
		})
	}
}

func TestCompleteInitialLoginRejectsInvalidBoundaryBeforeWork(t *testing.T) {
	t.Parallel()

	panicConsume := func(context.Context, string) (consumedInitialLogin, error) { panic("consume must not run") }
	panicExchange := func(context.Context, string, loginMaterial) (verifiedIdentityClaims, error) {
		panic("exchange must not run")
	}
	panicCreate := func(context.Context, verifiedIdentityClaims) (createdInitialSession, error) {
		panic("create must not run")
	}
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name     string
		ctx      context.Context
		consume  func(context.Context, string) (consumedInitialLogin, error)
		exchange func(context.Context, string, loginMaterial) (verifiedIdentityClaims, error)
		create   func(context.Context, verifiedIdentityClaims) (createdInitialSession, error)
		state    string
		code     string
		cause    error
	}{
		{name: "nil context", consume: panicConsume, exchange: panicExchange, create: panicCreate, state: "state", code: "code"},
		{name: "nil consume", ctx: context.Background(), exchange: panicExchange, create: panicCreate, state: "state", code: "code"},
		{name: "nil exchange", ctx: context.Background(), consume: panicConsume, create: panicCreate, state: "state", code: "code"},
		{name: "nil create", ctx: context.Background(), consume: panicConsume, exchange: panicExchange, state: "state", code: "code"},
		{name: "empty state", ctx: context.Background(), consume: panicConsume, exchange: panicExchange, create: panicCreate, code: "code"},
		{name: "empty code", ctx: context.Background(), consume: panicConsume, exchange: panicExchange, create: panicCreate, state: "state"},
		{name: "canceled context", ctx: canceledContext, consume: panicConsume, exchange: panicExchange, create: panicCreate, state: "state", code: "code", cause: context.Canceled},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := completeInitialLogin(test.ctx, test.consume, test.exchange, test.create, test.state, test.code)
			if err == nil || got != (completedInitialLogin{}) || test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("completeInitialLogin() = (%+v, %v)", got, err)
			}
		})
	}
}

func TestCompleteInitialLoginRejectsIncompleteSuccessfulStageResults(t *testing.T) {
	t.Parallel()

	claims := verifiedIdentityClaims{issuer: "issuer", subject: "subject", displayName: "Member"}
	validCreated := createdInitialSession{token: "token", userID: 1, sessionID: 2, expiresAt: time.Now()}
	for _, test := range []struct {
		name       string
		returnPath string
		created    createdInitialSession
	}{
		{name: "empty return path", created: validCreated},
		{name: "empty token", returnPath: "/bb/", created: createdInitialSession{userID: 1, sessionID: 2, expiresAt: time.Now()}},
		{name: "empty user", returnPath: "/bb/", created: createdInitialSession{token: "token", sessionID: 2, expiresAt: time.Now()}},
		{name: "empty session", returnPath: "/bb/", created: createdInitialSession{token: "token", userID: 1, expiresAt: time.Now()}},
		{name: "empty expiry", returnPath: "/bb/", created: createdInitialSession{token: "token", userID: 1, sessionID: 2}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := completeInitialLogin(
				context.Background(),
				func(context.Context, string) (consumedInitialLogin, error) {
					return consumedInitialLogin{returnPath: test.returnPath}, nil
				},
				func(context.Context, string, loginMaterial) (verifiedIdentityClaims, error) { return claims, nil },
				func(context.Context, verifiedIdentityClaims) (createdInitialSession, error) { return test.created, nil },
				"state", "code",
			)
			if got != (completedInitialLogin{}) || err == nil {
				t.Fatalf("completeInitialLogin() = (%+v, %v), want zero/error", got, err)
			}
		})
	}
}

func TestCompleteInitialLoginPreservesCancellationRaisedByEachStage(t *testing.T) {
	t.Parallel()

	const secret = "do-not-leak-stage-cause"
	for _, canceledStage := range []string{"consume", "exchange", "create"} {
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
			got, err := completeInitialLogin(
				ctx,
				func(context.Context, string) (consumedInitialLogin, error) {
					return consumedInitialLogin{returnPath: "/bb/"}, fail("consume")
				},
				func(context.Context, string, loginMaterial) (verifiedIdentityClaims, error) {
					return verifiedIdentityClaims{issuer: "issuer", subject: "subject", displayName: "Member"}, fail("exchange")
				},
				func(context.Context, verifiedIdentityClaims) (createdInitialSession, error) {
					return createdInitialSession{token: "token", userID: 1, sessionID: 2, expiresAt: time.Now()}, fail("create")
				},
				"state", "code",
			)
			if got != (completedInitialLogin{}) || !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), secret) {
				t.Fatalf("completeInitialLogin() = (%+v, %v)", got, err)
			}
		})
	}
}
