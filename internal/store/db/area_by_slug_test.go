package db

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestGetVisibleAreaBySlugBindsAccessFactsAndScansArea(t *testing.T) {
	t.Parallel()

	want := Area{
		ID: 3, Slug: "members", Name: "Members", Description: "Member area",
		DisplayOrder: 2, Visibility: "authenticated", PostingMode: "normal",
		CreatedBy: 7, UpdatedBy: 8,
		CreatedAt: pgtype.Timestamptz{Valid: true}, UpdatedAt: pgtype.Timestamptz{Valid: true},
	}
	ctx := context.WithValue(context.Background(), areaBySlugContextKey{}, "preserved")
	database := &areaBySlugDBTX{row: areaBySlugRow{area: want}}

	got, err := New(database).GetVisibleAreaBySlug(ctx, GetVisibleAreaBySlugParams{
		Slug: "members", IsStaff: false, IsMember: true, GroupIds: []int64{11, 13},
	})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("GetVisibleAreaBySlug() = (%+v, %v), want (%+v, nil)", got, err, want)
	}
	if database.ctx != ctx || len(database.args) != 4 || database.args[0] != "members" ||
		database.args[1] != false || database.args[2] != true ||
		!reflect.DeepEqual(database.args[3], []int64{11, 13}) {
		t.Fatalf("query call = (context %v, args %#v)", database.ctx, database.args)
	}
	for _, required := range []string{
		"a.slug = $1",
		"a.visibility = 'public'",
		"a.visibility = 'authenticated'",
		"a.visibility = 'groups'",
		"COALESCE(cardinality($4::bigint[]), 0) > 0",
		"ag.group_id = ANY($4::bigint[])",
	} {
		if !strings.Contains(database.query, required) {
			t.Fatalf("area-by-slug query lacks %q", required)
		}
	}
}

func TestGetVisibleAreaBySlugReturnsScanFailure(t *testing.T) {
	t.Parallel()

	cause := errors.New("scan failed")
	database := &areaBySlugDBTX{row: areaBySlugRow{err: cause}}
	got, err := New(database).GetVisibleAreaBySlug(context.Background(), GetVisibleAreaBySlugParams{})
	if !errors.Is(err, cause) || !reflect.DeepEqual(got, Area{}) {
		t.Fatalf("GetVisibleAreaBySlug() = (%+v, %v), want zero/cause", got, err)
	}
}

type areaBySlugContextKey struct{}

type areaBySlugDBTX struct {
	DBTX
	ctx   context.Context
	query string
	args  []any
	row   pgx.Row
}

func (database *areaBySlugDBTX) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	database.ctx = ctx
	database.query = query
	database.args = append([]any(nil), args...)
	return database.row
}

type areaBySlugRow struct {
	area Area
	err  error
}

func (row areaBySlugRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	*(destinations[0].(*int64)) = row.area.ID
	*(destinations[1].(*string)) = row.area.Slug
	*(destinations[2].(*string)) = row.area.Name
	*(destinations[3].(*string)) = row.area.Description
	*(destinations[4].(*int32)) = row.area.DisplayOrder
	*(destinations[5].(*string)) = row.area.Visibility
	*(destinations[6].(*string)) = row.area.PostingMode
	*(destinations[7].(*int64)) = row.area.CreatedBy
	*(destinations[8].(*int64)) = row.area.UpdatedBy
	*(destinations[9].(*pgtype.Timestamptz)) = row.area.CreatedAt
	*(destinations[10].(*pgtype.Timestamptz)) = row.area.UpdatedAt
	return nil
}
