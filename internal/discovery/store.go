package discovery

import (
	"context"
	"errors"
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/gotthboard/gotth-bb/internal/policy"
	contentrender "github.com/gotthboard/gotth-bb/internal/render"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/text/unicode/norm"
)

const (
	SearchPageSize      = 25
	ActivityPageSize    = 25
	ActivityQueryLimit  = 26
	MaximumExcerptRunes = 300
)

var ErrInvalidSearchQuery = errors.New("search query has no bounded positive expression")

type SearchResult struct {
	Kind       string
	ID         int64
	AreaID     int64
	AreaSlug   string
	AreaName   string
	TopicID    int64
	TopicTitle string
	PostID     int64
	AuthorID   int64
	AuthorName string
	CreatedAt  pgtype.Timestamptz
	Rank       float32
	Excerpt    string
}

type SearchPage struct {
	Results      []SearchResult
	HasNextPage  bool
	BeyondWindow bool
}

type ActivityResult struct {
	PostID     int64
	CreatedAt  pgtype.Timestamptz
	AuthorID   int64
	AuthorName string
	TopicID    int64
	TopicTitle string
	AreaID     int64
	AreaSlug   string
	AreaName   string
}

type ActivityPage struct {
	Results      []ActivityResult
	NextBoundary *ActivityBoundary
	NextCursor   string
}

type DirectPost struct {
	PostID          int64
	CreatedAt       pgtype.Timestamptz
	UpdatedAt       pgtype.Timestamptz
	EditedAt        pgtype.Timestamptz
	Revision        int32
	Body            contentrender.TrustedHTML
	VisibleText     string
	RendererVersion string
	AuthorID        int64
	AuthorName      string
	TopicID         int64
	TopicTitle      string
	AreaID          int64
	AreaSlug        string
	AreaName        string
}

type searchQuerier interface {
	ParseDiscoveryQuery(context.Context, string) (db.ParseDiscoveryQueryRow, error)
	SearchDiscoveryPage(context.Context, db.SearchDiscoveryPageParams) ([]db.SearchDiscoveryPageRow, error)
}

type activityQuerier interface {
	ListRecentActivity(context.Context, activityQueryParameters) ([]activityQueryRow, error)
}

type activityQueryParameters struct {
	IsStaff  bool
	IsMember bool
	GroupIDs []int64
	Boundary ActivityBoundary
}

type activityQueryRow struct {
	PostID     int64
	CreatedAt  pgtype.Timestamptz
	AuthorID   int64
	AuthorName string
	TopicID    int64
	TopicTitle string
	AreaID     int64
	AreaSlug   string
	AreaName   string
}

type directPostQuerier interface {
	GetDirectPost(context.Context, db.GetDirectPostParams) (db.GetDirectPostRow, error)
}

