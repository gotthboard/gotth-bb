package administration

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrAdministrationDenied      = errors.New("administration denied")
	ErrAdministrationInput       = errors.New("invalid administration input")
	ErrAdministrationNotFound    = errors.New("administration target not found")
	ErrAdministrationConflict    = errors.New("administration conflict")
	ErrAdministrationUnavailable = errors.New("administration unavailable")
)

type Dashboard struct {
	ObservedAt                                  time.Time
	Users, Members, Moderators, Administrators  int64
	ActiveUsers, SuspendedUsers                 int64
	Topics, Posts, OpenReports, InReviewReports int64
}

type AreaSummary struct {
	ID, Revision, GroupCount int64
	Slug, Name, Description  string
	DisplayOrder             int32
	Visibility               policy.Visibility
	PostingMode              policy.PostingMode
}

type AreaPage struct {
	Areas     []AreaSummary
	NextAfter int64
}

type AreaGroup struct {
	ID       int64
	Name     string
	Assigned bool
}

type AreaDetail struct {
	Area      AreaSummary
	Groups    []AreaGroup
	NextAfter int64
}

type AreaCoreInput struct {
	Slug, Name, Description, Reason string
	DisplayOrder                    int32
	Visibility                      policy.Visibility
	PostingMode                     policy.PostingMode
	InitialGroupID, Revision        int64
}

type AreaCompletionResult struct {
	AreaID, Revision, AuditID int64
	Slug                      string
}

type completionReadQuerier interface {
	ListAreasForAdministrationPage(context.Context, db.ListAreasForAdministrationPageParams) ([]db.ListAreasForAdministrationPageRow, error)
	LoadAreaForAdministrationPage(context.Context, db.LoadAreaForAdministrationPageParams) (db.LoadAreaForAdministrationPageRow, error)
	ListAreaGroupsForAdministrationPage(context.Context, db.ListAreaGroupsForAdministrationPageParams) ([]db.ListAreaGroupsForAdministrationPageRow, error)
}

type dashboardBeginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

func LoadDashboard(ctx context.Context, beginner dashboardBeginner, actor policy.AccessContext) (Dashboard, error) {
	if ctx == nil || beginner == nil {
		return Dashboard{}, fmt.Errorf("dashboard boundary is incomplete")
	}
	if !policy.CanAdminister(actor) {
		return Dashboard{}, ErrAdministrationDenied
	}
	var result Dashboard
	err := store.WithinTxOptions(ctx, beginner, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(queries *db.Queries) error {
		if err := configureAccountTransaction(ctx, queries); err != nil {
			return err
		}
		row, err := queries.LoadAdministrationDashboard(ctx, actor.UserID)
		if err != nil {
			return fmt.Errorf("load dashboard: %w", err)
		}
		if !row.ActorPresent {
			return ErrAdministrationDenied
		}
		if !finiteAdministrationTime(row.AtTime) || row.UsersTotal < 0 || row.Members < 0 || row.Moderators < 0 || row.Administrators < 1 ||
			row.SuspendedUsers < 0 || row.Topics < 0 || row.Posts < 0 || row.OpenReports < 0 || row.InReviewReports < 0 ||
			row.Members+row.Moderators+row.Administrators != row.UsersTotal || row.SuspendedUsers > row.UsersTotal {
			return fmt.Errorf("%w: malformed dashboard", ErrAdministrationUnavailable)
		}
		result = Dashboard{ObservedAt: row.AtTime.Time.UTC().Truncate(time.Microsecond), Users: row.UsersTotal, Members: row.Members,
			Moderators: row.Moderators, Administrators: row.Administrators, ActiveUsers: row.UsersTotal - row.SuspendedUsers,
			SuspendedUsers: row.SuspendedUsers, Topics: row.Topics, Posts: row.Posts, OpenReports: row.OpenReports, InReviewReports: row.InReviewReports}
		return nil
	})
	if err != nil {
		return Dashboard{}, fmt.Errorf("dashboard transaction: %w", err)
	}
	return result, nil
}

