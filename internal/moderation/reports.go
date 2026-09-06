package moderation

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
)

const MaximumActiveReportsPerUser int32 = 25

var (
	ErrReportInput     = errors.New("invalid report input")
	ErrReportDenied    = errors.New("report denied")
	ErrReportDuplicate = errors.New("duplicate active report")
	ErrReportLimit     = errors.New("active report limit reached")
	ErrReportConflict  = errors.New("report state conflict")
)

type ReportTargetType string

const (
	ReportTopic ReportTargetType = "topic"
	ReportPost  ReportTargetType = "post"
	ReportUser  ReportTargetType = "user"
)

type ReportResult struct {
	ReportID  int64
	Target    ReportTargetType
	TargetID  int64
	Status    string
	CreatedAt time.Time
}

// CreateReport serializes on the persisted reporter row, enforces the active
// cap, proves target visibility in SQL, and inserts one open report. Muted
// members retain this safety channel; suspended or stale actors do not.
func CreateReport(
	ctx context.Context,
	beginner transactionBeginner,
	clock func() time.Time,
	actor policy.AccessContext,
	target ReportTargetType,
	targetID int64,
	reason string,
) (ReportResult, error) {
	if ctx == nil || beginner == nil || clock == nil {
		return ReportResult{}, fmt.Errorf("report dependencies are required")
	}
	if err := ctx.Err(); err != nil {
		return ReportResult{}, fmt.Errorf("create report: %w", err)
	}
	if !actor.Valid() || !actor.Authenticated {
		return ReportResult{}, ErrReportDenied
	}
	if target != ReportTopic && target != ReportPost && target != ReportUser || targetID <= 0 || !validReportText(reason) {
		return ReportResult{}, fmt.Errorf("%w: target or reason", ErrReportInput)
	}
	if actor.Suspended {
		return ReportResult{}, ErrReportDenied
	}
	now := clock()
	if now.IsZero() {
		return ReportResult{}, fmt.Errorf("report clock returned zero time")
	}
	now = now.UTC().Truncate(time.Microsecond)
	var result ReportResult
	err := store.WithinTx(ctx, beginner, func(queries *db.Queries) error {
		persisted, err := queries.LockUserForSuspension(ctx, actor.UserID)
		if err != nil {
			return fmt.Errorf("lock report author: %w", err)
		}
		if !validSuspensionTarget(persisted, actor.UserID) {
			return fmt.Errorf("report author row is invalid")
		}
		role, valid := roleFromStorage(persisted.Role)
		if !valid || role != actor.Role || userSuspendedAt(persisted, now) {
			return ErrReportDenied
		}
		count, err := queries.CountActiveReportsByReporter(ctx, actor.UserID)
		if err != nil {
			return fmt.Errorf("count active reports: %w", err)
		}
		if count >= MaximumActiveReportsPerUser {
			return ErrReportLimit
		}
		atTime := pgtype.Timestamptz{Time: now, Valid: true}
		staff := actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator
		switch target {
		case ReportTopic:
			row, createErr := queries.CreateTopicReport(ctx, db.CreateTopicReportParams{
				ReportedBy: actor.UserID, Reason: reason, AtTime: atTime, TopicID: targetID,
				IsStaff: staff, IsMember: true, GroupIds: actor.GroupIDs,
			})
			if createErr != nil {
				return mapCreateReportError(createErr)
			}
			if !row.TopicID.Valid || row.PostID.Valid || row.UserID.Valid || !finiteTimestamp(row.CreatedAt) {
				return fmt.Errorf("create topic report returned malformed state")
			}
			result = ReportResult{
				ReportID: row.ID, Target: target, TargetID: row.TopicID.Int64,
				Status: row.Status, CreatedAt: row.CreatedAt.Time,
			}
		case ReportPost:
			row, createErr := queries.CreatePostReport(ctx, db.CreatePostReportParams{
				ReportedBy: actor.UserID, Reason: reason, AtTime: atTime, PostID: targetID,
				IsStaff: staff, IsMember: true, GroupIds: actor.GroupIDs,
			})
			if createErr != nil {
				return mapCreateReportError(createErr)
			}
			if row.TopicID.Valid || !row.PostID.Valid || row.UserID.Valid || !finiteTimestamp(row.CreatedAt) {
				return fmt.Errorf("create post report returned malformed state")
			}
			result = ReportResult{
				ReportID: row.ID, Target: target, TargetID: row.PostID.Int64,
				Status: row.Status, CreatedAt: row.CreatedAt.Time,
			}
		case ReportUser:
			row, createErr := queries.CreateUserReport(ctx, db.CreateUserReportParams{
				ReportedBy: actor.UserID, Reason: reason, AtTime: atTime, UserID: targetID,
				IsStaff: staff, IsMember: true, GroupIds: actor.GroupIDs,
			})
			if createErr != nil {
				return mapCreateReportError(createErr)
			}
			if row.TopicID.Valid || row.PostID.Valid || !row.UserID.Valid || !finiteTimestamp(row.CreatedAt) {
				return fmt.Errorf("create user report returned malformed state")
			}
			result = ReportResult{
				ReportID: row.ID, Target: target, TargetID: row.UserID.Int64,
				Status: row.Status, CreatedAt: row.CreatedAt.Time,
			}
		}
		if result.ReportID <= 0 || result.TargetID != targetID || result.Status != "open" || result.CreatedAt.IsZero() {
			return fmt.Errorf("create report returned an invalid result")
		}
		return nil
	})
	if err != nil {
		return ReportResult{}, fmt.Errorf("create report transaction: %w", err)
	}
	return result, nil
}

