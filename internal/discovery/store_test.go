package discovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestSearchWithQuerierValidatesSemanticQueryAndRows(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	fake := &searchQuerierStub{
		parsed: db.ParseDiscoveryQueryRow{ParsedQuery: "'needle'", NodeCount: 1, PositiveQuery: "'needle'"},
		rows: []db.SearchDiscoveryPageRow{
			searchRow("topic", 11, 1, now),
			searchRow("post", 12, 2, now.Add(-time.Second)),
		},
	}
	fake.rows[1].PostID = pgtype.Int8{Int64: 12, Valid: true}
	fake.rows[1].RenderedHtml = pgtype.Text{String: "<p>Hello <strong>needle</strong></p><script>secret</script>", Valid: true}
	page, err := searchWithQuerier(context.Background(), fake, SearchRequest{Query: "needle", Page: 1}, policy.AccessContext{})
	if err != nil {
		t.Fatalf("searchWithQuerier() returned error: %v", err)
	}
	if len(page.Results) != 2 || page.Results[1].Excerpt != "Hello needle" || fake.parameters.ParsedQuery != "'needle'" {
		t.Fatalf("page = %+v; params = %+v", page, fake.parameters)
	}

	fake.parsed.PositiveQuery = "T"
	if _, err := searchWithQuerier(context.Background(), fake, SearchRequest{Query: "-needle", Page: 1}, policy.AccessContext{}); !errors.Is(err, ErrInvalidSearchQuery) {
		t.Fatalf("no-positive query error = %v", err)
	}
	fake.parsed = db.ParseDiscoveryQueryRow{ParsedQuery: "'x'", NodeCount: 32, PositiveQuery: "'x'"}
	if _, err := searchWithQuerier(context.Background(), fake, SearchRequest{Query: "x", Page: 1}, policy.AccessContext{}); !errors.Is(err, ErrInvalidSearchQuery) {
		t.Fatalf("over-node query error = %v", err)
	}
}

func TestSearchWithQuerierFailsClosedOnSelectedDrift(t *testing.T) {
	t.Parallel()

	row := searchRow("topic", 11, 1, time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC))
	row.ResultID = 0
	if page, err := searchWithQuerier(context.Background(), &searchQuerierStub{rows: []db.SearchDiscoveryPageRow{row}}, SearchRequest{AuthorID: 7, Page: 1}, policy.AccessContext{}); err == nil {
		t.Fatalf("searchWithQuerier() = %+v, want error", page)
	}
}

func TestActivityWithQuerierUsesStrictBoundaryAndSentinel(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	rows := make([]activityQueryRow, ActivityQueryLimit)
	for index := range rows {
		rows[index] = activityRow(int64(100-index), start.Add(-time.Duration(index)*time.Second))
	}
	fake := &activityQuerierStub{rows: rows}
	page, err := activityWithQuerier(context.Background(), fake, ActivityBoundary{CreatedAt: start.Add(time.Second), PostID: 101}, policy.AccessContext{})
	if err != nil {
		t.Fatalf("activityWithQuerier() returned error: %v", err)
	}
	if len(page.Results) != ActivityPageSize || page.NextBoundary == nil || page.NextBoundary.PostID != rows[ActivityPageSize-1].PostID || fake.parameters.Boundary.PostID == 0 {
		t.Fatalf("page = %+v; params = %+v", page, fake.parameters)
	}
	bad := rows[:2]
	bad[1].CreatedAt = bad[0].CreatedAt
	bad[1].PostID = bad[0].PostID + 1
	if _, err := activityWithQuerier(context.Background(), &activityQuerierStub{rows: bad}, ActivityBoundary{}, policy.AccessContext{}); err == nil {
		t.Fatal("activityWithQuerier() accepted non-descending rows")
	}
}

func TestGetDirectPostFailsClosedAndSanitizes(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	row := db.GetDirectPostRow{
		PostID: 9, CreatedAt: finiteTime(now), UpdatedAt: finiteTime(now), Revision: 1,
		RenderedHtml: "<p>safe</p><script>bad</script>", RendererVersion: "renderer-v1", AuthorID: 2, AuthorName: "Author",
		TopicID: 3, TopicTitle: "Topic", AreaID: 4, AreaSlug: "area", AreaName: "Area",
	}
	got, err := GetDirectPost(context.Background(), directPostQuerierStub{row: row}, 9, policy.AccessContext{})
	if err != nil || got.PostID != 9 || got.VisibleText != "safe" {
		t.Fatalf("GetDirectPost() = %+v, %v", got, err)
	}
	row.PostID = 10
	if got, err := GetDirectPost(context.Background(), directPostQuerierStub{row: row}, 9, policy.AccessContext{}); err == nil {
		t.Fatalf("GetDirectPost() = %+v, want error", got)
	}
}

type searchQuerierStub struct {
	parsed     db.ParseDiscoveryQueryRow
	rows       []db.SearchDiscoveryPageRow
	parameters db.SearchDiscoveryPageParams
}

func (stub *searchQuerierStub) ParseDiscoveryQuery(context.Context, string) (db.ParseDiscoveryQueryRow, error) {
	return stub.parsed, nil
}
func (stub *searchQuerierStub) SearchDiscoveryPage(_ context.Context, parameters db.SearchDiscoveryPageParams) ([]db.SearchDiscoveryPageRow, error) {
	stub.parameters = parameters
	return stub.rows, nil
}

type activityQuerierStub struct {
	rows       []activityQueryRow
	parameters activityQueryParameters
}

func (stub *activityQuerierStub) ListRecentActivity(_ context.Context, parameters activityQueryParameters) ([]activityQueryRow, error) {
	stub.parameters = parameters
	return stub.rows, nil
}

type directPostQuerierStub struct{ row db.GetDirectPostRow }

func (stub directPostQuerierStub) GetDirectPost(context.Context, db.GetDirectPostParams) (db.GetDirectPostRow, error) {
	return stub.row, nil
}

func searchRow(kind string, id int64, ordinal int32, createdAt time.Time) db.SearchDiscoveryPageRow {
	row := db.SearchDiscoveryPageRow{
		ExpectedKind: kind, ExpectedID: id, ResultOrdinal: ordinal, ResultKind: kind, ResultID: id,
		AreaID: 4, AreaSlug: "area", AreaName: "Area", TopicID: 3, TopicTitle: "Topic",
		AuthorID: 2, AuthorName: "Author", CreatedAt: finiteTime(createdAt),
	}
	if kind == "topic" {
		row.TopicID = id
	}
	return row
}

func activityRow(id int64, createdAt time.Time) activityQueryRow {
	return activityQueryRow{
		PostID: id, CreatedAt: finiteTime(createdAt), AuthorID: 2, AuthorName: "Author",
		TopicID: 3, TopicTitle: "Topic", AreaID: 4, AreaSlug: "area", AreaName: "Area",
	}
}

func finiteTime(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true, InfinityModifier: pgtype.Finite}
}