func ListAreaPage(ctx context.Context, querier completionReadQuerier, actor policy.AccessContext, observedAt time.Time, afterAreaID int64) (AreaPage, error) {
	if ctx == nil || querier == nil || !policy.CanAdminister(actor) {
		return AreaPage{}, ErrAdministrationDenied
	}
	if observedAt.IsZero() || afterAreaID < 0 {
		return AreaPage{}, ErrAdministrationInput
	}
	rows, err := querier.ListAreasForAdministrationPage(ctx, db.ListAreasForAdministrationPageParams{ActorUserID: actor.UserID, ObservedAt: administrationTime(observedAt), AfterAreaID: afterAreaID, PageLimit: administrationPageQueryLimit})
	if err != nil {
		return AreaPage{}, fmt.Errorf("%w: list areas: %w", ErrAdministrationUnavailable, err)
	}
	if len(rows) == 0 {
		return AreaPage{}, ErrAdministrationDenied
	}
	page := AreaPage{Areas: make([]AreaSummary, 0, min(len(rows), administrationPageSize))}
	previous := afterAreaID
	for index, row := range rows {
		if !row.AreaPresent {
			if len(rows) != 1 || row.ID != 0 || row.Slug != "" || row.Name != "" || row.AdministrationRevision != 0 || row.GroupCount != 0 {
				return AreaPage{}, fmt.Errorf("%w: malformed area page", ErrAdministrationUnavailable)
			}
			return page, nil
		}
		area := areaSummary(row.ID, row.Slug, row.Name, row.Description, row.DisplayOrder, row.Visibility, row.PostingMode, row.AdministrationRevision, row.GroupCount)
		if !validAreaSummary(area) || area.ID <= previous {
			return AreaPage{}, fmt.Errorf("%w: malformed area page", ErrAdministrationUnavailable)
		}
		previous = area.ID
		if index == administrationPageSize {
			page.NextAfter = page.Areas[len(page.Areas)-1].ID
			continue
		}
		page.Areas = append(page.Areas, area)
	}
	return page, nil
}

func LoadAreaDetail(ctx context.Context, querier completionReadQuerier, actor policy.AccessContext, observedAt time.Time, areaID, afterGroupID int64) (AreaDetail, error) {
	if ctx == nil || querier == nil || !policy.CanAdminister(actor) {
		return AreaDetail{}, ErrAdministrationDenied
	}
	if observedAt.IsZero() || areaID <= 0 || afterGroupID < 0 {
		return AreaDetail{}, ErrAdministrationInput
	}
	row, err := querier.LoadAreaForAdministrationPage(ctx, db.LoadAreaForAdministrationPageParams{ActorUserID: actor.UserID, ObservedAt: administrationTime(observedAt), AreaID: areaID})
	if err != nil {
		return AreaDetail{}, fmt.Errorf("%w: load area: %w", ErrAdministrationUnavailable, err)
	}
	if !row.ActorPresent {
		return AreaDetail{}, ErrAdministrationDenied
	}
	if !row.AreaPresent {
		return AreaDetail{}, ErrAdministrationNotFound
	}
	area := areaSummary(row.ID, row.Slug, row.Name, row.Description, row.DisplayOrder, row.Visibility, row.PostingMode, row.AdministrationRevision, 0)
	if !validAreaSummary(area) || area.ID != areaID {
		return AreaDetail{}, fmt.Errorf("%w: malformed area", ErrAdministrationUnavailable)
	}
	groupRows, err := querier.ListAreaGroupsForAdministrationPage(ctx, db.ListAreaGroupsForAdministrationPageParams{ActorUserID: actor.UserID, ObservedAt: administrationTime(observedAt), AreaID: areaID, AfterGroupID: afterGroupID, PageLimit: administrationPageQueryLimit})
	if err != nil {
		return AreaDetail{}, fmt.Errorf("%w: list area groups: %w", ErrAdministrationUnavailable, err)
	}
	detail := AreaDetail{Area: area, Groups: make([]AreaGroup, 0, min(len(groupRows), administrationPageSize))}
	previous := afterGroupID
	for index, group := range groupRows {
		if !group.ActorPresent {
			return AreaDetail{}, ErrAdministrationDenied
		}
		if !group.AreaPresent {
			return AreaDetail{}, ErrAdministrationNotFound
		}
		if !group.GroupPresent {
			if len(groupRows) != 1 || group.GroupID != 0 || group.GroupName != "" || group.Assigned {
				return AreaDetail{}, fmt.Errorf("%w: malformed area groups", ErrAdministrationUnavailable)
			}
			return detail, nil
		}
		if group.GroupID <= previous || !validAdministrationGroupName(group.GroupName) {
			return AreaDetail{}, fmt.Errorf("%w: malformed area groups", ErrAdministrationUnavailable)
		}
		previous = group.GroupID
		if index == administrationPageSize {
			detail.NextAfter = detail.Groups[len(detail.Groups)-1].ID
			continue
		}
		detail.Groups = append(detail.Groups, AreaGroup{ID: group.GroupID, Name: group.GroupName, Assigned: group.Assigned})
	}
	return detail, nil
}

