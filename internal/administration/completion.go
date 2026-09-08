package administration

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	Areas          []AreaSummary
	NextAfterOrder int32
	NextAfterID    int64
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

type boundedAreaAuditState struct {
	Slug                   string             `json:"slug"`
	Name                   string             `json:"name"`
	DescriptionSHA256      string             `json:"description_sha256"`
	DisplayOrder           int32              `json:"display_order"`
	Visibility             policy.Visibility  `json:"visibility"`
	PostingMode            policy.PostingMode `json:"posting_mode"`
	AdministrationRevision int64              `json:"administration_revision"`
	GroupCount             int64              `json:"group_count"`
	GroupIDsSHA256         string             `json:"group_ids_sha256"`
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
	dashboardContext, cancel := context.WithTimeout(ctx, accountMutationTimeout)
	defer cancel()
	var result Dashboard
	err := store.WithinTxOptions(dashboardContext, beginner, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(queries *db.Queries) error {
		if err := configureAccountTransaction(dashboardContext, queries); err != nil {
			return err
		}
		row, err := queries.LoadAdministrationDashboard(dashboardContext, actor.UserID)
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

func ListAreaPage(ctx context.Context, querier completionReadQuerier, actor policy.AccessContext, observedAt time.Time, afterOrder int32, afterAreaID int64) (AreaPage, error) {
	if ctx == nil || querier == nil || !policy.CanAdminister(actor) {
		return AreaPage{}, ErrAdministrationDenied
	}
	if observedAt.IsZero() || !finiteAdministrationTime(administrationTime(observedAt)) || afterOrder < 0 || afterAreaID < 0 || afterAreaID == 0 && afterOrder != 0 {
		return AreaPage{}, ErrAdministrationInput
	}
	rows, err := querier.ListAreasForAdministrationPage(ctx, db.ListAreasForAdministrationPageParams{ActorUserID: actor.UserID, ObservedAt: administrationTime(observedAt), AfterOrder: afterOrder, AfterAreaID: afterAreaID, PageLimit: administrationAreaPageQueryLimit})
	if err != nil {
		return AreaPage{}, fmt.Errorf("%w: list areas: %w", ErrAdministrationUnavailable, err)
	}
	if len(rows) == 0 {
		return AreaPage{}, ErrAdministrationDenied
	}
	if len(rows) > int(administrationAreaPageQueryLimit) {
		return AreaPage{}, fmt.Errorf("%w: oversized area page", ErrAdministrationUnavailable)
	}
	page := AreaPage{Areas: make([]AreaSummary, 0, min(len(rows), administrationAreaPageSize))}
	previousOrder, previousID := afterOrder, afterAreaID
	for index, row := range rows {
		if !row.AreaPresent {
			if len(rows) != 1 || row.ID != 0 || row.Slug != "" || row.Name != "" || row.Description != "" || row.DisplayOrder != 0 || row.Visibility != "" || row.PostingMode != "" || row.AdministrationRevision != 0 || row.GroupCount != 0 {
				return AreaPage{}, fmt.Errorf("%w: malformed area page", ErrAdministrationUnavailable)
			}
			return page, nil
		}
		area := areaSummary(row.ID, row.Slug, row.Name, row.Description, row.DisplayOrder, row.Visibility, row.PostingMode, row.AdministrationRevision, row.GroupCount)
		if !validAreaSummary(area) || area.DisplayOrder < previousOrder || area.DisplayOrder == previousOrder && area.ID <= previousID {
			return AreaPage{}, fmt.Errorf("%w: malformed area page", ErrAdministrationUnavailable)
		}
		previousOrder, previousID = area.DisplayOrder, area.ID
		if index == administrationAreaPageSize {
			last := page.Areas[len(page.Areas)-1]
			page.NextAfterOrder, page.NextAfterID = last.DisplayOrder, last.ID
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
	if observedAt.IsZero() || !finiteAdministrationTime(administrationTime(observedAt)) || areaID <= 0 || afterGroupID < 0 {
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
	if len(groupRows) > int(administrationPageQueryLimit) {
		return AreaDetail{}, fmt.Errorf("%w: oversized area group page", ErrAdministrationUnavailable)
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
			if err != nil {
				return fmt.Errorf("lock initial area group: %w", err)
			}
			if !validLockedGroup(group, input.InitialGroupID) {
				return fmt.Errorf("lock initial area group returned invalid state")
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
		state := newBoundedAreaAuditState(input.Slug, input.Name, input.Description, input.DisplayOrder, input.Visibility, input.PostingMode, 1, groups)
		auditID, err := insertBoundedAreaAudit(mutationContext, queries, actor.UserID, created.ID, "create_area", input.Reason, nil, state, requestID, administrationTime(observedAt))
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
		current, err := queries.LockAdministrationAreaCore(mutationContext, areaID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAdministrationNotFound
		}
		if err != nil {
			return fmt.Errorf("lock area: %w", err)
		}
		if input.Slug != "" || current.AdministrationRevision != input.Revision || input.Revision == maximumAdministrationRevision {
			return fmt.Errorf("%w: stale or exhausted area core", ErrAdministrationConflict)
		}
		input.Slug = current.Slug
		if current.Visibility == string(policy.VisibilityGroups) && input.Visibility == policy.VisibilityGroups && input.InitialGroupID != 0 {
			return ErrAdministrationInput
		}
		if current.Visibility != string(policy.VisibilityGroups) && input.Visibility == policy.VisibilityGroups && input.InitialGroupID <= 0 {
			return ErrAdministrationInput
		}
		previousGroupCount, previousGroupDigest, err := streamAreaGroupDigest(mutationContext, queries, areaID)
		if err != nil {
			return err
		}
		resultingGroupCount, resultingGroupDigest := previousGroupCount, previousGroupDigest
		groupsChanged := false
		if input.Visibility == policy.VisibilityGroups && current.Visibility != string(policy.VisibilityGroups) {
			group, err := queries.LockAdministrationGroup(mutationContext, input.InitialGroupID)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrAdministrationNotFound
			}
			if err != nil {
				return fmt.Errorf("lock initial area group: %w", err)
			}
			if !validLockedGroup(group, input.InitialGroupID) {
				return fmt.Errorf("lock initial area group returned invalid state")
			}
			resultingGroupCount = 1
			resultingGroupDigest = digestIDs([]int64{input.InitialGroupID})
			groupsChanged = true
		} else if input.Visibility != policy.VisibilityGroups {
			resultingGroupCount = 0
			resultingGroupDigest = digestIDs(nil)
			groupsChanged = previousGroupCount != 0
		}
		previous := boundedAreaAuditState{Slug: current.Slug, Name: current.Name, DescriptionSHA256: digestBytes([]byte(current.Description)), DisplayOrder: current.DisplayOrder, Visibility: policy.Visibility(current.Visibility), PostingMode: policy.PostingMode(current.PostingMode), AdministrationRevision: current.AdministrationRevision, GroupCount: previousGroupCount, GroupIDsSHA256: previousGroupDigest}
		resulting := boundedAreaAuditState{Slug: current.Slug, Name: input.Name, DescriptionSHA256: digestBytes([]byte(input.Description)), DisplayOrder: input.DisplayOrder, Visibility: input.Visibility, PostingMode: input.PostingMode, AdministrationRevision: input.Revision + 1, GroupCount: resultingGroupCount, GroupIDsSHA256: resultingGroupDigest}
		if equalBoundedAreaStatesIgnoringRevision(previous, resulting) {
			return fmt.Errorf("%w: unchanged area core", ErrAdministrationConflict)
		}
		if groupsChanged {
			if err := queries.DeleteAreaGroupsForAdministration(mutationContext, areaID); err != nil {
				return fmt.Errorf("replace area groups: %w", err)
			}
			groups := []int64(nil)
			if resultingGroupCount == 1 {
				groups = []int64{input.InitialGroupID}
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
			return fmt.Errorf("%w: conditional area update", ErrAdministrationConflict)
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
		area, err := queries.LockAdministrationAreaCore(mutationContext, areaID)
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
		if err != nil {
			return fmt.Errorf("lock area group: %w", err)
		}
		if !validLockedGroup(group, groupID) {
			return fmt.Errorf("lock area group returned invalid state")
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
	if (create && !policy.ValidAreaSlug(input.Slug)) || (!create && input.Slug != "") || !validCanonicalText(input.Name, 120, false) || !validDescription(input.Description) || input.DisplayOrder < 0 || !validAdministrationReason(input.Reason) {
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

func boundedAreaAuditStates(previous, resulting boundedAreaAuditState) ([]byte, []byte, error) {
	left, err := json.Marshal(previous)
	if err != nil {
		return nil, nil, err
	}
	right, err := json.Marshal(resulting)
	if err != nil {
		return nil, nil, err
	}
	return left, right, nil
}

func newBoundedAreaAuditState(slug, name, description string, order int32, visibility policy.Visibility, posting policy.PostingMode, revision int64, groupIDs []int64) boundedAreaAuditState {
	return boundedAreaAuditState{Slug: slug, Name: name, DescriptionSHA256: digestBytes([]byte(description)), DisplayOrder: order, Visibility: visibility, PostingMode: posting, AdministrationRevision: revision, GroupCount: int64(len(groupIDs)), GroupIDsSHA256: digestIDs(groupIDs)}
}

func equalBoundedAreaStatesIgnoringRevision(left, right boundedAreaAuditState) bool {
	left.AdministrationRevision = 0
	right.AdministrationRevision = 0
	return left == right
}

func streamAreaGroupDigest(ctx context.Context, queries *db.Queries, areaID int64) (int64, string, error) {
	rows, err := queries.StreamAdministrationAreaGroupIDs(ctx, areaID)
	if err != nil {
		return 0, "", fmt.Errorf("stream area group identifiers: %w", err)
	}
	defer rows.Close()
	hash := sha256.New()
	var encoded [8]byte
	var count, previous int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, "", fmt.Errorf("scan area group identifier: %w", err)
		}
		if id <= previous {
			return 0, "", fmt.Errorf("stream area group identifiers returned invalid order")
		}
		previous = id
		count++
		binary.BigEndian.PutUint64(encoded[:], uint64(id))
		_, _ = hash.Write(encoded[:])
	}
	if err := rows.Err(); err != nil {
		return 0, "", fmt.Errorf("stream area group identifiers: %w", err)
	}
	return count, hex.EncodeToString(hash.Sum(nil)), nil
}

func insertBoundedAreaAudit(ctx context.Context, queries *db.Queries, actorID, areaID int64, action, reason string, previous *boundedAreaAuditState, resulting boundedAreaAuditState, requestID pgtype.UUID, at pgtype.Timestamptz) (int64, error) {
	previousJSON := []byte("{}")
	var err error
	if previous != nil {
		previousJSON, err = json.Marshal(*previous)
		if err != nil {
			return 0, fmt.Errorf("encode previous bounded area state: %w", err)
		}
	}
	resultingJSON, err := json.Marshal(resulting)
	if err != nil {
		return 0, fmt.Errorf("encode resulting bounded area state: %w", err)
	}
	auditID, err := queries.CreateAreaAdministrationAudit(ctx, db.CreateAreaAdministrationAuditParams{ActorUserID: pgtype.Int8{Int64: actorID, Valid: true}, AreaID: pgtype.Int8{Int64: areaID, Valid: true}, ActionType: action, Reason: administrationReason(reason), PreviousState: previousJSON, ResultingState: resultingJSON, RequestID: requestID, AtTime: at})
	if err != nil {
		return 0, fmt.Errorf("write bounded area audit: %w", err)
	}
	if auditID <= 0 {
		return 0, fmt.Errorf("bounded area audit returned invalid identifier")
	}
	return auditID, nil
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
		return fmt.Errorf("%w: %v", ErrAdministrationDenied, err)
	case errors.Is(err, ErrAccountAdministrationInput), errors.Is(err, ErrAreaAdministrationInput):
		return fmt.Errorf("%w: %v", ErrAdministrationInput, err)
	case errors.Is(err, ErrAccountAdministrationNotFound):
		return fmt.Errorf("%w: %v", ErrAdministrationNotFound, err)
	case errors.Is(err, ErrAccountAdministrationConflict), errors.Is(err, ErrAreaAdministrationConflict):
		return fmt.Errorf("%w: %v", ErrAdministrationConflict, err)
	default:
		return err
	}
}
