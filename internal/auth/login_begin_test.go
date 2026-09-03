package auth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/store/db"
)

func TestBeginInitialLoginValidatesProtectsAndPersistsExactAttempt(t *testing.T) {
	t.Parallel()

	rawEntropy := make([]byte, 120)
	for index := range rawEntropy {
		rawEntropy[index] = byte(index)
	}
	wantMaterial, err := generateLoginMaterial(bytes.NewReader(rawEntropy[:96]))
	if err != nil {
		t.Fatalf("generateLoginMaterial() returned error: %v", err)
	}
	wantProtected, err := protectLoginMaterial(wantMaterial, bytes.NewReader(rawEntropy[96:]))
	if err != nil {
		t.Fatalf("protectLoginMaterial() returned error: %v", err)
	}
	now := time.Date(2026, time.September, 1, 10, 40, 0, 123456789, time.FixedZone("test", -5*60*60))
	ctx := context.WithValue(context.Background(), beginLoginContextKey{}, "preserved")
	insertCalls := 0
	insert := func(gotContext context.Context, params db.InsertOIDCLoginAttemptParams) error {
		insertCalls++
		if gotContext != ctx || gotContext.Value(beginLoginContextKey{}) != "preserved" {
			t.Fatal("insert did not receive the original request context")
		}
		wantCreated := now.UTC().Truncate(time.Microsecond)
		if !bytes.Equal(params.StateHash, wantProtected.stateHash[:]) ||
			!bytes.Equal(params.NonceCiphertext, wantProtected.nonceCiphertext[:]) ||
			!bytes.Equal(params.PkceVerifierCiphertext, wantProtected.pkceVerifierCiphertext[:]) ||
			params.Purpose != "login" || params.SessionID.Valid ||
			params.ReturnPath != "/bb/topics/42" ||
			!params.CreatedAt.Valid || !params.CreatedAt.Time.Equal(wantCreated) ||
			!params.ExpiresAt.Valid || !params.ExpiresAt.Time.Equal(wantCreated.Add(5*time.Minute)) {
			t.Fatal("insert received incorrect login-attempt parameters")
		}
		return nil
	}
	validateCalls := 0
	validate := func(raw string) (string, error) {
		validateCalls++
		if raw != "/topics/42" {
			t.Fatalf("validator input = %q", raw)
		}
		return "/bb/topics/42", nil
	}
	material, err := beginInitialLogin(ctx, insert, bytes.NewReader(rawEntropy), func() time.Time { return now }, validate, "/topics/42")
	if err != nil || material != wantMaterial || insertCalls != 1 || validateCalls != 1 {
		t.Fatalf("beginInitialLogin() returned expected material = %v, insert calls = %d, validate calls = %d, error = %v", material == wantMaterial, insertCalls, validateCalls, err)
	}
}

func TestBeginInitialLoginRejectsInvalidDependenciesAndInputBeforeSideEffects(t *testing.T) {
	t.Parallel()

	validInsert := func(context.Context, db.InsertOIDCLoginAttemptParams) error { return nil }
	validClock := func() time.Time { return time.Date(2026, time.September, 1, 10, 40, 0, 0, time.UTC) }
	validValidate := func(raw string) (string, error) { return raw, nil }
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	validationCause := errors.New("invalid return path")
	for _, test := range []struct {
		name      string
		ctx       context.Context
		insert    insertOIDCLoginAttempt
		clock     func() time.Time
		validate  func(string) (string, error)
		returnRaw string
		wantErr   error
	}{
		{name: "nil context", insert: validInsert, clock: validClock, validate: validValidate},
		{name: "nil insert", ctx: context.Background(), clock: validClock, validate: validValidate},
		{name: "nil clock", ctx: context.Background(), insert: validInsert, validate: validValidate},
		{name: "nil validator", ctx: context.Background(), insert: validInsert, clock: validClock},
		{name: "canceled context", ctx: canceledContext, insert: validInsert, clock: validClock, validate: validValidate, wantErr: context.Canceled},
		{name: "validation failure", ctx: context.Background(), insert: validInsert, clock: validClock, validate: func(string) (string, error) { return "", validationCause }, wantErr: validationCause},
		{name: "validator empty result", ctx: context.Background(), insert: validInsert, clock: validClock, validate: func(string) (string, error) { return "", nil }},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			material, err := beginInitialLogin(test.ctx, test.insert, panicReader{}, test.clock, test.validate, test.returnRaw)
			if err == nil || material != (loginMaterial{}) {
				t.Fatalf("beginInitialLogin() returned zero material = %v, error = %v", material == (loginMaterial{}), err)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want cause %v", err, test.wantErr)
			}
		})
	}
}

func TestBeginInitialLoginReturnsZeroMaterialOnClockEntropyOrInsertFailure(t *testing.T) {
	t.Parallel()

	validValidate := func(raw string) (string, error) { return raw, nil }
	validNow := time.Date(2026, time.September, 1, 10, 40, 0, 0, time.UTC)
	insertCause := errors.New("insert failed")
	entropyCause := errors.New("entropy failed")
	for _, test := range []struct {
		name    string
		reader  io.Reader
		clock   func() time.Time
		insert  insertOIDCLoginAttempt
		wantErr error
	}{
		{name: "zero clock", reader: panicReader{}, clock: func() time.Time { return time.Time{} }, insert: func(context.Context, db.InsertOIDCLoginAttemptParams) error { panic("insert must not be called") }},
		{name: "entropy failure", reader: errReader{cause: entropyCause}, clock: func() time.Time { return validNow }, insert: func(context.Context, db.InsertOIDCLoginAttemptParams) error { panic("insert must not be called") }, wantErr: entropyCause},
		{name: "protection entropy failure", reader: bytes.NewReader(sequentialBytes(119)), clock: func() time.Time { return validNow }, insert: func(context.Context, db.InsertOIDCLoginAttemptParams) error { panic("insert must not be called") }, wantErr: io.ErrUnexpectedEOF},
		{name: "insert failure", reader: bytes.NewReader(sequentialBytes(120)), clock: func() time.Time { return validNow }, insert: func(context.Context, db.InsertOIDCLoginAttemptParams) error { return insertCause }, wantErr: insertCause},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			material, err := beginInitialLogin(context.Background(), test.insert, test.reader, test.clock, validValidate, "/bb/")
			if err == nil || material != (loginMaterial{}) {
				t.Fatalf("beginInitialLogin() returned zero material = %v, error = %v", material == (loginMaterial{}), err)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want cause %v", err, test.wantErr)
			}
		})
	}
}

type beginLoginContextKey struct{}

func sequentialBytes(length int) []byte {
	result := make([]byte, length)
	for index := range result {
		result[index] = byte(index)
	}
	return result
}