func CreateAreaCompletion(ctx context.Context, beginner accountTransactionBeginner, clock func() time.Time, actor policy.AccessContext, input AreaCoreInput, requestID pgtype.UUID) (AreaCompletionResult, error) {
	if input.Revision != 0 || !validAreaCoreInput(input, true) {
		return AreaCompletionResult{}, ErrAdministrationInput
	}
	if err := validateAccountMutationBoundary(ctx, beginner, clock, actor, input.Reason, requestID); err != nil {
		return AreaCompletionResult{}, mapCompletionBoundaryError(err)
	}
	observedAt, err := accountMutationTime(clock)
	if err != nil {
		return AreaCompletionResult{}, err
	}
	mutationContext, cancel := context.WithTimeout(ctx, accountMutationTimeout)
	defer cancel()
	var result AreaCompletionResult
	err = store.WithinTxOptions(mutationContext, beginner, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(queries *db.Queries) error {
		if err := configureAccountTransaction(mutationContext, queries); err != nil {
			return err
		}
		if _, err := lockAccountActor(mutationContext, queries, actor, observedAt); err != nil {
			return err
		}
		groups := []int64(nil)
		if input.InitialGroupID > 0 {
			group, err := queries.LockAdministrationGroup(mutationContext, input.InitialGroupID)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrAdministrationNotFound
			}
			if err != nil || !validLockedGroup(group, input.InitialGroupID) {
				return fmt.Errorf("lock initial area group: %w", err)
			}
			groups = []int64{input.InitialGroupID}
		}
		created, err := queries.CreateAreaForAdministration(mutationContext, db.CreateAreaForAdministrationParams{Slug: input.Slug, Name: input.Name, Description: input.Description, DisplayOrder: input.DisplayOrder, Visibility: string(input.Visibility), PostingMode: string(input.PostingMode), ActorUserID: actor.UserID, AtTime: administrationTime(observedAt)})
		if err != nil {
			return mapAreaWriteError("create area", err)
		}
		if err := replaceAreaGroups(mutationContext, queries, created.ID, actor.UserID, groups, administrationTime(observedAt)); err != nil {
			return err
		}
		state := auditedAreaState{Slug: input.Slug, Name: input.Name, Description: input.Description, DisplayOrder: input.DisplayOrder, Visibility: input.Visibility, PostingMode: input.PostingMode, GroupIDs: groups}
		auditID, err := insertAreaAudit(mutationContext, queries, actor.UserID, created.ID, "create_area", input.Reason, auditedAreaState{}, state, requestID, administrationTime(observedAt))
		if err != nil {
			return err
		}
		if created.ID <= 0 || created.AdministrationRevision != 1 || auditID <= 0 {
			return fmt.Errorf("create area returned invalid state")
		}
		result = AreaCompletionResult{AreaID: created.ID, Slug: created.Slug, Revision: 1, AuditID: auditID}
		return nil
	})
	if err != nil {
		return AreaCompletionResult{}, fmt.Errorf("create area completion transaction: %w", mapCompletionBoundaryError(err))
	}
	return result, nil
}