type ReportAction string

const (
	ClaimReport   ReportAction = "claim"
	NoteReport    ReportAction = "note"
	ResolveReport ReportAction = "resolve"
	DismissReport ReportAction = "dismiss"
)

type ReportActionResult struct {
	ReportID int64
	Status   string
	NoteID   int64
	AuditID  int64
}

// ProcessReport applies one strict, serialized staff transition and appends
// its audit in the same transaction. Claims are self-assignment only.
func ProcessReport(
	ctx context.Context,
	beginner transactionBeginner,
	clock func() time.Time,
	actor policy.AccessContext,
	reportID int64,
	action ReportAction,
	text string,
	requestID pgtype.UUID,
) (ReportActionResult, error) {
	if ctx == nil || beginner == nil || clock == nil {
		return ReportActionResult{}, fmt.Errorf("report processing dependencies are required")
	}
	if err := ctx.Err(); err != nil {
		return ReportActionResult{}, fmt.Errorf("process report: %w", err)
	}
	if !actor.Valid() || !actor.Authenticated {
		return ReportActionResult{}, ErrReportDenied
	}
	staffRole := actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator
	if actor.Suspended || actor.MutedUntil != nil || !staffRole {
		return ReportActionResult{}, ErrReportDenied
	}
	if reportID <= 0 || action != ClaimReport && action != NoteReport && action != ResolveReport && action != DismissReport {
		return ReportActionResult{}, fmt.Errorf("%w: report action", ErrReportInput)
	}
	if action == ClaimReport {
		if text != "" {
			return ReportActionResult{}, fmt.Errorf("%w: claim text", ErrReportInput)
		}
	} else if !validReportText(text) {
		return ReportActionResult{}, fmt.Errorf("%w: action text", ErrReportInput)
	}
	if !requestID.Valid || requestID.Bytes == ([16]byte{}) {
		return ReportActionResult{}, fmt.Errorf("report request ID is invalid")
	}
	now := clock()
	if now.IsZero() {
		return ReportActionResult{}, fmt.Errorf("report processing clock returned zero time")
	}
	now = now.UTC().Truncate(time.Microsecond)
	var result ReportActionResult
	err := store.WithinTx(ctx, beginner, func(queries *db.Queries) error {
		persisted, err := queries.LockUserForSuspension(ctx, actor.UserID)
		if err != nil {
			return fmt.Errorf("lock report actor: %w", err)
		}
		role, valid := roleFromStorage(persisted.Role)
		persistedStaffRole := role == policy.RoleModerator || role == policy.RoleAdministrator
		persistedMuted := persisted.MutedUntil.Valid && persisted.MutedUntil.Time.After(now)
		if !validSuspensionTarget(persisted, actor.UserID) || !valid || role != actor.Role ||
			userSuspendedAt(persisted, now) || persistedMuted || !persistedStaffRole {
			return ErrReportDenied
		}
		report, err := queries.LockReportForModeration(ctx, reportID)
		if err != nil {
			return fmt.Errorf("lock report: %w", err)
		}
		if report.ID != reportID || !finiteTimestamp(report.CreatedAt) || !finiteTimestamp(report.UpdatedAt) || report.UpdatedAt.Time.Before(report.CreatedAt.Time) {
			return fmt.Errorf("locked report is invalid")
		}
		at := now
		if report.UpdatedAt.Time.After(at) {
			at = report.UpdatedAt.Time.UTC().Truncate(time.Microsecond)
		}
		atTime := pgtype.Timestamptz{Time: at, Valid: true}
		actorID := pgtype.Int8{Int64: actor.UserID, Valid: true}
		if action == ClaimReport {
			if report.Status != "open" || report.AssignedTo.Valid {
				return ErrReportConflict
			}
			changed, changeErr := queries.ClaimReportAndAudit(ctx, db.ClaimReportAndAuditParams{
				ActorUserID: actorID, AtTime: atTime, ReportID: reportID, RequestID: requestID,
			})
			if changeErr != nil {
				return fmt.Errorf("claim report and audit: %w", changeErr)
			}
			if changed.ReportID != reportID || changed.Status != "in_review" ||
				!changed.AssignedTo.Valid || changed.AssignedTo.Int64 != actor.UserID ||
				!finiteTimestamp(changed.UpdatedAt) || changed.AuditID <= 0 {
				return fmt.Errorf("claim report returned an invalid result")
			}
			result = ReportActionResult{ReportID: reportID, Status: changed.Status, AuditID: changed.AuditID}
			return nil
		}
		wrongAssignee := actor.Role != policy.RoleAdministrator && report.AssignedTo.Int64 != actor.UserID
		if report.Status != "in_review" || !report.AssignedTo.Valid || wrongAssignee {
			return ErrReportConflict
		}
		if action == NoteReport {
			added, addErr := queries.AddReportNoteAndAudit(ctx, db.AddReportNoteAndAuditParams{
				ReportID: reportID, ActorUserID: actor.UserID, Body: text,
				AtTime: atTime, RequestID: requestID,
			})
			if addErr != nil {
				return fmt.Errorf("add report note and audit: %w", addErr)
			}
			if added.ReportID != reportID || added.AuthorID != actor.UserID || added.NoteID <= 0 || added.AuditID <= 0 {
				return fmt.Errorf("add report note returned an invalid result")
			}
			result = ReportActionResult{ReportID: reportID, Status: "in_review", NoteID: added.NoteID, AuditID: added.AuditID}
			return nil
		}
		status, auditAction := "resolved", "resolve_report"
		if action == DismissReport {
			status, auditAction = "dismissed", "dismiss_report"
		}
		finished, finishErr := queries.FinishReportAndAudit(ctx, db.FinishReportAndAuditParams{
			ResultingStatus: status,
			Resolution:      pgtype.Text{String: text, Valid: true},
			ActorUserID:     actorID,
			AtTime:          atTime,
			ReportID:        reportID,
			ActionType:      auditAction,
			RequestID:       requestID,
		})
		if finishErr != nil {
			return fmt.Errorf("finish report and audit: %w", finishErr)
		}
		if finished.ReportID != reportID || finished.Status != status ||
			!finished.AssignedTo.Valid || !finished.ResolvedBy.Valid ||
			finished.ResolvedBy.Int64 != actor.UserID || !finiteTimestamp(finished.ResolvedAt) ||
			!finiteTimestamp(finished.UpdatedAt) || finished.AuditID <= 0 {
			return fmt.Errorf("finish report returned an invalid result")
		}
		result = ReportActionResult{ReportID: reportID, Status: status, AuditID: finished.AuditID}
		return nil
	})
	if err != nil {
		return ReportActionResult{}, fmt.Errorf("process report transaction: %w", err)
	}
	return result, nil
}

func validReportText(value string) bool {
	if len(value) == 0 || len(value) > 2_000 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\t'
	}) < 0
}

func mapCreateReportError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && databaseError.Code == "23505" {
		return ErrReportDuplicate
	}
	return fmt.Errorf("insert report: %w", err)
}
