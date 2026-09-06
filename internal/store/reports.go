package store

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	ReportPageSize    int32 = 25
	MaximumReportPage int32 = 10_000
)

type reportReader interface {
	ListActiveReportsForModeration(context.Context, db.ListActiveReportsForModerationParams) ([]db.ListActiveReportsForModerationRow, error)
	GetReportForModeration(context.Context, db.GetReportForModerationParams) (db.GetReportForModerationRow, error)
	ListReportNotes(context.Context, db.ListReportNotesParams) ([]db.ListReportNotesRow, error)
}

type ModerationReportSummary struct {
	ID, TargetID       int64
	Reason, Status     string
	Reporter, Assignee string
	TargetType         string
	TargetLabel        string
	CreatedAt          time.Time
}

type ModerationReportPage struct {
	Reports    []ModerationReportSummary
	Number     int32
	Total      int64
	TotalPages int64
}

type ModerationReportNote struct {
	ID, AuthorID int64
	Author, Body string
	CreatedAt    time.Time
}

type ModerationReportDetail struct {
	ID, ReporterID, TargetID, TargetTopicID int64
	TargetPage                              int64
	Reporter, Reason, Status                string
	Assignee, Resolution, Resolver          string
	AssignedTo                              int64
	TargetType, TargetLabel                 string
	CreatedAt, UpdatedAt                    time.Time
	ResolvedAt                              *time.Time
	Notes                                   []ModerationReportNote
}

func ListModerationReports(ctx context.Context, reader reportReader, actor policy.AccessContext, page int32, observedAt time.Time) (ModerationReportPage, error) {
	if ctx == nil || reader == nil || !actor.Valid() || observedAt.IsZero() {
		return ModerationReportPage{}, fmt.Errorf("moderation report list dependencies are invalid")
	}
	if err := ctx.Err(); err != nil {
		return ModerationReportPage{}, fmt.Errorf("list moderation reports: %w", err)
	}
	if !activeStaff(actor) || page < 1 || page > MaximumReportPage {
		return ModerationReportPage{}, fmt.Errorf("list moderation reports: %w", pgx.ErrNoRows)
	}
	parameters := db.ListActiveReportsForModerationParams{
		ActorUserID: actor.UserID, ActorRole: storedPolicyRole(actor.Role),
		ObservedAt: pgtype.Timestamptz{Time: observedAt.UTC().Truncate(time.Microsecond), Valid: true},
		PageOffset: (page - 1) * ReportPageSize, PageLimit: ReportPageSize,
	}
	rows, err := reader.ListActiveReportsForModeration(ctx, parameters)
	if err != nil {
		return ModerationReportPage{}, fmt.Errorf("query moderation reports: %w", err)
	}
	result := ModerationReportPage{Reports: make([]ModerationReportSummary, 0, len(rows)), Number: page}
	for index, row := range rows {
		if !validActiveReportRow(row) || index > 0 && row.TotalActive != rows[0].TotalActive {
			return ModerationReportPage{}, fmt.Errorf("moderation report query returned malformed rows")
		}
		if index == 0 {
			result.Total = row.TotalActive
			result.TotalPages = 1 + (row.TotalActive-1)/int64(ReportPageSize)
		}
		assignee := ""
		if row.AssignedTo.Valid {
			assignee = row.AssigneeDisplayName.String
		}
		result.Reports = append(result.Reports, ModerationReportSummary{
			ID:          row.ID,
			TargetID:    row.TargetID,
			Reason:      row.Reason,
			Status:      row.Status,
			Reporter:    row.ReporterDisplayName,
			Assignee:    assignee,
			TargetType:  row.TargetType,
			TargetLabel: row.TargetLabel,
			CreatedAt:   row.CreatedAt.Time,
		})
	}
	if len(rows) == 0 && page != 1 {
		return ModerationReportPage{}, fmt.Errorf("list moderation reports: %w", pgx.ErrNoRows)
	}
	return result, nil
}