func UpdateAreaCompletion(ctx context.Context, beginner accountTransactionBeginner, clock func() time.Time, actor policy.AccessContext, areaID int64, input AreaCoreInput, requestID pgtype.UUID) (AreaCompletionResult, error) {
	if areaID <= 0 || input.Revision <= 0 || !validAreaCoreInput(input, false) {
		return AreaCompletionResult{}, ErrAdministrationInput
	}
	if err := validateAccountMutationBoundary(ctx, beginner, clock, actor, input.Reason, requestID); err != nil {
		return AreaCompletionResult{}, mapCompletionBoundaryError(err)
	}
	observedAt, err := accountMutationTime(clock)
	if err != nil {
		return AreaCompletionResult{}, err
	}
	mutationContext, cancel := context.WithTimeout(ctx, accountMutationTimeout)
	defer cancel()
	var result AreaCompletionResult
	err = store.WithinTxOptions(mutationContext, beginner, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(queries *db.Queries) error {
		if err := configureAccountTransaction(mutationContext, queries); err != nil {
			return err
		}
		if _, err := lockAccountActor(mutationContext, queries, actor, observedAt); err != nil {
			return err
		}
		current, err := queries.LockAreaForAdministration(mutationContext, areaID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAdministrationNotFound
		}
		if err != nil {
			return fmt.Errorf("lock area: %w", err)
		}
		if current.AdministrationRevision != input.Revision || input.Revision == maximumAdministrationRevision || current.Slug != input.Slug {
			return ErrAdministrationConflict
		}
		if current.Visibility == string(policy.VisibilityGroups) && input.Visibility == policy.VisibilityGroups && input.InitialGroupID != 0 {
			return ErrAdministrationInput
		}
		groups := slices.Clone(current.GroupIds)
		if input.Visibility == policy.VisibilityGroups && current.Visibility != string(policy.VisibilityGroups) {
			group, err := queries.LockAdministrationGroup(mutationContext, input.InitialGroupID)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrAdministrationNotFound
			}
			if err != nil || !validLockedGroup(group, input.InitialGroupID) {
				return fmt.Errorf("lock initial area group: %w", err)
			}
			groups = []int64{input.InitialGroupID}
		} else if input.Visibility != policy.VisibilityGroups {
			groups = nil
		}
		previous := auditedAreaState{Slug: current.Slug, Name: current.Name, Description: current.Description, DisplayOrder: current.DisplayOrder, Visibility: policy.Visibility(current.Visibility), PostingMode: policy.PostingMode(current.PostingMode), GroupIDs: slices.Clone(current.GroupIds)}
		resulting := auditedAreaState{Slug: input.Slug, Name: input.Name, Description: input.Description, DisplayOrder: input.DisplayOrder, Visibility: input.Visibility, PostingMode: input.PostingMode, GroupIDs: groups}
		if equalAreaStates(previous, resulting) {
			return ErrAdministrationConflict
		}
		if !slices.Equal(current.GroupIds, groups) {
			if err := queries.DeleteAreaGroupsForAdministration(mutationContext, areaID); err != nil {
				return fmt.Errorf("replace area groups: %w", err)
			}
			if err := replaceAreaGroups(mutationContext, queries, areaID, actor.UserID, groups, administrationTime(observedAt)); err != nil {
				return err
			}
		}
		previousJSON, resultingJSON, err := boundedAreaAuditStates(previous, resulting)
		if err != nil {
			return err
		}
		changed, err := queries.UpdateAdministrationAreaAndAudit(mutationContext, db.UpdateAdministrationAreaAndAuditParams{Name: input.Name, Description: input.Description, DisplayOrder: input.DisplayOrder, Visibility: string(input.Visibility), PostingMode: string(input.PostingMode), ActorUserID: actor.UserID, ObservedAt: administrationTime(observedAt), AreaID: areaID, ExpectedRevision: input.Revision, Reason: administrationReason(input.Reason), PreviousState: previousJSON, ResultingState: resultingJSON, RequestID: requestID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAdministrationConflict
		}
		if err != nil {
			return err
		}
		if changed.AreaID != areaID || changed.Slug != input.Slug || changed.AdministrationRevision != input.Revision+1 || changed.AuditID <= 0 {
			return fmt.Errorf("update area returned invalid state")
		}
		result = AreaCompletionResult{AreaID: areaID, Slug: changed.Slug, Revision: changed.AdministrationRevision, AuditID: changed.AuditID}
		return nil
	})
	if err != nil {
		return AreaCompletionResult{}, fmt.Errorf("update area completion transaction: %w", mapCompletionBoundaryError(err))
	}
	return result, nil
}

