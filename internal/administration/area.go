package administration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
)

var (
	ErrAreaAdministrationInput    = errors.New("invalid area administration input")
	ErrAreaAdministrationDenied   = errors.New("area administration denied")
	ErrAreaAdministrationConflict = errors.New("area administration conflict")
)

type AreaInput struct {
	Slug         string
	Name         string
	Description  string
	DisplayOrder int32
	Visibility   policy.Visibility
	PostingMode  policy.PostingMode
	GroupIDs     []int64
	Reason       string
	Revision     time.Time
}

type ManagedArea struct {
	ID           int64
	Slug         string
	Name         string
	Description  string
	DisplayOrder int32
	Visibility   policy.Visibility
	PostingMode  policy.PostingMode
	GroupIDs     []int64
	UpdatedAt    time.Time
}

type ForumGroup struct {
	ID   int64
	Name string
}

type AreaManagementPage struct {
	Areas  []ManagedArea
	Groups []ForumGroup
}

type AreaMutationResult struct {
	AreaID  int64
	Slug    string
	AuditID int64
}

type areaAdministrationQuerier interface {
	ListAreasForAdministration(context.Context) ([]db.ListAreasForAdministrationRow, error)
	ListForumGroupsForAreaAdministration(context.Context) ([]db.ListForumGroupsForAreaAdministrationRow, error)
}

type transactionBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type auditedAreaState struct {
	Slug         string             `json:"slug"`
	Name         string             `json:"name"`
	Description  string             `json:"description"`
	DisplayOrder int32              `json:"display_order"`
	Visibility   policy.Visibility  `json:"visibility"`
	PostingMode  policy.PostingMode `json:"posting_mode"`
	GroupIDs     []int64            `json:"group_ids"`
}

// LoadAreaManagement returns the complete administrator projection. The
// caller must supply current server-owned administrator authority; there is no
// visibility filtering because restricted areas are part of this surface.
func LoadAreaManagement(ctx context.Context, querier areaAdministrationQuerier, actor policy.AccessContext) (AreaManagementPage, error) {
	if ctx == nil || querier == nil {
		return AreaManagementPage{}, fmt.Errorf("area administration loader is incomplete")
	}
	if !policy.CanAdminister(actor) {
		return AreaManagementPage{}, ErrAreaAdministrationDenied
	}
	if err := ctx.Err(); err != nil {
		return AreaManagementPage{}, fmt.Errorf("load area administration: %w", err)
	}
	rows, err := querier.ListAreasForAdministration(ctx)
	if err != nil {
		return AreaManagementPage{}, fmt.Errorf("list administered areas: %w", err)
	}
	groups, err := querier.ListForumGroupsForAreaAdministration(ctx)
	if err != nil {
		return AreaManagementPage{}, fmt.Errorf("list area groups: %w", err)
	}
	page := AreaManagementPage{Areas: make([]ManagedArea, len(rows)), Groups: make([]ForumGroup, len(groups))}
	for index, row := range rows {
		state := ManagedArea{ID: row.ID, Slug: row.Slug, Name: row.Name, Description: row.Description, DisplayOrder: row.DisplayOrder, Visibility: policy.Visibility(row.Visibility), PostingMode: policy.PostingMode(row.PostingMode), GroupIDs: slices.Clone(row.GroupIds)}
		if row.UpdatedAt.Valid && row.UpdatedAt.InfinityModifier == pgtype.Finite {
			state.UpdatedAt = row.UpdatedAt.Time.UTC().Truncate(time.Microsecond)
		}
		if !validManagedArea(state) {
			return AreaManagementPage{}, fmt.Errorf("administered area row is invalid")
		}
		page.Areas[index] = state
	}
	for index, group := range groups {
		if group.ID <= 0 || !validCanonicalText(group.Name, 80, false) {
			return AreaManagementPage{}, fmt.Errorf("area group row is invalid")
		}
		page.Groups[index] = ForumGroup{ID: group.ID, Name: group.Name}
	}
	return page, nil
}