// searchWithQuerier performs semantic parsing and validates every selected-row
// recheck before returning a fully buffered page.
//
// Complexity: with r <= 25 rows and h bounded rendered-HTML bytes, local time
// and auxiliary space are O(r+h), Omega(1), tight Theta(r+h) when post excerpts
// are present; PostgreSQL parse/search costs remain delegated and variable.
func searchWithQuerier(ctx context.Context, querier searchQuerier, request SearchRequest, actor policy.AccessContext) (SearchPage, error) {
	if ctx == nil || querier == nil || !actor.Valid() || !validSearchRequest(request) {
		return SearchPage{}, fmt.Errorf("search input is invalid")
	}
	if err := ctx.Err(); err != nil {
		return SearchPage{}, fmt.Errorf("search canceled: %w", err)
	}
	parsedQuery := ""
	if request.Query != "" {
		parsed, err := querier.ParseDiscoveryQuery(ctx, request.Query)
		if err != nil {
			return SearchPage{}, fmt.Errorf("parse search query: %w", err)
		}
		if parsed.NodeCount < 1 || parsed.NodeCount > 31 || parsed.PositiveQuery == "T" || parsed.ParsedQuery == "" {
			return SearchPage{}, ErrInvalidSearchQuery
		}
		parsedQuery = parsed.ParsedQuery
	}
	parameters := db.SearchDiscoveryPageParams{
		IsStaff:  actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator,
		IsMember: actor.Authenticated, GroupIds: actor.GroupIDs, HasQuery: request.Query != "", ParsedQuery: parsedQuery,
		AuthorID: request.AuthorID, AreaSlug: request.AreaSlug, HasFrom: !request.From.IsZero(), HasTo: !request.ToExclusive.IsZero(),
		FromTime:    pgtype.Timestamptz{Time: request.From, Valid: !request.From.IsZero()},
		ToInclusive: request.ToInclusiveMaximum, ToTime: pgtype.Timestamptz{Time: request.ToExclusive, Valid: !request.ToExclusive.IsZero()},
		PageOffset: int32(request.Page-1) * SearchPageSize,
	}
	rows, err := querier.SearchDiscoveryPage(ctx, parameters)
	if err != nil {
		return SearchPage{}, fmt.Errorf("query search page: %w", err)
	}
	if len(rows) > SearchPageSize {
		return SearchPage{}, fmt.Errorf("search page exceeded row bound")
	}
	page := SearchPage{Results: make([]SearchResult, len(rows))}
	for index, row := range rows {
		ordinal := parameters.PageOffset + int32(index) + 1
		if row.ResultOrdinal != ordinal || row.ExpectedKind != row.ResultKind || row.ExpectedID != row.ResultID ||
			(row.ResultKind != "topic" && row.ResultKind != "post") || row.ResultID <= 0 || row.AreaID <= 0 ||
			!policy.ValidAreaSlug(row.AreaSlug) || row.AreaName == "" || row.TopicID <= 0 || row.TopicTitle == "" ||
			row.AuthorID <= 0 || row.AuthorName == "" || !validFiniteTimestamp(row.CreatedAt) ||
			math.IsNaN(float64(row.ResultRank)) || math.IsInf(float64(row.ResultRank), 0) || row.ResultRank < 0 ||
			index > 0 && (row.HasNextPage != rows[0].HasNextPage || row.BeyondWindow != rows[0].BeyondWindow) {
			return SearchPage{}, fmt.Errorf("search selected-row recheck failed")
		}
		result := SearchResult{
			Kind: row.ResultKind, ID: row.ResultID, AreaID: row.AreaID, AreaSlug: row.AreaSlug, AreaName: row.AreaName,
			TopicID: row.TopicID, TopicTitle: row.TopicTitle, AuthorID: row.AuthorID, AuthorName: row.AuthorName,
			CreatedAt: row.CreatedAt, Rank: row.ResultRank,
		}
		if row.ResultKind == "topic" {
			if row.PostID.Valid || row.RenderedHtml.Valid || row.ResultID != row.TopicID {
				return SearchPage{}, fmt.Errorf("search topic projection is malformed")
			}
		} else {
			if !row.PostID.Valid || row.PostID.Int64 != row.ResultID || !row.RenderedHtml.Valid {
				return SearchPage{}, fmt.Errorf("search post projection is malformed")
			}
			result.PostID = row.PostID.Int64
			result.Excerpt = firstRunes(contentrender.SanitizeHTML(row.RenderedHtml.String).VisibleText(), MaximumExcerptRunes)
		}
		page.Results[index] = result
	}
	if len(rows) > 0 {
		page.HasNextPage = rows[0].HasNextPage
		page.BeyondWindow = rows[0].BeyondWindow
	}
	return page, nil
}

// activityWithQuerier validates one strict 26-row keyset result and strips the
// sentinel before exposing the continuation boundary.
//
// Complexity: with r <= 26, local time and returned space are O(r), Omega(1),
// and tight Theta(r) for nonempty pages; database work is delegated.
func activityWithQuerier(ctx context.Context, querier activityQuerier, boundary ActivityBoundary, actor policy.AccessContext) (ActivityPage, error) {
	if ctx == nil || querier == nil || !actor.Valid() || boundary.PostID < 0 || boundary.PostID == 0 && !boundary.CreatedAt.IsZero() || boundary.PostID > 0 && !validMicrosecondTime(boundary.CreatedAt) {
		return ActivityPage{}, fmt.Errorf("activity input is invalid")
	}
	parameters := activityQueryParameters{
		IsStaff:  actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator,
		IsMember: actor.Authenticated, GroupIDs: actor.GroupIDs, Boundary: boundary,
	}
	rows, err := querier.ListRecentActivity(ctx, parameters)
	if err != nil {
		return ActivityPage{}, fmt.Errorf("query recent activity: %w", err)
	}
	if len(rows) > ActivityQueryLimit {
		return ActivityPage{}, fmt.Errorf("activity query exceeded row bound")
	}
	visibleCount := len(rows)
	if visibleCount > ActivityPageSize {
		visibleCount = ActivityPageSize
	}
	page := ActivityPage{Results: make([]ActivityResult, visibleCount)}
	for index, row := range rows {
		if !validActivityRow(row) || index > 0 && !strictlyAfterActivity(rows[index-1], row) {
			return ActivityPage{}, fmt.Errorf("activity query returned malformed order")
		}
		if index < visibleCount {
			page.Results[index] = ActivityResult(row)
		}
	}
	if len(rows) == ActivityQueryLimit {
		boundaryRow := rows[ActivityPageSize-1]
		page.NextBoundary = &ActivityBoundary{CreatedAt: boundaryRow.CreatedAt.Time.UTC(), PostID: boundaryRow.PostID}
	}
	return page, nil
}

