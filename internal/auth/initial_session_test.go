package auth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type panicTransactionBeginner struct{}

func (panicTransactionBeginner) Begin(context.Context) (pgx.Tx, error) {
	panic("transaction must not begin")
}

type failingTransactionBeginner struct {
	cause  error
	called bool
}

func (beginner *failingTransactionBeginner) Begin(context.Context) (pgx.Tx, error) {
	beginner.called = true
	return nil, beginner.cause
}

func TestCreateInitialSessionRejectsInvalidInputBeforeEntropyOrTransaction(t *testing.T) {
	t.Parallel()

	validClaims := verifiedIdentityClaims{issuer: "issuer", subject: "subject", displayName: "Member"}
	validClock := func() time.Time { return time.Date(2026, time.September, 1, 11, 40, 0, 0, time.UTC) }
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name      string
		ctx       context.Context
		beginner  transactionBeginner
		entropy   io.Reader
		clock     func() time.Time
		maxAge    time.Duration
		claims    verifiedIdentityClaims
		wantCause error
	}{
		{name: "nil context", beginner: panicTransactionBeginner{}, entropy: bytes.NewReader(make([]byte, 32)), clock: validClock, maxAge: time.Hour, claims: validClaims},
		{name: "nil beginner", ctx: context.Background(), entropy: bytes.NewReader(make([]byte, 32)), clock: validClock, maxAge: time.Hour, claims: validClaims},
		{name: "nil entropy", ctx: context.Background(), beginner: panicTransactionBeginner{}, clock: validClock, maxAge: time.Hour, claims: validClaims},
		{name: "nil clock", ctx: context.Background(), beginner: panicTransactionBeginner{}, entropy: bytes.NewReader(make([]byte, 32)), maxAge: time.Hour, claims: validClaims},
		{name: "zero maximum age", ctx: context.Background(), beginner: panicTransactionBeginner{}, entropy: bytes.NewReader(make([]byte, 32)), clock: validClock, claims: validClaims},
		{name: "negative maximum age", ctx: context.Background(), beginner: panicTransactionBeginner{}, entropy: bytes.NewReader(make([]byte, 32)), clock: validClock, maxAge: -time.Second, claims: validClaims},
		{name: "sub-microsecond maximum age", ctx: context.Background(), beginner: panicTransactionBeginner{}, entropy: bytes.NewReader(make([]byte, 32)), clock: validClock, maxAge: time.Microsecond - time.Nanosecond, claims: validClaims},
		{name: "incomplete claims", ctx: context.Background(), beginner: panicTransactionBeginner{}, entropy: bytes.NewReader(make([]byte, 32)), clock: validClock, maxAge: time.Hour},
		{name: "canceled context", ctx: canceledContext, beginner: panicTransactionBeginner{}, entropy: bytes.NewReader(make([]byte, 32)), clock: validClock, maxAge: time.Hour, claims: validClaims, wantCause: context.Canceled},
		{name: "zero clock", ctx: context.Background(), beginner: panicTransactionBeginner{}, entropy: bytes.NewReader(make([]byte, 32)), clock: func() time.Time { return time.Time{} }, maxAge: time.Hour, claims: validClaims},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := createInitialSession(test.ctx, test.beginner, test.entropy, test.clock, test.maxAge, test.claims)
			if err == nil || got != (createdInitialSession{}) {
				t.Fatalf("createInitialSession() = (%+v, %v), want zero/error", got, err)
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("error = %v, want cause %v", err, test.wantCause)
			}
		})
	}
}

func TestCreateInitialSessionReturnsNoCredentialWhenEntropyOrBeginFails(t *testing.T) {
	t.Parallel()

	claims := verifiedIdentityClaims{issuer: "issuer", subject: "subject", displayName: "Member"}
	clock := func() time.Time { return time.Date(2026, time.September, 1, 11, 45, 0, 0, time.UTC) }
	entropyCause := errors.New("entropy unavailable")
	got, err := createInitialSession(
		context.Background(), panicTransactionBeginner{}, errReader{cause: entropyCause}, clock, time.Hour, claims,
	)
	if !errors.Is(err, entropyCause) || got != (createdInitialSession{}) {
		t.Fatalf("entropy failure = (%+v, %v), want zero/%v", got, err, entropyCause)
	}

	beginCause := errors.New("database unavailable")
	beginner := &failingTransactionBeginner{cause: beginCause}
	got, err = createInitialSession(
		context.Background(), beginner, bytes.NewReader(make([]byte, sessionTokenBytes)), clock, time.Hour, claims,
	)
	if !beginner.called || !errors.Is(err, beginCause) || got != (createdInitialSession{}) {
		t.Fatalf("begin failure = (called %t, %+v, %v), want true/zero/%v", beginner.called, got, err, beginCause)
	}
}