func GetModerationReport(ctx context.Context, reader reportReader, actor policy.AccessContext, reportID int64, observedAt time.Time) (ModerationReportDetail, error) {
	if ctx == nil || reader == nil || !actor.Valid() || observedAt.IsZero() {
		return ModerationReportDetail{}, fmt.Errorf("moderation report detail dependencies are invalid")
	}
	if err := ctx.Err(); err != nil {
		return ModerationReportDetail{}, fmt.Errorf("get moderation report: %w", err)
	}
	if !activeStaff(actor) || reportID <= 0 {
		return ModerationReportDetail{}, fmt.Errorf("get moderation report: %w", pgx.ErrNoRows)
	}
	parameters := db.GetReportForModerationParams{
		ReportID:    reportID,
		ActorUserID: actor.UserID,
		ActorRole:   storedPolicyRole(actor.Role),
		ObservedAt:  pgtype.Timestamptz{Time: observedAt.UTC().Truncate(time.Microsecond), Valid: true},
	}
	row, err := reader.GetReportForModeration(ctx, parameters)
	if err != nil {
		return ModerationReportDetail{}, fmt.Errorf("query moderation report: %w", err)
	}
	if !validReportDetailRow(row) {
		return ModerationReportDetail{}, fmt.Errorf("moderation report detail returned malformed row")
	}
	notes, err := reader.ListReportNotes(ctx, db.ListReportNotesParams{
		ReportID:    reportID,
		ActorUserID: actor.UserID,
		ActorRole:   parameters.ActorRole,
		ObservedAt:  parameters.ObservedAt,
	})
	if err != nil {
		return ModerationReportDetail{}, fmt.Errorf("query report notes: %w", err)
	}
	if len(notes) == 0 {
		return ModerationReportDetail{}, fmt.Errorf("report notes authorization changed: %w", pgx.ErrNoRows)
	}
	detail := ModerationReportDetail{
		ID:            row.ID,
		ReporterID:    row.ReportedBy,
		TargetID:      row.TargetID,
		TargetTopicID: row.TargetTopicID,
		TargetPage:    row.TargetPage,
		Reporter:      row.ReporterDisplayName,
		Reason:        row.Reason,
		Status:        row.Status,
		TargetType:    row.TargetType,
		TargetLabel:   row.TargetLabel,
		CreatedAt:     row.CreatedAt.Time,
		UpdatedAt:     row.UpdatedAt.Time,
		Notes:         make([]ModerationReportNote, 0, len(notes)),
	}
	if row.AssignedTo.Valid {
		detail.AssignedTo, detail.Assignee = row.AssignedTo.Int64, row.AssigneeDisplayName.String
	}
	if row.Resolution.Valid {
		detail.Resolution = row.Resolution.String
	}
	if row.ResolvedBy.Valid {
		detail.Resolver = row.ResolverDisplayName.String
	}
	if row.ResolvedAt.Valid {
		resolved := row.ResolvedAt.Time
		detail.ResolvedAt = &resolved
	}
	for _, note := range notes {
		if note.AuthorizedReportID != reportID {
			return ModerationReportDetail{}, fmt.Errorf("moderation report notes returned malformed row")
		}
		if !note.ID.Valid {
			if len(notes) != 1 || note.AuthorID.Valid || note.AuthorDisplayName.Valid || note.Body.Valid || note.CreatedAt.Valid {
				return ModerationReportDetail{}, fmt.Errorf("moderation report notes returned malformed empty row")
			}
			continue
		}
		if note.ID.Int64 <= 0 || !note.AuthorID.Valid || note.AuthorID.Int64 <= 0 ||
			!note.AuthorDisplayName.Valid || note.AuthorDisplayName.String == "" ||
			!note.Body.Valid || !validReportTextValue(note.Body.String) || !validReportTimestamp(note.CreatedAt) {
			return ModerationReportDetail{}, fmt.Errorf("moderation report notes returned malformed row")
		}
		detail.Notes = append(detail.Notes, ModerationReportNote{
			ID:        note.ID.Int64,
			AuthorID:  note.AuthorID.Int64,
			Author:    note.AuthorDisplayName.String,
			Body:      note.Body.String,
			CreatedAt: note.CreatedAt.Time,
		})
	}
	return detail, nil
}