func CreateArea(ctx context.Context, beginner transactionBeginner, clock func() time.Time, actor policy.AccessContext, input AreaInput, requestID pgtype.UUID) (AreaMutationResult, error) {
	if err := validateMutationBoundary(ctx, beginner, clock, actor, &input, requestID, true); err != nil {
		return AreaMutationResult{}, err
	}
	now := canonicalTime(clock)
	if !now.Valid {
		return AreaMutationResult{}, fmt.Errorf("area administration clock returned a zero time")
	}
	state := stateFromInput(input)
	result := AreaMutationResult{}
	err := store.WithinTx(ctx, beginner, func(queries *db.Queries) error {
		if err := validateGroupsExist(ctx, queries, input.GroupIDs); err != nil {
			return err
		}
		created, err := queries.CreateAreaForAdministration(ctx, db.CreateAreaForAdministrationParams{
			Slug: input.Slug, Name: input.Name, Description: input.Description, DisplayOrder: input.DisplayOrder,
			Visibility: string(input.Visibility), PostingMode: string(input.PostingMode), ActorUserID: actor.UserID, AtTime: now,
		})
		if err != nil {
			return mapAreaWriteError("create area", err)
		}
		if err := replaceAreaGroups(ctx, queries, created.ID, actor.UserID, input.GroupIDs, now); err != nil {
			return err
		}
		auditID, err := insertAreaAudit(ctx, queries, actor.UserID, created.ID, "create_area", input.Reason, auditedAreaState{}, state, requestID, now)
		if err != nil {
			return err
		}
		result = AreaMutationResult{AreaID: created.ID, Slug: created.Slug, AuditID: auditID}
		return nil
	})
	if err != nil {
		return AreaMutationResult{}, fmt.Errorf("create area transaction: %w", err)
	}
	return result, nil
}

func UpdateArea(ctx context.Context, beginner transactionBeginner, clock func() time.Time, actor policy.AccessContext, areaID int64, input AreaInput, requestID pgtype.UUID) (AreaMutationResult, error) {
	if areaID <= 0 {
		return AreaMutationResult{}, fmt.Errorf("%w: target", ErrAreaAdministrationInput)
	}
	if err := validateMutationBoundary(ctx, beginner, clock, actor, &input, requestID, false); err != nil {
		return AreaMutationResult{}, err
	}
	now := canonicalTime(clock)
	if !now.Valid {
		return AreaMutationResult{}, fmt.Errorf("area administration clock returned a zero time")
	}
	result := AreaMutationResult{}
	err := store.WithinTx(ctx, beginner, func(queries *db.Queries) error {
		current, err := queries.LockAreaForAdministration(ctx, areaID)
		if err != nil {
			return fmt.Errorf("lock area for administration: %w", err)
		}
		previous := auditedAreaState{Slug: current.Slug, Name: current.Name, Description: current.Description, DisplayOrder: current.DisplayOrder, Visibility: policy.Visibility(current.Visibility), PostingMode: policy.PostingMode(current.PostingMode), GroupIDs: slices.Clone(current.GroupIds)}
		if !validAuditedState(previous) || input.Slug != current.Slug {
			return fmt.Errorf("%w: immutable slug", ErrAreaAdministrationInput)
		}
		if !current.UpdatedAt.Valid || current.UpdatedAt.InfinityModifier != pgtype.Finite || !current.UpdatedAt.Time.UTC().Truncate(time.Microsecond).Equal(input.Revision) {
			return ErrAreaAdministrationConflict
		}
		if err := validateGroupsExist(ctx, queries, input.GroupIDs); err != nil {
			return err
		}
		resulting := stateFromInput(input)
		if equalAreaStates(previous, resulting) {
			return ErrAreaAdministrationConflict
		}
		if err := queries.DeleteAreaGroupsForAdministration(ctx, areaID); err != nil {
			return fmt.Errorf("remove area groups: %w", err)
		}
		updated, err := queries.UpdateAreaForAdministration(ctx, db.UpdateAreaForAdministrationParams{
			Name: input.Name, Description: input.Description, DisplayOrder: input.DisplayOrder,
			Visibility: string(input.Visibility), PostingMode: string(input.PostingMode), ActorUserID: actor.UserID,
			AtTime: now, AreaID: areaID,
		})
		if err != nil {
			return mapAreaWriteError("update area", err)
		}
		if err := replaceAreaGroups(ctx, queries, areaID, actor.UserID, input.GroupIDs, updated.UpdatedAt); err != nil {
			return err
		}
		auditID, err := insertAreaAudit(ctx, queries, actor.UserID, areaID, "update_area", input.Reason, previous, resulting, requestID, updated.UpdatedAt)
		if err != nil {
			return err
		}
		result = AreaMutationResult{AreaID: areaID, Slug: current.Slug, AuditID: auditID}
		return nil
	})
	if err != nil {
		return AreaMutationResult{}, fmt.Errorf("update area transaction: %w", err)
	}
	return result, nil
}

