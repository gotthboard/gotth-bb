package auth

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/store/db"
)

func TestBeginRevalidationBindsGeneratedAttemptToExactSession(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 1, 19, 10, 0, 123456789, time.UTC)
	rawEntropy := sequentialBytes(120)
	var inserted db.InsertOIDCLoginAttemptParams
	material, err := beginRevalidation(
		context.Background(),
		func(_ context.Context, params db.InsertOIDCLoginAttemptParams) error {
			inserted = params
			return nil
		},
		bytes.NewReader(rawEntropy),
		func() time.Time { return now },
		func(raw string) (string, error) {
			if raw != "/bb/topics/42" {
				t.Fatalf("return path input = %q", raw)
			}
			return raw, nil
		},
		73,
		"/bb/topics/42",
	)
	if err != nil || material == (loginMaterial{}) {
		t.Fatalf("beginRevalidation() = (%+v, %v)", material, err)
	}
	if inserted.Purpose != "revalidate" || !inserted.SessionID.Valid || inserted.SessionID.Int64 != 73 ||
		inserted.ReturnPath != "/bb/topics/42" || !inserted.CreatedAt.Time.Equal(now.UTC().Truncate(time.Microsecond)) ||
		!inserted.ExpiresAt.Time.Equal(now.UTC().Truncate(time.Microsecond).Add(initialLoginAttemptLifetime)) {
		t.Fatalf("inserted attempt = %+v", inserted)
	}
	wantMaterial, err := generateLoginMaterial(bytes.NewReader(rawEntropy[:96]))
	if err != nil || !reflect.DeepEqual(material, wantMaterial) {
		t.Fatalf("generated material = (%+v, %v), want %+v", material, err, wantMaterial)
	}
}

func TestBeginRevalidationRejectsInvalidBindingBeforeSideEffects(t *testing.T) {
	t.Parallel()

	validInsert := insertOIDCLoginAttempt(func(context.Context, db.InsertOIDCLoginAttemptParams) error { return nil })
	for _, test := range []struct {
		name      string
		insert    insertOIDCLoginAttempt
		sessionID int64
	}{
		{name: "insert", sessionID: 1},
		{name: "zero session", insert: validInsert},
		{name: "negative session", insert: validInsert, sessionID: -1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			material, err := beginRevalidation(
				context.Background(), test.insert, panicReader{}, time.Now,
				func(string) (string, error) { panic("validation must not run") },
				test.sessionID, "/bb/",
			)
			if err == nil || material != (loginMaterial{}) {
				t.Fatalf("beginRevalidation() = (%+v, %v), want zero/error", material, err)
			}
		})
	}
}