func activeStaff(actor policy.AccessContext) bool {
	return actor.Authenticated && !actor.Suspended && actor.MutedUntil == nil && (actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator)
}

func storedPolicyRole(role policy.Role) string {
	switch role {
	case policy.RoleModerator:
		return "moderator"
	case policy.RoleAdministrator:
		return "administrator"
	default:
		return ""
	}
}

func validActiveReportRow(row db.ListActiveReportsForModerationRow) bool {
	open := row.Status == "open" && !row.AssignedTo.Valid && !row.AssigneeDisplayName.Valid
	inReview := row.Status == "in_review" && row.AssignedTo.Valid && row.AssignedTo.Int64 > 0 &&
		row.AssigneeDisplayName.Valid && row.AssigneeDisplayName.String != ""
	validTargetType := row.TargetType == "topic" || row.TargetType == "post" || row.TargetType == "user"
	return row.ID > 0 && row.TargetID > 0 && row.ReporterDisplayName != "" && row.TargetLabel != "" &&
		validReportTextValue(row.Reason) && (open || inReview) && validTargetType &&
		validReportTimestamp(row.CreatedAt) && row.TotalActive > 0
}

func validReportDetailRow(row db.GetReportForModerationRow) bool {
	invalidIdentity := row.ID <= 0 || row.ReportedBy <= 0 || row.ReporterDisplayName == "" ||
		row.TargetID <= 0 || row.TargetLabel == ""
	invalidContent := !validReportTextValue(row.Reason) || !validReportTimestamp(row.CreatedAt) ||
		!validReportTimestamp(row.UpdatedAt) || row.UpdatedAt.Time.Before(row.CreatedAt.Time)
	if invalidIdentity || invalidContent {
		return false
	}
	invalidType := row.TargetType != "topic" && row.TargetType != "post" && row.TargetType != "user"
	invalidTopic := row.TargetType == "topic" && (row.TargetTopicID != row.TargetID || row.TargetPage != 0) ||
		row.TargetType == "post" && (row.TargetTopicID <= 0 || row.TargetPage <= 0 || row.TargetPage > int64(MaximumPostPage)) ||
		row.TargetType == "user" && (row.TargetTopicID != 0 || row.TargetPage != 0)
	if invalidType || invalidTopic {
		return false
	}
	switch row.Status {
	case "open":
		return !row.AssignedTo.Valid && !row.AssigneeDisplayName.Valid && !row.Resolution.Valid && !row.ResolvedBy.Valid && !row.ResolverDisplayName.Valid && !row.ResolvedAt.Valid
	case "in_review":
		return row.AssignedTo.Valid && row.AssignedTo.Int64 > 0 &&
			row.AssigneeDisplayName.Valid && row.AssigneeDisplayName.String != "" &&
			!row.Resolution.Valid && !row.ResolvedBy.Valid &&
			!row.ResolverDisplayName.Valid && !row.ResolvedAt.Valid
	case "resolved", "dismissed":
		return row.AssignedTo.Valid && row.AssignedTo.Int64 > 0 &&
			row.AssigneeDisplayName.Valid && row.AssigneeDisplayName.String != "" &&
			row.Resolution.Valid && validReportTextValue(row.Resolution.String) &&
			row.ResolvedBy.Valid && row.ResolvedBy.Int64 > 0 &&
			row.ResolverDisplayName.Valid && row.ResolverDisplayName.String != "" &&
			validReportTimestamp(row.ResolvedAt)
	default:
		return false
	}
}

func validReportTextValue(value string) bool {
	return len(value) > 0 && len(value) <= 2_000 && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) < 0
}

func validReportTimestamp(value pgtype.Timestamptz) bool {
	return value.Valid && value.InfinityModifier == pgtype.Finite && !value.Time.IsZero()
}