func validateMutationBoundary(ctx context.Context, beginner transactionBeginner, clock func() time.Time, actor policy.AccessContext, input *AreaInput, requestID pgtype.UUID, requireSlug bool) error {
	if ctx == nil || beginner == nil || clock == nil {
		return fmt.Errorf("area administration mutation boundary is incomplete")
	}
	if !policy.CanAdminister(actor) {
		return ErrAreaAdministrationDenied
	}
	if input == nil || requireSlug && !policy.ValidAreaSlug(input.Slug) || !validCanonicalText(input.Name, 120, false) || !validDescription(input.Description) || input.DisplayOrder < 0 || !validReason(input.Reason) {
		return ErrAreaAdministrationInput
	}
	if !requireSlug && !policy.ValidAreaSlug(input.Slug) {
		return ErrAreaAdministrationInput
	}
	if requireSlug && !input.Revision.IsZero() || !requireSlug && (input.Revision.IsZero() || input.Revision.Location() != time.UTC || !input.Revision.Equal(input.Revision.Truncate(time.Microsecond))) {
		return ErrAreaAdministrationInput
	}
	if input.Visibility != policy.VisibilityPublic && input.Visibility != policy.VisibilityAuthenticated && input.Visibility != policy.VisibilityGroups || input.PostingMode != policy.PostingNormal && input.PostingMode != policy.PostingReadOnly && input.PostingMode != policy.PostingArchived {
		return ErrAreaAdministrationInput
	}
	input.GroupIDs = slices.Clone(input.GroupIDs)
	slices.Sort(input.GroupIDs)
	for index, groupID := range input.GroupIDs {
		if groupID <= 0 || index > 0 && groupID == input.GroupIDs[index-1] {
			return ErrAreaAdministrationInput
		}
	}
	if input.Visibility == policy.VisibilityGroups && len(input.GroupIDs) == 0 || input.Visibility != policy.VisibilityGroups && len(input.GroupIDs) != 0 {
		return ErrAreaAdministrationInput
	}
	if !requestID.Valid || requestID.Bytes == ([16]byte{}) {
		return fmt.Errorf("area administration request ID is invalid")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("administer area: %w", err)
	}
	return nil
}

func canonicalTime(clock func() time.Time) pgtype.Timestamptz {
	value := clock()
	return pgtype.Timestamptz{Time: value.UTC().Truncate(time.Microsecond), Valid: !value.IsZero()}
}

func validateGroupsExist(ctx context.Context, queries *db.Queries, groupIDs []int64) error {
	if len(groupIDs) == 0 {
		return nil
	}
	count, err := queries.CountExistingForumGroups(ctx, groupIDs)
	if err != nil {
		return fmt.Errorf("validate area groups: %w", err)
	}
	if count != int64(len(groupIDs)) {
		return fmt.Errorf("%w: group", ErrAreaAdministrationInput)
	}
	return nil
}

func replaceAreaGroups(ctx context.Context, queries *db.Queries, areaID, actorID int64, groupIDs []int64, at pgtype.Timestamptz) error {
	for _, groupID := range groupIDs {
		if err := queries.AddAreaGroupForAdministration(ctx, db.AddAreaGroupForAdministrationParams{AreaID: areaID, GroupID: groupID, ActorUserID: actorID, AtTime: at}); err != nil {
			return mapAreaWriteError("add area group", err)
		}
	}
	return nil
}

