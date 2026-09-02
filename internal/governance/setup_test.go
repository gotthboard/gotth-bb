package governance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestLoadInitialAdministratorSetup(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 2, 15, 0, 0, 123456789, time.UTC)
	for _, test := range []struct {
		name          string
		database      *setupStatusDatabase
		userID        int64
		want          InitialAdministratorSetupStatus
		wantError     bool
		wantQueryCall int
	}{
		{name: "anonymous open", database: &setupStatusDatabase{identityUserID: 41}, want: InitialAdministratorSetupStatus{Open: true}, wantQueryCall: 2},
		{name: "eligible open", database: &setupStatusDatabase{identityUserID: 41}, userID: 41, want: InitialAdministratorSetupStatus{Open: true, Eligible: true}, wantQueryCall: 3},
		{name: "wrong user", database: &setupStatusDatabase{identityUserID: 41}, userID: 42, want: InitialAdministratorSetupStatus{Open: true}, wantQueryCall: 3},
		{name: "identity absent", database: &setupStatusDatabase{identityMissing: true}, userID: 42, want: InitialAdministratorSetupStatus{Open: true}, wantQueryCall: 3},
		{name: "historically closed", database: &setupStatusDatabase{bootstraps: 1}, userID: 41, wantQueryCall: 2},
		{name: "active administrator", database: &setupStatusDatabase{administrators: 1}, userID: 41, wantQueryCall: 2},
		{name: "bootstrap count failure", database: &setupStatusDatabase{failureAt: 1}, wantError: true, wantQueryCall: 1},
		{name: "administrator count failure", database: &setupStatusDatabase{failureAt: 2}, wantError: true, wantQueryCall: 2},
		{name: "identity failure", database: &setupStatusDatabase{failureAt: 3}, userID: 41, wantError: true, wantQueryCall: 3},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := LoadInitialAdministratorSetup(context.Background(), db.New(test.database), func() time.Time { return now }, test.userID, "issuer", "subject")
			if (err != nil) != test.wantError || got != test.want || test.database.calls != test.wantQueryCall {
				t.Fatalf("LoadInitialAdministratorSetup() = (%+v, %v, %d queries), want (%+v, error %t, %d)", got, err, test.database.calls, test.want, test.wantError, test.wantQueryCall)
			}
		})
	}
}

func TestLoadInitialAdministratorSetupRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	validQueries := db.New(&setupStatusDatabase{})
	validClock := func() time.Time { return time.Date(2026, time.September, 2, 15, 0, 0, 0, time.UTC) }
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name    string
		ctx     context.Context
		queries *db.Queries
		clock   func() time.Time
		userID  int64
		issuer  string
		subject string
	}{
		{name: "nil context", queries: validQueries, clock: validClock, issuer: "issuer", subject: "subject"},
		{name: "nil queries", ctx: context.Background(), clock: validClock, issuer: "issuer", subject: "subject"},
		{name: "nil clock", ctx: context.Background(), queries: validQueries, issuer: "issuer", subject: "subject"},
		{name: "negative user", ctx: context.Background(), queries: validQueries, clock: validClock, userID: -1, issuer: "issuer", subject: "subject"},
		{name: "bad issuer", ctx: context.Background(), queries: validQueries, clock: validClock, issuer: "", subject: "subject"},
		{name: "bad subject", ctx: context.Background(), queries: validQueries, clock: validClock, issuer: "issuer", subject: "subject\n"},
		{name: "canceled", ctx: canceled, queries: validQueries, clock: validClock, issuer: "issuer", subject: "subject"},
		{name: "zero clock", ctx: context.Background(), queries: validQueries, clock: func() time.Time { return time.Time{} }, issuer: "issuer", subject: "subject"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got, err := LoadInitialAdministratorSetup(test.ctx, test.queries, test.clock, test.userID, test.issuer, test.subject); err == nil || got != (InitialAdministratorSetupStatus{}) {
				t.Fatalf("LoadInitialAdministratorSetup() = (%+v, %v), want error", got, err)
			}
		})
	}
}

