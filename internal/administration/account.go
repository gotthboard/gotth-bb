package administration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/text/unicode/norm"
)

const (
	administrationPageQueryLimit  int32 = 51
	administrationPageSize              = 50
	accountMutationTimeout              = 2 * time.Second
	maximumAdministrationRevision       = int64(^uint64(0) >> 1)
)

var (
	ErrAccountAdministrationInput       = errors.New("invalid account administration input")
	ErrAccountAdministrationDenied      = errors.New("account administration denied")
	ErrAccountAdministrationNotFound    = errors.New("account administration target not found")
	ErrAccountAdministrationConflict    = errors.New("account administration conflict")
	ErrAccountAdministratorContinuity   = errors.New("administrator continuity required")
	ErrAccountAdministrationUnavailable = errors.New("account administration unavailable")
)

type AccountSummary struct {
	ID          int64
	DisplayName string
	Role        policy.Role
	Suspended   bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Revision    int64
}

type AccountPage struct {
	Accounts  []AccountSummary
	NextAfter int64
}

type AccountGroup struct {
	ID     int64
	Name   string
	Member bool
}

type AccountGroupPage struct {
	Groups    []AccountGroup
	NextAfter int64
}

type GroupSummary struct {
	ID        int64
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
	Revision  int64
}

type GroupPage struct {
	Groups    []GroupSummary
	NextAfter int64
}

type GroupMutationResult struct {
	GroupID  int64
	Name     string
	Revision int64
	AuditID  int64
}

type AccountMutationResult struct {
	UserID          int64
	Role            policy.Role
	Revision        int64
	AuditID         int64
	RevokedSessions int64
}

type accountAdministrationQuerier interface {
	ListAccountsForAdministration(context.Context, db.ListAccountsForAdministrationParams) ([]db.ListAccountsForAdministrationRow, error)
	LoadAccountForAdministration(context.Context, db.LoadAccountForAdministrationParams) (db.LoadAccountForAdministrationRow, error)
	ListAccountGroupsForAdministration(context.Context, db.ListAccountGroupsForAdministrationParams) ([]db.ListAccountGroupsForAdministrationRow, error)
	ListGroupsForAdministration(context.Context, db.ListGroupsForAdministrationParams) ([]db.ListGroupsForAdministrationRow, error)
}

type accountTransactionBeginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

func ListAccounts(ctx context.Context, querier accountAdministrationQuerier, actor policy.AccessContext, observedAt time.Time, afterUserID int64) (AccountPage, error) {
	if err := validateAccountReadBoundary(ctx, querier, actor, observedAt, afterUserID); err != nil {
		return AccountPage{}, err
	}
	rows, err := querier.ListAccountsForAdministration(ctx, db.ListAccountsForAdministrationParams{
		ObservedAt: administrationTime(observedAt), ActorUserID: actor.UserID,
		AfterUserID: afterUserID, PageLimit: administrationPageQueryLimit,
	})
	if err != nil {
		return AccountPage{}, fmt.Errorf("%w: list accounts", ErrAccountAdministrationUnavailable)
	}
	if len(rows) == 0 {
		return AccountPage{}, ErrAccountAdministrationDenied
	}
	if len(rows) > int(administrationPageQueryLimit) {
		return AccountPage{}, malformedAdministrationRows("accounts")
	}
	page := AccountPage{Accounts: make([]AccountSummary, 0, min(len(rows), administrationPageSize))}
	previousID := afterUserID
	for index, row := range rows {
		if !row.AccountPresent {
			if len(rows) != 1 || index != 0 || !emptyAccountRow(row) {
				return AccountPage{}, malformedAdministrationRows("accounts")
			}
			return page, nil
		}
		account, valid := accountFromRow(row)
		if !valid || account.ID <= previousID {
			return AccountPage{}, malformedAdministrationRows("accounts")
		}
		previousID = account.ID
		if index == administrationPageSize {
			page.NextAfter = page.Accounts[len(page.Accounts)-1].ID
			continue
		}
		page.Accounts = append(page.Accounts, account)
	}
	return page, nil
}