func insertAreaAudit(ctx context.Context, queries *db.Queries, actorID, areaID int64, action, reason string, previous, resulting auditedAreaState, requestID pgtype.UUID, at pgtype.Timestamptz) (int64, error) {
	previousJSON, err := json.Marshal(previous)
	if err != nil {
		return 0, fmt.Errorf("encode previous area state: %w", err)
	}
	if action == "create_area" {
		previousJSON = []byte("{}")
	}
	resultingJSON, err := json.Marshal(resulting)
	if err != nil {
		return 0, fmt.Errorf("encode resulting area state: %w", err)
	}
	auditID, err := queries.CreateAreaAdministrationAudit(ctx, db.CreateAreaAdministrationAuditParams{
		ActorUserID: pgtype.Int8{Int64: actorID, Valid: true}, AreaID: pgtype.Int8{Int64: areaID, Valid: true},
		ActionType: action, Reason: pgtype.Text{String: reason, Valid: true}, PreviousState: previousJSON,
		ResultingState: resultingJSON, RequestID: requestID, AtTime: at,
	})
	if err != nil {
		return 0, fmt.Errorf("write area administration audit: %w", err)
	}
	if auditID <= 0 {
		return 0, fmt.Errorf("area administration audit returned an invalid identifier")
	}
	return auditID, nil
}

func stateFromInput(input AreaInput) auditedAreaState {
	return auditedAreaState{Slug: input.Slug, Name: input.Name, Description: input.Description, DisplayOrder: input.DisplayOrder, Visibility: input.Visibility, PostingMode: input.PostingMode, GroupIDs: slices.Clone(input.GroupIDs)}
}

func validManagedArea(area ManagedArea) bool {
	return area.ID > 0 && !area.UpdatedAt.IsZero() && area.UpdatedAt.Location() == time.UTC && area.UpdatedAt.Equal(area.UpdatedAt.Truncate(time.Microsecond)) && policy.ValidAreaSlug(area.Slug) && validCanonicalText(area.Name, 120, false) && validDescription(area.Description) && area.DisplayOrder >= 0 && validAuditedState(auditedAreaState{Slug: area.Slug, Name: area.Name, Description: area.Description, DisplayOrder: area.DisplayOrder, Visibility: area.Visibility, PostingMode: area.PostingMode, GroupIDs: area.GroupIDs})
}

func validAuditedState(state auditedAreaState) bool {
	if !policy.ValidAreaSlug(state.Slug) || !validCanonicalText(state.Name, 120, false) || !validDescription(state.Description) || state.DisplayOrder < 0 || state.Visibility != policy.VisibilityPublic && state.Visibility != policy.VisibilityAuthenticated && state.Visibility != policy.VisibilityGroups || state.PostingMode != policy.PostingNormal && state.PostingMode != policy.PostingReadOnly && state.PostingMode != policy.PostingArchived {
		return false
	}
	if state.Visibility == policy.VisibilityGroups && len(state.GroupIDs) == 0 || state.Visibility != policy.VisibilityGroups && len(state.GroupIDs) != 0 {
		return false
	}
	for index, groupID := range state.GroupIDs {
		if groupID <= 0 || index > 0 && groupID <= state.GroupIDs[index-1] {
			return false
		}
	}
	return true
}

func equalAreaStates(left, right auditedAreaState) bool {
	return left.Slug == right.Slug && left.Name == right.Name && left.Description == right.Description &&
		left.DisplayOrder == right.DisplayOrder && left.Visibility == right.Visibility &&
		left.PostingMode == right.PostingMode && slices.Equal(left.GroupIDs, right.GroupIDs)
}

func validCanonicalText(value string, maximumRunes int, allowEmpty bool) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maximumRunes || (!allowEmpty && value == "") || strings.TrimSpace(value) != value {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) }) < 0
}

func validDescription(value string) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= 4000 && strings.TrimSpace(value) == value && strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) < 0
}

func validReason(value string) bool {
	return validCanonicalText(value, 2000, false)
}

func mapAreaWriteError(operation string, err error) error {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && (databaseError.Code == "23505" || databaseError.Code == "23503" || databaseError.Code == "23514") {
		return fmt.Errorf("%w: %s", ErrAreaAdministrationConflict, operation)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