// GetDirectPost performs exactly one primary-key-started authorized query and
// rejects malformed persisted data before producing sanitized presentation.
//
// Complexity: for h bounded HTML bytes, local time and auxiliary/output space
// are O(h), Omega(1), and tight Theta(h); database work is delegated.
func GetDirectPost(ctx context.Context, querier directPostQuerier, postID int64, actor policy.AccessContext) (DirectPost, error) {
	if ctx == nil || querier == nil || postID <= 0 || !actor.Valid() {
		return DirectPost{}, fmt.Errorf("direct post input is invalid")
	}
	row, err := querier.GetDirectPost(ctx, db.GetDirectPostParams{
		PostID: postID, IsStaff: actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator,
		IsMember: actor.Authenticated, GroupIds: actor.GroupIDs,
	})
	if err != nil {
		return DirectPost{}, fmt.Errorf("query direct post: %w", err)
	}
	if row.PostID != postID || row.AuthorID <= 0 || row.AuthorName == "" || row.TopicID <= 0 || row.TopicTitle == "" ||
		row.AreaID <= 0 || !policy.ValidAreaSlug(row.AreaSlug) || row.AreaName == "" || row.Revision <= 0 ||
		row.RenderedHtml == "" || row.RendererVersion == "" || !validFiniteTimestamp(row.CreatedAt) || !validFiniteTimestamp(row.UpdatedAt) ||
		row.UpdatedAt.Time.Before(row.CreatedAt.Time) || row.Revision == 1 && row.EditedAt.Valid ||
		row.Revision > 1 && (!validFiniteTimestamp(row.EditedAt) || row.EditedAt.Time.Before(row.CreatedAt.Time)) {
		return DirectPost{}, fmt.Errorf("direct post query returned malformed row")
	}
	body := contentrender.SanitizeHTML(row.RenderedHtml)
	return DirectPost{
		PostID: row.PostID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, EditedAt: row.EditedAt, Revision: row.Revision,
		Body: body, VisibleText: body.VisibleText(), RendererVersion: row.RendererVersion,
		AuthorID: row.AuthorID, AuthorName: row.AuthorName, TopicID: row.TopicID, TopicTitle: row.TopicTitle,
		AreaID: row.AreaID, AreaSlug: row.AreaSlug, AreaName: row.AreaName,
	}, nil
}

// validSearchRequest revalidates the closed parsed-request representation at
// the store boundary.
//
// Complexity: for q query bytes, time is O(q), Omega(1), and tight Theta(q)
// when normalization is checked; auxiliary space is O(q).
func validSearchRequest(request SearchRequest) bool {
	if request.Page < 1 || request.Page > 2 || request.Query == "" && request.AuthorID <= 0 || request.AuthorID < 0 ||
		request.Query != "" && (len(request.Query) > MaximumSearchQueryBytes || !utf8.ValidString(request.Query) || norm.NFC.String(request.Query) != request.Query || trimUnicode15WhiteSpace(request.Query) != request.Query) ||
		request.AreaSlug != "" && !policy.ValidAreaSlug(request.AreaSlug) ||
		!request.From.IsZero() && !validMicrosecondTime(request.From) || !request.ToExclusive.IsZero() && !validMicrosecondTime(request.ToExclusive) {
		return false
	}
	if request.ToInclusiveMaximum != request.ToExclusive.Equal(maximumSearchTime) {
		return false
	}
	if !request.From.IsZero() && !request.ToExclusive.IsZero() {
		upper := request.ToExclusive
		if request.ToInclusiveMaximum {
			upper = upper.Add(1_000)
		}
		return request.From.Before(upper)
	}
	return true
}

// validFiniteTimestamp accepts only present finite UTC microsecond values.
//
// Complexity: time and auxiliary space are tight Theta(1).
func validFiniteTimestamp(value pgtype.Timestamptz) bool {
	return value.Valid && value.InfinityModifier == pgtype.Finite && value.Time.Year() >= 1 && value.Time.Year() <= 9999 && value.Time.Nanosecond()%1_000 == 0
}

// validActivityRow checks one fully joined recent-activity identity.
//
// Complexity: time and auxiliary space are tight Theta(1).
func validActivityRow(row activityQueryRow) bool {
	return row.PostID > 0 && validFiniteTimestamp(row.CreatedAt) && row.AuthorID > 0 && row.AuthorName != "" &&
		row.TopicID > 0 && row.TopicTitle != "" && row.AreaID > 0 && policy.ValidAreaSlug(row.AreaSlug) && row.AreaName != ""
}

// strictlyAfterActivity enforces descending tuple order without arithmetic.
//
// Complexity: time and auxiliary space are tight Theta(1).
func strictlyAfterActivity(previous, current activityQueryRow) bool {
	return current.CreatedAt.Time.Before(previous.CreatedAt.Time) || current.CreatedAt.Time.Equal(previous.CreatedAt.Time) && current.PostID < previous.PostID
}

// firstRunes returns at most limit Unicode scalar values without splitting
// UTF-8 encoding.
//
// Complexity: for n bytes through the limit, time is O(n), Omega(1), and tight
// Theta(n) when truncation is needed; auxiliary space is O(n) for the result.
func firstRunes(value string, limit int) string {
	count := 0
	for index := range value {
		if count == limit {
			return value[:index]
		}
		count++
	}
	return value
}