func ChangeAreaGroup(ctx context.Context, beginner accountTransactionBeginner, clock func() time.Time, actor policy.AccessContext, areaID, groupID int64, grant bool, reason string, revision int64, requestID pgtype.UUID) (AreaCompletionResult, error) {
	if areaID <= 0 || groupID <= 0 || revision <= 0 {
		return AreaCompletionResult{}, ErrAdministrationInput
	}
	if err := validateAccountMutationBoundary(ctx, beginner, clock, actor, reason, requestID); err != nil {
		return AreaCompletionResult{}, mapCompletionBoundaryError(err)
	}
	observedAt, err := accountMutationTime(clock)
	if err != nil {
		return AreaCompletionResult{}, err
	}
	mutationContext, cancel := context.WithTimeout(ctx, accountMutationTimeout)
	defer cancel()
	var result AreaCompletionResult
	err = store.WithinTxOptions(mutationContext, beginner, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(queries *db.Queries) error {
		if err := configureAccountTransaction(mutationContext, queries); err != nil {
			return err
		}
		if _, err := lockAccountActor(mutationContext, queries, actor, observedAt); err != nil {
			return err
		}
		area, err := queries.LockAreaForAdministration(mutationContext, areaID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAdministrationNotFound
		}
		if err != nil {
			return err
		}
		if area.Visibility != string(policy.VisibilityGroups) || area.AdministrationRevision != revision || revision == maximumAdministrationRevision {
			return ErrAdministrationConflict
		}
		group, err := queries.LockAdministrationGroup(mutationContext, groupID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAdministrationNotFound
		}
		if err != nil || !validLockedGroup(group, groupID) {
			return fmt.Errorf("lock area group: %w", err)
		}
		assigned, err := queries.AdministrationAreaGroupExists(mutationContext, db.AdministrationAreaGroupExistsParams{AreaID: areaID, GroupID: groupID})
		if err != nil {
			return err
		}
		if assigned == grant {
			return ErrAdministrationConflict
		}
		var changed AreaCompletionResult
		if grant {
			row, err := queries.GrantAdministrationAreaGroupAndAudit(mutationContext, db.GrantAdministrationAreaGroupAndAuditParams{AreaID: areaID, GroupID: groupID, ActorUserID: actor.UserID, ObservedAt: administrationTime(observedAt), ExpectedRevision: revision, Reason: administrationReason(reason), RequestID: requestID})
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrAdministrationConflict
				}
				return err
			}
			changed = AreaCompletionResult{AreaID: row.AreaID, Revision: row.AdministrationRevision, AuditID: row.AuditID}
		} else {
			row, err := queries.RevokeAdministrationAreaGroupAndAudit(mutationContext, db.RevokeAdministrationAreaGroupAndAuditParams{AreaID: areaID, GroupID: groupID, ActorUserID: actor.UserID, ObservedAt: administrationTime(observedAt), ExpectedRevision: revision, Reason: administrationReason(reason), RequestID: requestID})
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrAdministrationConflict
				}
				return err
			}
			changed = AreaCompletionResult{AreaID: row.AreaID, Revision: row.AdministrationRevision, AuditID: row.AuditID}
		}
		if changed.AreaID != areaID || changed.Revision != revision+1 || changed.AuditID <= 0 {
			return fmt.Errorf("area group mutation returned invalid state")
		}
		changed.Slug = area.Slug
		result = changed
		return nil
	})
	if err != nil {
		return AreaCompletionResult{}, fmt.Errorf("area group transaction: %w", mapCompletionBoundaryError(err))
	}
	return result, nil
}