func TestClaimInitialAdministrator(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 2, 15, 10, 0, 123456789, time.UTC)
	requestID := pgtype.UUID{Bytes: [16]byte{1, 2, 3}, Valid: true}
	for _, test := range []struct {
		name      string
		failure   string
		want      InitialAdministratorClaimResult
		wantCause error
	}{
		{name: "success", want: InitialAdministratorClaimResult{UserID: 41, AuditID: 73, RevokedSessionID: 91}},
		{name: "closed by history", failure: "existing bootstrap", wantCause: ErrAdministratorSetupClosed},
		{name: "closed by administrator", failure: "existing administrator", wantCause: ErrAdministratorSetupClosed},
		{name: "identity denied", failure: "claim missing", wantCause: ErrAdministratorSetupDenied},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tx := &setupClaimTx{failure: test.failure}
			got, err := ClaimInitialAdministrator(context.Background(), setupTestBeginner{tx: tx}, func() time.Time { return now }, 41, 91, "issuer", "subject", requestID)
			if test.wantCause == nil {
				if err != nil || got != test.want || !tx.commitCalled || tx.rollbackCalled {
					t.Fatalf("ClaimInitialAdministrator() = (%+v, %v), transaction (%t, %t)", got, err, tx.commitCalled, tx.rollbackCalled)
				}
				return
			}
			if err == nil || !errors.Is(err, test.wantCause) || got != (InitialAdministratorClaimResult{}) || !tx.rollbackCalled {
				t.Fatalf("ClaimInitialAdministrator() = (%+v, %v), rollback %t", got, err, tx.rollbackCalled)
			}
		})
	}
}

type setupStatusDatabase struct {
	pgx.Tx
	calls           int
	failureAt       int
	bootstraps      int64
	administrators  int64
	identityUserID  int64
	identityMissing bool
}

type setupTestBeginner struct {
	tx pgx.Tx
}

func (beginner setupTestBeginner) Begin(context.Context) (pgx.Tx, error) {
	return beginner.tx, nil
}

func (database *setupStatusDatabase) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	database.calls++
	call := database.calls
	return bootstrapTestRow{scan: func(destinations ...any) error {
		if database.failureAt == call {
			return errors.New("query failed")
		}
		switch {
		case strings.Contains(query, "CountAdministratorBootstraps"):
			*(destinations[0].(*int64)) = database.bootstraps
		case strings.Contains(query, "CountActiveAdministrators"):
			*(destinations[0].(*int64)) = database.administrators
		case strings.Contains(query, "GetUserByExternalIdentity"):
			if database.identityMissing {
				return pgx.ErrNoRows
			}
			*(destinations[0].(*int64)) = database.identityUserID
		default:
			return errors.New("unexpected query")
		}
		return nil
	}}
}

type setupClaimTx struct {
	pgx.Tx
	failure        string
	calls          int
	commitCalled   bool
	rollbackCalled bool
}

func (tx *setupClaimTx) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	tx.calls++
	return bootstrapTestRow{scan: func(destinations ...any) error {
		switch {
		case strings.Contains(query, "LockGovernanceState"):
			*(destinations[0].(*bool)) = true
		case strings.Contains(query, "CountAdministratorBootstraps"):
			if tx.failure == "existing bootstrap" {
				*(destinations[0].(*int64)) = 1
			}
		case strings.Contains(query, "CountActiveAdministrators"):
			if tx.failure == "existing administrator" {
				*(destinations[0].(*int64)) = 1
			}
		case strings.Contains(query, "ClaimInitialAdministratorAndAudit"):
			if tx.failure == "claim missing" {
				return pgx.ErrNoRows
			}
			*(destinations[0].(*int64)) = 41
			*(destinations[1].(*int64)) = 73
			*(destinations[2].(*int64)) = 91
		default:
			return errors.New("unexpected query")
		}
		return nil
	}}
}

func (tx *setupClaimTx) Commit(context.Context) error {
	tx.commitCalled = true
	return nil
}

func (tx *setupClaimTx) Rollback(context.Context) error {
	tx.rollbackCalled = true
	return nil
}