func LoadAccount(ctx context.Context, querier accountAdministrationQuerier, actor policy.AccessContext, observedAt time.Time, targetUserID int64) (AccountSummary, error) {
	if targetUserID <= 0 {
		return AccountSummary{}, fmt.Errorf("%w: target", ErrAccountAdministrationInput)
	}
	if err := validateAccountReadBoundary(ctx, querier, actor, observedAt, 0); err != nil {
		return AccountSummary{}, err
	}
	row, err := querier.LoadAccountForAdministration(ctx, db.LoadAccountForAdministrationParams{
		ObservedAt: administrationTime(observedAt), ActorUserID: actor.UserID, TargetUserID: targetUserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountSummary{}, ErrAccountAdministrationDenied
	}
	if err != nil {
		return AccountSummary{}, fmt.Errorf("%w: load account", ErrAccountAdministrationUnavailable)
	}
	if !row.AccountPresent {
		if !emptyLoadedAccountRow(row) {
			return AccountSummary{}, malformedAdministrationRows("account")
		}
		return AccountSummary{}, ErrAccountAdministrationNotFound
	}
	account, valid := loadedAccountFromRow(row)
	if !valid || account.ID != targetUserID {
		return AccountSummary{}, malformedAdministrationRows("account")
	}
	return account, nil
}

func ListAccountGroups(ctx context.Context, querier accountAdministrationQuerier, actor policy.AccessContext, observedAt time.Time, targetUserID, afterGroupID int64) (AccountGroupPage, error) {
	if targetUserID <= 0 {
		return AccountGroupPage{}, fmt.Errorf("%w: target", ErrAccountAdministrationInput)
	}
	if err := validateAccountReadBoundary(ctx, querier, actor, observedAt, afterGroupID); err != nil {
		return AccountGroupPage{}, err
	}
	rows, err := querier.ListAccountGroupsForAdministration(ctx, db.ListAccountGroupsForAdministrationParams{
		ActorUserID: actor.UserID, ObservedAt: administrationTime(observedAt), TargetUserID: targetUserID,
		AfterGroupID: afterGroupID, PageLimit: administrationPageQueryLimit,
	})
	if err != nil {
		return AccountGroupPage{}, fmt.Errorf("%w: list account groups", ErrAccountAdministrationUnavailable)
	}
	if len(rows) == 0 {
		return AccountGroupPage{}, ErrAccountAdministrationDenied
	}
	if len(rows) > int(administrationPageQueryLimit) {
		return AccountGroupPage{}, malformedAdministrationRows("account groups")
	}
	page := AccountGroupPage{Groups: make([]AccountGroup, 0, min(len(rows), administrationPageSize))}
	previousID := afterGroupID
	for index, row := range rows {
		if !row.AccountPresent {
			if len(rows) != 1 || index != 0 || row.GroupPresent || row.GroupID != 0 || row.GroupName != "" || row.Member {
				return AccountGroupPage{}, malformedAdministrationRows("account groups")
			}
			return AccountGroupPage{}, ErrAccountAdministrationNotFound
		}
		if !row.GroupPresent {
			if len(rows) != 1 || index != 0 || row.GroupID != 0 || row.GroupName != "" || row.Member {
				return AccountGroupPage{}, malformedAdministrationRows("account groups")
			}
			return page, nil
		}
		if row.GroupID <= previousID || !validAdministrationGroupName(row.GroupName) {
			return AccountGroupPage{}, malformedAdministrationRows("account groups")
		}
		previousID = row.GroupID
		if index == administrationPageSize {
			page.NextAfter = page.Groups[len(page.Groups)-1].ID
			continue
		}
		page.Groups = append(page.Groups, AccountGroup{ID: row.GroupID, Name: row.GroupName, Member: row.Member})
	}
	return page, nil
}

func ListGroups(ctx context.Context, querier accountAdministrationQuerier, actor policy.AccessContext, observedAt time.Time, afterGroupID int64) (GroupPage, error) {
	if err := validateAccountReadBoundary(ctx, querier, actor, observedAt, afterGroupID); err != nil {
		return GroupPage{}, err
	}
	rows, err := querier.ListGroupsForAdministration(ctx, db.ListGroupsForAdministrationParams{
		ActorUserID: actor.UserID, ObservedAt: administrationTime(observedAt), AfterGroupID: afterGroupID, PageLimit: administrationPageQueryLimit,
	})
	if err != nil {
		return GroupPage{}, fmt.Errorf("%w: list groups", ErrAccountAdministrationUnavailable)
	}
	if len(rows) == 0 {
		return GroupPage{}, ErrAccountAdministrationDenied
	}
	if len(rows) > int(administrationPageQueryLimit) {
		return GroupPage{}, malformedAdministrationRows("groups")
	}
	page := GroupPage{Groups: make([]GroupSummary, 0, min(len(rows), administrationPageSize))}
	previousID := afterGroupID
	for index, row := range rows {
		if !row.GroupPresent {
			if len(rows) != 1 || index != 0 || !emptyGroupRow(row) {
				return GroupPage{}, malformedAdministrationRows("groups")
			}
			return page, nil
		}
		group, valid := groupFromRow(row)
		if !valid || group.ID <= previousID {
			return GroupPage{}, malformedAdministrationRows("groups")
		}
		previousID = group.ID
		if index == administrationPageSize {
			page.NextAfter = page.Groups[len(page.Groups)-1].ID
			continue
		}
		page.Groups = append(page.Groups, group)
	}
	return page, nil
}

func CreateGroup(ctx context.Context, beginner accountTransactionBeginner, clock func() time.Time, actor policy.AccessContext, name, reason string, requestID pgtype.UUID) (GroupMutationResult, error) {
	if err := validateAccountMutationBoundary(ctx, beginner, clock, actor, reason, requestID); err != nil {
		return GroupMutationResult{}, err
	}
	if !validAdministrationGroupName(name) {
		return GroupMutationResult{}, fmt.Errorf("%w: group name", ErrAccountAdministrationInput)
	}
	observedAt, err := accountMutationTime(clock)
	if err != nil {
		return GroupMutationResult{}, err
	}
	mutationContext, cancel := context.WithTimeout(ctx, accountMutationTimeout)
	defer cancel()
	result := GroupMutationResult{}
	err = store.WithinTxOptions(mutationContext, beginner, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(queries *db.Queries) error {
		if err := configureAccountTransaction(mutationContext, queries); err != nil {
			return err
		}
		if _, err := lockAccountActor(mutationContext, queries, actor, observedAt); err != nil {
			return err
		}
		created, err := queries.CreateAdministrationGroupAndAudit(mutationContext, db.CreateAdministrationGroupAndAuditParams{
			Name: name, ActorUserID: actor.UserID, ObservedAt: administrationTime(observedAt),
			Reason: administrationReason(reason), RequestID: requestID,
		})
		if err != nil {
			return mapAccountWriteError("create group", err)
		}
		if created.GroupID <= 0 || created.Name != name || created.AdministrationRevision != 1 || created.AuditID <= 0 {
			return fmt.Errorf("create group returned invalid state")
		}
		result = GroupMutationResult{GroupID: created.GroupID, Name: created.Name, Revision: created.AdministrationRevision, AuditID: created.AuditID}
		return nil
	})
	if err != nil {
		return GroupMutationResult{}, fmt.Errorf("create group transaction: %w", err)
	}
	return result, nil
}

func RenameGroup(ctx context.Context, beginner accountTransactionBeginner, clock func() time.Time, actor policy.AccessContext, groupID int64, name, reason string, revision int64, requestID pgtype.UUID) (GroupMutationResult, error) {
	if groupID <= 0 || revision <= 0 || !validAdministrationGroupName(name) {
		return GroupMutationResult{}, fmt.Errorf("%w: group", ErrAccountAdministrationInput)
	}
	if err := validateAccountMutationBoundary(ctx, beginner, clock, actor, reason, requestID); err != nil {
		return GroupMutationResult{}, err
	}
	observedAt, err := accountMutationTime(clock)
	if err != nil {
		return GroupMutationResult{}, err
	}
	mutationContext, cancel := context.WithTimeout(ctx, accountMutationTimeout)
	defer cancel()
	result := GroupMutationResult{}
	err = store.WithinTxOptions(mutationContext, beginner, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(queries *db.Queries) error {
		if err := configureAccountTransaction(mutationContext, queries); err != nil {
			return err
		}
		if _, err := lockAccountActor(mutationContext, queries, actor, observedAt); err != nil {
			return err
		}
		group, err := queries.LockAdministrationGroup(mutationContext, groupID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAccountAdministrationNotFound
		}
		if err != nil {
			return fmt.Errorf("lock administration group: %w", err)
		}
		if !validLockedGroup(group, groupID) {
			return fmt.Errorf("locked administration group is malformed")
		}
		if group.AdministrationRevision != revision || revision == maximumAdministrationRevision || group.Name == name {
			return ErrAccountAdministrationConflict
		}
		changed, err := queries.RenameAdministrationGroupAndAudit(mutationContext, db.RenameAdministrationGroupAndAuditParams{
			Name: name, ObservedAt: administrationTime(observedAt), GroupID: groupID, ExpectedRevision: revision,
			ActorUserID: administrationActor(actor.UserID), Reason: administrationReason(reason), PreviousName: group.Name, RequestID: requestID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAccountAdministrationConflict
		}
		if err != nil {
			return mapAccountWriteError("rename group", err)
		}
		if changed.GroupID != groupID || changed.Name != name || changed.AdministrationRevision != revision+1 || changed.AuditID <= 0 {
			return fmt.Errorf("rename group returned invalid state")
		}
		result = GroupMutationResult{GroupID: groupID, Name: name, Revision: changed.AdministrationRevision, AuditID: changed.AuditID}
		return nil
	})
	if err != nil {
		return GroupMutationResult{}, fmt.Errorf("rename group transaction: %w", err)
	}
	return result, nil
}

func ChangeGroupMembership(ctx context.Context, beginner accountTransactionBeginner, clock func() time.Time, actor policy.AccessContext, targetUserID, groupID int64, grant bool, reason string, revision int64, requestID pgtype.UUID) (AccountMutationResult, error) {
	if targetUserID <= 0 || groupID <= 0 || revision <= 0 {
		return AccountMutationResult{}, fmt.Errorf("%w: membership", ErrAccountAdministrationInput)
	}
	if err := validateAccountMutationBoundary(ctx, beginner, clock, actor, reason, requestID); err != nil {
		return AccountMutationResult{}, err
	}
	observedAt, err := accountMutationTime(clock)
	if err != nil {
		return AccountMutationResult{}, err
	}
	mutationContext, cancel := context.WithTimeout(ctx, accountMutationTimeout)
	defer cancel()
	result := AccountMutationResult{}
	err = store.WithinTxOptions(mutationContext, beginner, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(queries *db.Queries) error {
		if err := configureAccountTransaction(mutationContext, queries); err != nil {
			return err
		}
		lockedActor, lockedTarget, err := lockAdministrationUsers(mutationContext, queries, actor.UserID, targetUserID)
		if err != nil {
			return err
		}
		if !validLockedAdministrator(lockedActor, actor, observedAt) {
			return ErrAccountAdministrationDenied
		}
		if !validLockedAdministrationUser(lockedTarget, targetUserID) {
			return fmt.Errorf("locked target account is malformed")
		}
		if effectiveSuspended(lockedTarget, observedAt) {
			return ErrAccountAdministrationConflict
		}
		if lockedTarget.AdministrationRevision != revision || revision == maximumAdministrationRevision {
			return ErrAccountAdministrationConflict
		}
		group, err := queries.LockAdministrationGroup(mutationContext, groupID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAccountAdministrationNotFound
		}
		if err != nil {
			return fmt.Errorf("lock membership group: %w", err)
		}
		if !validLockedGroup(group, groupID) {
			return fmt.Errorf("locked membership group is malformed")
		}
		member, err := queries.AdministrationMembershipExists(mutationContext, db.AdministrationMembershipExistsParams{UserID: targetUserID, GroupID: groupID})
		if err != nil {
			return fmt.Errorf("load administration membership: %w", err)
		}
		if member == grant {
			return ErrAccountAdministrationConflict
		}
		var changed db.GrantAdministrationMembershipAndAuditRow
		if grant {
			changed, err = queries.GrantAdministrationMembershipAndAudit(mutationContext, db.GrantAdministrationMembershipAndAuditParams{
				GroupID: groupID, TargetUserID: targetUserID, ActorUserID: actor.UserID,
				ObservedAt: administrationTime(observedAt), ExpectedRevision: revision, Reason: administrationReason(reason), RequestID: requestID,
			})
		} else {
			revoked, revokeErr := queries.RevokeAdministrationMembershipAndAudit(mutationContext, db.RevokeAdministrationMembershipAndAuditParams{
				GroupID: groupID, TargetUserID: targetUserID, ObservedAt: administrationTime(observedAt), ExpectedRevision: revision,
				ActorUserID: administrationActor(actor.UserID), Reason: administrationReason(reason), RequestID: requestID,
			})
			err = revokeErr
			changed = db.GrantAdministrationMembershipAndAuditRow{UserID: revoked.UserID, AdministrationRevision: revoked.AdministrationRevision, AuditID: revoked.AuditID}
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAccountAdministrationConflict
		}
		if err != nil {
			return mapAccountWriteError("change group membership", err)
		}
		if changed.UserID != targetUserID || changed.AdministrationRevision != revision+1 || changed.AuditID <= 0 {
			return fmt.Errorf("membership change returned invalid state")
		}
		role, _ := administrationRole(lockedTarget.Role)
		result = AccountMutationResult{UserID: targetUserID, Role: role, Revision: changed.AdministrationRevision, AuditID: changed.AuditID}
		return nil
	})
	if err != nil {
		return AccountMutationResult{}, fmt.Errorf("group membership transaction: %w", err)
	}
	return result, nil
}

func ChangeAccountRole(ctx context.Context, beginner accountTransactionBeginner, clock func() time.Time, actor policy.AccessContext, targetUserID int64, role, expectedRole policy.Role, reason string, revision int64, requestID pgtype.UUID) (AccountMutationResult, error) {
	if targetUserID <= 0 || targetUserID == actor.UserID || revision <= 0 || !validAdministrationRole(role) || !validAdministrationRole(expectedRole) || role == expectedRole {
		return AccountMutationResult{}, fmt.Errorf("%w: role change", ErrAccountAdministrationInput)
	}
	if err := validateAccountMutationBoundary(ctx, beginner, clock, actor, reason, requestID); err != nil {
		return AccountMutationResult{}, err
	}
	observedAt, err := accountMutationTime(clock)
	if err != nil {
		return AccountMutationResult{}, err
	}
	mutationContext, cancel := context.WithTimeout(ctx, accountMutationTimeout)
	defer cancel()
	result := AccountMutationResult{}
	err = store.WithinTxOptions(mutationContext, beginner, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(queries *db.Queries) error {
		if err := configureAccountTransaction(mutationContext, queries); err != nil {
			return err
		}
		locked, err := queries.LockGovernanceState(mutationContext)
		if err != nil {
			return fmt.Errorf("lock role governance state: %w", err)
		}
		if !locked {
			return fmt.Errorf("role governance singleton is missing")
		}
		lockedActor, lockedTarget, err := lockAdministrationUsers(mutationContext, queries, actor.UserID, targetUserID)
		if err != nil {
			return err
		}
		if !validLockedAdministrator(lockedActor, actor, observedAt) {
			return ErrAccountAdministrationDenied
		}
		if !validLockedAdministrationUser(lockedTarget, targetUserID) {
			return fmt.Errorf("locked target account is malformed")
		}
		persistedRole, _ := administrationRole(lockedTarget.Role)
		if effectiveSuspended(lockedTarget, observedAt) || persistedRole != expectedRole || lockedTarget.AdministrationRevision != revision || revision == maximumAdministrationRevision || persistedRole == role {
			return ErrAccountAdministrationConflict
		}
		if persistedRole == policy.RoleAdministrator && role != policy.RoleAdministrator {
			administrators, err := queries.CountActiveAdministrators(mutationContext, administrationTime(observedAt))
			if err != nil {
				return fmt.Errorf("count active administrators for role change: %w", err)
			}
			if administrators <= 1 {
				return ErrAccountAdministratorContinuity
			}
		}
		changed, err := queries.ChangeAdministrationRoleAndAudit(mutationContext, db.ChangeAdministrationRoleAndAuditParams{
			Role: administrationRoleName(role), ObservedAt: administrationTime(observedAt), TargetUserID: targetUserID,
			ExpectedRole: administrationRoleName(expectedRole), ExpectedRevision: revision,
			ActorUserID: administrationActor(actor.UserID), Reason: administrationReason(reason), RequestID: requestID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAccountAdministrationConflict
		}
		if err != nil {
			return mapAccountWriteError("change account role", err)
		}
		if changed.UserID != targetUserID || changed.Role != administrationRoleName(role) || changed.AdministrationRevision != revision+1 || changed.AuditID <= 0 || changed.RevokedSessions < 0 {
			return fmt.Errorf("role change returned invalid state")
		}
		result = AccountMutationResult{UserID: targetUserID, Role: role, Revision: changed.AdministrationRevision, AuditID: changed.AuditID, RevokedSessions: changed.RevokedSessions}
		return nil
	})
	if err != nil {
		return AccountMutationResult{}, fmt.Errorf("account role transaction: %w", err)
	}
	return result, nil
}

func validateAccountReadBoundary(ctx context.Context, querier accountAdministrationQuerier, actor policy.AccessContext, observedAt time.Time, afterID int64) error {
	if ctx == nil || querier == nil {
		return fmt.Errorf("account administration loader is incomplete")
	}
	if !policy.CanAdminister(actor) {
		return ErrAccountAdministrationDenied
	}
	if observedAt.IsZero() || afterID < 0 {
		return fmt.Errorf("%w: read boundary", ErrAccountAdministrationInput)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("load account administration: %w", err)
	}
	return nil
}

func validateAccountMutationBoundary(ctx context.Context, beginner accountTransactionBeginner, clock func() time.Time, actor policy.AccessContext, reason string, requestID pgtype.UUID) error {
	if ctx == nil || beginner == nil || clock == nil {
		return fmt.Errorf("account administration mutation boundary is incomplete")
	}
	if !policy.CanAdminister(actor) {
		return ErrAccountAdministrationDenied
	}
	if !validAdministrationReason(reason) || !requestID.Valid || requestID.Bytes == ([16]byte{}) {
		return ErrAccountAdministrationInput
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("administer account: %w", err)
	}
	return nil
}

func accountMutationTime(clock func() time.Time) (time.Time, error) {
	observedAt := clock()
	if observedAt.IsZero() {
		return time.Time{}, fmt.Errorf("account administration clock returned a zero time")
	}
	return observedAt.UTC().Truncate(time.Microsecond), nil
}

func configureAccountTransaction(ctx context.Context, queries *db.Queries) error {
	configured, err := queries.ConfigureAdministrationTransaction(ctx)
	if err != nil {
		return fmt.Errorf("configure account administration transaction: %w", err)
	}
	if configured.SetConfig != "2s" || configured.SetConfig_2 != "250ms" {
		return fmt.Errorf("account administration transaction configuration is invalid")
	}
	return nil
}

func lockAccountActor(ctx context.Context, queries *db.Queries, actor policy.AccessContext, observedAt time.Time) (db.LockAdministrationUserRow, error) {
	locked, err := queries.LockAdministrationUser(ctx, actor.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.LockAdministrationUserRow{}, ErrAccountAdministrationDenied
	}
	if err != nil {
		return db.LockAdministrationUserRow{}, fmt.Errorf("lock account administrator: %w", err)
	}
	if !validLockedAdministrator(locked, actor, observedAt) {
		return db.LockAdministrationUserRow{}, ErrAccountAdministrationDenied
	}
	return locked, nil
}

func lockAdministrationUsers(ctx context.Context, queries *db.Queries, actorUserID, targetUserID int64) (db.LockAdministrationUserRow, db.LockAdministrationUserRow, error) {
	firstID, secondID := actorUserID, targetUserID
	if secondID < firstID {
		firstID, secondID = secondID, firstID
	}
	first, err := queries.LockAdministrationUser(ctx, firstID)
	if errors.Is(err, pgx.ErrNoRows) {
		if firstID == actorUserID {
			return db.LockAdministrationUserRow{}, db.LockAdministrationUserRow{}, ErrAccountAdministrationDenied
		}
		return db.LockAdministrationUserRow{}, db.LockAdministrationUserRow{}, ErrAccountAdministrationNotFound
	}
	if err != nil {
		return db.LockAdministrationUserRow{}, db.LockAdministrationUserRow{}, fmt.Errorf("lock first administration user: %w", err)
	}
	if secondID == firstID {
		return db.LockAdministrationUserRow{}, db.LockAdministrationUserRow{}, ErrAccountAdministrationDenied
	}
	second, err := queries.LockAdministrationUser(ctx, secondID)
	if errors.Is(err, pgx.ErrNoRows) {
		if secondID == actorUserID {
			return db.LockAdministrationUserRow{}, db.LockAdministrationUserRow{}, ErrAccountAdministrationDenied
		}
		return db.LockAdministrationUserRow{}, db.LockAdministrationUserRow{}, ErrAccountAdministrationNotFound
	}
	if err != nil {
		return db.LockAdministrationUserRow{}, db.LockAdministrationUserRow{}, fmt.Errorf("lock second administration user: %w", err)
	}
	if first.ID == actorUserID {
		return first, second, nil
	}
	return second, first, nil
}

func validLockedAdministrator(row db.LockAdministrationUserRow, actor policy.AccessContext, observedAt time.Time) bool {
	if !validLockedAdministrationUser(row, actor.UserID) || effectiveSuspended(row, observedAt) || row.Role != "administrator" {
		return false
	}
	return actor.Role == policy.RoleAdministrator
}

func validLockedAdministrationUser(row db.LockAdministrationUserRow, expectedID int64) bool {
	if row.ID != expectedID || row.ID <= 0 || !validAdministrationDisplayName(row.DisplayName) || row.AdministrationRevision <= 0 ||
		!finiteAdministrationTime(row.CreatedAt) || !finiteAdministrationTime(row.UpdatedAt) || row.UpdatedAt.Time.Before(row.CreatedAt.Time) {
		return false
	}
	if _, valid := administrationRole(row.Role); !valid {
		return false
	}
	if row.MutedUntil.Valid && (!finiteAdministrationTime(row.MutedUntil) || !row.MutedUntil.Time.After(row.CreatedAt.Time)) {
		return false
	}
	if !row.SuspendedAt.Valid {
		return !row.SuspendedUntil.Valid
	}
	return finiteAdministrationTime(row.SuspendedAt) && !row.SuspendedAt.Time.Before(row.CreatedAt.Time) &&
		(!row.SuspendedUntil.Valid || finiteAdministrationTime(row.SuspendedUntil) && row.SuspendedUntil.Time.After(row.SuspendedAt.Time))
}

func effectiveSuspended(row db.LockAdministrationUserRow, observedAt time.Time) bool {
	return row.SuspendedAt.Valid && !row.SuspendedAt.Time.After(observedAt) && (!row.SuspendedUntil.Valid || row.SuspendedUntil.Time.After(observedAt))
}

func validLockedGroup(group db.ForumGroup, expectedID int64) bool {
	return group.ID == expectedID && group.ID > 0 && validAdministrationGroupName(group.Name) && group.CreatedBy > 0 &&
		finiteAdministrationTime(group.CreatedAt) && finiteAdministrationTime(group.UpdatedAt) && !group.UpdatedAt.Time.Before(group.CreatedAt.Time) &&
		group.AdministrationRevision > 0
}

func accountFromRow(row db.ListAccountsForAdministrationRow) (AccountSummary, bool) {
	role, validRole := administrationRole(row.Role)
	account := AccountSummary{ID: row.ID, DisplayName: row.DisplayName, Role: role, Suspended: row.Suspended, Revision: row.AdministrationRevision}
	if finiteAdministrationTime(row.CreatedAt) {
		account.CreatedAt = row.CreatedAt.Time.UTC().Truncate(time.Microsecond)
	}
	if finiteAdministrationTime(row.UpdatedAt) {
		account.UpdatedAt = row.UpdatedAt.Time.UTC().Truncate(time.Microsecond)
	}
	valid := row.ID > 0 && validAdministrationDisplayName(row.DisplayName) && validRole && row.AdministrationRevision > 0 &&
		finiteAdministrationTime(row.CreatedAt) && finiteAdministrationTime(row.UpdatedAt) && !account.UpdatedAt.Before(account.CreatedAt)
	return account, valid
}

func loadedAccountFromRow(row db.LoadAccountForAdministrationRow) (AccountSummary, bool) {
	return accountFromRow(db.ListAccountsForAdministrationRow{
		AccountPresent: row.AccountPresent, ID: row.ID, DisplayName: row.DisplayName, Role: row.Role,
		Suspended: row.Suspended, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, AdministrationRevision: row.AdministrationRevision,
	})
}

func groupFromRow(row db.ListGroupsForAdministrationRow) (GroupSummary, bool) {
	group := GroupSummary{ID: row.ID, Name: row.Name, Revision: row.AdministrationRevision}
	if finiteAdministrationTime(row.CreatedAt) {
		group.CreatedAt = row.CreatedAt.Time.UTC().Truncate(time.Microsecond)
	}
	if finiteAdministrationTime(row.UpdatedAt) {
		group.UpdatedAt = row.UpdatedAt.Time.UTC().Truncate(time.Microsecond)
	}
	valid := row.ID > 0 && validAdministrationGroupName(row.Name) && row.AdministrationRevision > 0 &&
		finiteAdministrationTime(row.CreatedAt) && finiteAdministrationTime(row.UpdatedAt) && !group.UpdatedAt.Before(group.CreatedAt)
	return group, valid
}

func emptyAccountRow(row db.ListAccountsForAdministrationRow) bool {
	return row.ID == 0 && row.DisplayName == "" && row.Role == "" && !row.Suspended && !row.CreatedAt.Valid && !row.UpdatedAt.Valid && row.AdministrationRevision == 0
}

func emptyLoadedAccountRow(row db.LoadAccountForAdministrationRow) bool {
	return row.ID == 0 && row.DisplayName == "" && row.Role == "" && !row.Suspended && !row.CreatedAt.Valid && !row.UpdatedAt.Valid && row.AdministrationRevision == 0
}

func emptyGroupRow(row db.ListGroupsForAdministrationRow) bool {
	return row.ID == 0 && row.Name == "" && !row.CreatedAt.Valid && !row.UpdatedAt.Valid && row.AdministrationRevision == 0
}

func malformedAdministrationRows(kind string) error {
	return fmt.Errorf("%w: malformed %s projection", ErrAccountAdministrationUnavailable, kind)
}

func validAdministrationDisplayName(value string) bool {
	return utf8.ValidString(value) && value != "" && utf8.RuneCountInString(value) <= 80 && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validAdministrationGroupName(value string) bool {
	return utf8.ValidString(value) && value != "" && utf8.RuneCountInString(value) <= 80 && strings.TrimSpace(value) == value &&
		norm.NFC.IsNormalString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validAdministrationReason(value string) bool {
	return len(value) >= 1 && len(value) <= 2_000 && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		norm.NFC.IsNormalString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validAdministrationRole(role policy.Role) bool {
	return role == policy.RoleMember || role == policy.RoleModerator || role == policy.RoleAdministrator
}

func administrationRole(value string) (policy.Role, bool) {
	switch value {
	case "member":
		return policy.RoleMember, true
	case "moderator":
		return policy.RoleModerator, true
	case "administrator":
		return policy.RoleAdministrator, true
	default:
		return 0, false
	}
}

func administrationRoleName(role policy.Role) string {
	switch role {
	case policy.RoleMember:
		return "member"
	case policy.RoleModerator:
		return "moderator"
	case policy.RoleAdministrator:
		return "administrator"
	default:
		return ""
	}
}

func finiteAdministrationTime(value pgtype.Timestamptz) bool {
	return value.Valid && value.InfinityModifier == pgtype.Finite && !value.Time.IsZero()
}

func administrationTime(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC().Truncate(time.Microsecond), Valid: true}
}

func administrationActor(userID int64) pgtype.Int8 {
	return pgtype.Int8{Int64: userID, Valid: true}
}

func administrationReason(reason string) pgtype.Text {
	return pgtype.Text{String: reason, Valid: true}
}

func mapAccountWriteError(operation string, err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && (postgresError.Code == "23505" || postgresError.Code == "23514") {
		return fmt.Errorf("%w: %s", ErrAccountAdministrationConflict, operation)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