func validAreaCoreInput(input AreaCoreInput, create bool) bool {
	if !policy.ValidAreaSlug(input.Slug) || !validCanonicalText(input.Name, 120, false) || !validDescription(input.Description) || input.DisplayOrder < 0 || !validAdministrationReason(input.Reason) {
		return false
	}
	if input.Visibility != policy.VisibilityPublic && input.Visibility != policy.VisibilityAuthenticated && input.Visibility != policy.VisibilityGroups {
		return false
	}
	if input.PostingMode != policy.PostingNormal && input.PostingMode != policy.PostingReadOnly && input.PostingMode != policy.PostingArchived {
		return false
	}
	if input.Visibility == policy.VisibilityGroups {
		return input.InitialGroupID > 0 || !create
	}
	return input.InitialGroupID == 0
}

func areaSummary(id int64, slug, name, description string, order int32, visibility, posting string, revision, groupCount int64) AreaSummary {
	return AreaSummary{ID: id, Slug: slug, Name: name, Description: description, DisplayOrder: order, Visibility: policy.Visibility(visibility), PostingMode: policy.PostingMode(posting), Revision: revision, GroupCount: groupCount}
}

func validAreaSummary(area AreaSummary) bool {
	return area.ID > 0 && area.Revision > 0 && area.GroupCount >= 0 && policy.ValidAreaSlug(area.Slug) && validCanonicalText(area.Name, 120, false) && validDescription(area.Description) && area.DisplayOrder >= 0 &&
		(area.Visibility == policy.VisibilityPublic || area.Visibility == policy.VisibilityAuthenticated || area.Visibility == policy.VisibilityGroups) &&
		(area.PostingMode == policy.PostingNormal || area.PostingMode == policy.PostingReadOnly || area.PostingMode == policy.PostingArchived) &&
		(area.Visibility == policy.VisibilityGroups || area.GroupCount == 0)
}

func boundedAreaAuditStates(previous, resulting auditedAreaState) ([]byte, []byte, error) {
	type bounded struct {
		Slug, Name, DescriptionSHA256 string
		DisplayOrder                  int32
		Visibility                    policy.Visibility
		PostingMode                   policy.PostingMode
		GroupCount                    int
		GroupIDsSHA256                string
	}
	convert := func(state auditedAreaState) bounded {
		return bounded{Slug: state.Slug, Name: state.Name, DescriptionSHA256: digestBytes([]byte(state.Description)), DisplayOrder: state.DisplayOrder, Visibility: state.Visibility, PostingMode: state.PostingMode, GroupCount: len(state.GroupIDs), GroupIDsSHA256: digestIDs(state.GroupIDs)}
	}
	left, err := json.Marshal(convert(previous))
	if err != nil {
		return nil, nil, err
	}
	right, err := json.Marshal(convert(resulting))
	if err != nil {
		return nil, nil, err
	}
	return left, right, nil
}

func digestBytes(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
func digestIDs(ids []int64) string {
	hash := sha256.New()
	var encoded [8]byte
	for _, id := range ids {
		binary.BigEndian.PutUint64(encoded[:], uint64(id))
		_, _ = hash.Write(encoded[:])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func mapCompletionBoundaryError(err error) error {
	switch {
	case errors.Is(err, ErrAccountAdministrationDenied), errors.Is(err, ErrAreaAdministrationDenied):
		return ErrAdministrationDenied
	case errors.Is(err, ErrAccountAdministrationInput), errors.Is(err, ErrAreaAdministrationInput):
		return ErrAdministrationInput
	case errors.Is(err, ErrAccountAdministrationNotFound):
		return ErrAdministrationNotFound
	case errors.Is(err, ErrAccountAdministrationConflict), errors.Is(err, ErrAreaAdministrationConflict):
		return ErrAdministrationConflict
	default:
		return err
	}
}
