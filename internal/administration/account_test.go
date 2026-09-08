package administration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestAccountAdministrationReadModelsClosePaginationAndMarkerRows(t *testing.T) {
	t.Parallel()
	actor := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}
	observedAt := time.Date(2026, 9, 8, 9, 0, 0, 123456000, time.UTC)
	createdAt := pgtype.Timestamptz{Time: observedAt.Add(-time.Hour), Valid: true}
	rows := make([]db.ListAccountsForAdministrationRow, 51)
	for index := range rows {
		rows[index] = db.ListAccountsForAdministrationRow{
			AccountPresent: true, ID: int64(index + 1), DisplayName: fmt.Sprintf("Account %02d", index+1), Role: "member",
			CreatedAt: createdAt, UpdatedAt: createdAt, AdministrationRevision: 1,
		}
	}
	querier := accountReadTestQuerier{accounts: rows}
	page, err := ListAccounts(context.Background(), querier, actor, observedAt, 0)
	if err != nil || len(page.Accounts) != 50 || page.NextAfter != 50 || page.Accounts[49].ID != 50 {
		t.Fatalf("ListAccounts() = (%+v, %v)", page, err)
	}
	if querierFailure := func() error {
		_, listErr := ListAccounts(context.Background(), accountReadTestQuerier{}, actor, observedAt, 0)
		return listErr
	}(); !errors.Is(querierFailure, ErrAccountAdministrationDenied) {
		t.Fatalf("empty unauthorized rows error = %v", querierFailure)
	}
	empty, err := ListAccounts(context.Background(), accountReadTestQuerier{accounts: []db.ListAccountsForAdministrationRow{{}}}, actor, observedAt, 0)
	if err != nil || len(empty.Accounts) != 0 || empty.NextAfter != 0 {
		t.Fatalf("authorized empty account page = (%+v, %v)", empty, err)
	}
	if _, err := ListAccounts(context.Background(), accountReadTestQuerier{accounts: []db.ListAccountsForAdministrationRow{{AccountPresent: true, ID: 1, DisplayName: "A", Role: "invented", CreatedAt: createdAt, UpdatedAt: createdAt, AdministrationRevision: 1}}}, actor, observedAt, 0); !errors.Is(err, ErrAccountAdministrationUnavailable) {
		t.Fatalf("malformed account error = %v", err)
	}
}

func TestAccountAdministrationDetailAndGroupsDistinguishDeniedFromMissing(t *testing.T) {
	t.Parallel()
	actor := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}
	observedAt := time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)
	if _, err := LoadAccount(context.Background(), accountReadTestQuerier{load: db.LoadAccountForAdministrationRow{}}, actor, observedAt, 41); !errors.Is(err, ErrAccountAdministrationNotFound) {
		t.Fatalf("missing account error = %v", err)
	}
	if _, err := ListAccountGroups(context.Background(), accountReadTestQuerier{groups: []db.ListAccountGroupsForAdministrationRow{{AccountPresent: false}}}, actor, observedAt, 41, 0); !errors.Is(err, ErrAccountAdministrationNotFound) {
		t.Fatalf("missing account groups error = %v", err)
	}
	page, err := ListAccountGroups(context.Background(), accountReadTestQuerier{groups: []db.ListAccountGroupsForAdministrationRow{{AccountPresent: true}}}, actor, observedAt, 41, 0)
	if err != nil || len(page.Groups) != 0 {
		t.Fatalf("empty account groups = (%+v, %v)", page, err)
	}
	if _, err := LoadAccount(context.Background(), accountReadTestQuerier{loadErr: pgx.ErrNoRows}, actor, observedAt, 41); !errors.Is(err, ErrAccountAdministrationDenied) {
		t.Fatalf("absent actor detail error = %v", err)
	}
}

func TestAccountAdministrationClosedInputGrammars(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]bool{
		"Members": true, " Café ": false, "Cafe\u0301": false, "": false, "line\nbreak": false,
	} {
		if got := validAdministrationGroupName(value); got != want {
			t.Fatalf("validAdministrationGroupName(%q) = %t, want %t", value, got, want)
		}
	}
	for value, want := range map[string]bool{"Reason": true, "Cafe\u0301": true, " padded ": false, "line\nbreak": false, "": false} {
		if got := validAdministrationReason(value); got != want {
			t.Fatalf("validAdministrationReason(%q) = %t, want %t", value, got, want)
		}
	}
	if validAdministrationRole(0) || validAdministrationRole(policy.Role(99)) || !validAdministrationRole(policy.RoleAdministrator) {
		t.Fatal("role grammar is not closed")
	}
}

func TestAccountAdministrationReadPreservesQueryCause(t *testing.T) {
	t.Parallel()
	cause := errors.New("query canceled")
	actor := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}
	_, err := ListAccounts(context.Background(), accountReadTestQuerier{accountsErr: cause}, actor, time.Now(), 0)
	if !errors.Is(err, ErrAccountAdministrationUnavailable) || !errors.Is(err, cause) {
		t.Fatalf("ListAccounts() error = %v, want unavailable and cause", err)
	}
}

type accountReadTestQuerier struct {
	accounts    []db.ListAccountsForAdministrationRow
	accountsErr error
	load        db.LoadAccountForAdministrationRow
	loadErr     error
	groups      []db.ListAccountGroupsForAdministrationRow
	all         []db.ListGroupsForAdministrationRow
}

func (querier accountReadTestQuerier) ListAccountsForAdministration(context.Context, db.ListAccountsForAdministrationParams) ([]db.ListAccountsForAdministrationRow, error) {
	return querier.accounts, querier.accountsErr
}

func (querier accountReadTestQuerier) LoadAccountForAdministration(context.Context, db.LoadAccountForAdministrationParams) (db.LoadAccountForAdministrationRow, error) {
	return querier.load, querier.loadErr
}

func (querier accountReadTestQuerier) ListAccountGroupsForAdministration(context.Context, db.ListAccountGroupsForAdministrationParams) ([]db.ListAccountGroupsForAdministrationRow, error) {
	return querier.groups, nil
}

func (querier accountReadTestQuerier) ListGroupsForAdministration(context.Context, db.ListGroupsForAdministrationParams) ([]db.ListGroupsForAdministrationRow, error) {
	return querier.all, nil
}
