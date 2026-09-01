package db

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestListVisibleAreasBindsAccessFactsAndScansEveryArea(t *testing.T) {
	t.Parallel()

	want := []Area{
		{ID: 3, Slug: "public", Name: "Public", Description: "Visible", DisplayOrder: 1, Visibility: "public", PostingMode: "normal", CreatedBy: 7, UpdatedBy: 8, CreatedAt: pgtype.Timestamptz{Valid: true}, UpdatedAt: pgtype.Timestamptz{Valid: true}},
		{ID: 5, Slug: "staff", Name: "Staff", Description: "Restricted", DisplayOrder: 2, Visibility: "groups", PostingMode: "read_only", CreatedBy: 7, UpdatedBy: 9, CreatedAt: pgtype.Timestamptz{Valid: true}, UpdatedAt: pgtype.Timestamptz{Valid: true}},
	}
	database := &areaListDBTX{rows: &areaListRows{areas: want}}
	got, err := New(database).ListVisibleAreas(context.Background(), ListVisibleAreasParams{
		IsStaff:  true,
		IsMember: true,
		GroupIds: []int64{11, 13},
	})
	if err != nil || len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ListVisibleAreas() = (%+v, %v), want %+v", got, err, want)
	}
	if database.queryCalls != 1 || len(database.args) != 3 || database.args[0] != true || database.args[1] != true {
		t.Fatalf("query binding = (calls %d, args %#v)", database.queryCalls, database.args)
	}
	groupIDs, ok := database.args[2].([]int64)
	if !ok || len(groupIDs) != 2 || groupIDs[0] != 11 || groupIDs[1] != 13 {
		t.Fatalf("group IDs = %#v", database.args[2])
	}
	if database.rows.closeCalls != 1 {
		t.Fatalf("rows close calls = %d, want one", database.rows.closeCalls)
	}
}

func TestListVisibleAreasPreservesQueryScanAndRowsFailures(t *testing.T) {
	t.Parallel()

	cause := errors.New("area list failed")
	for _, test := range []struct {
		name     string
		database *areaListDBTX
	}{
		{name: "query", database: &areaListDBTX{queryErr: cause}},
		{name: "scan", database: &areaListDBTX{rows: &areaListRows{areas: []Area{{ID: 1}}, scanErr: cause}}},
		{name: "rows", database: &areaListDBTX{rows: &areaListRows{rowsErr: cause}}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := New(test.database).ListVisibleAreas(context.Background(), ListVisibleAreasParams{})
			if !errors.Is(err, cause) || len(got) != 0 {
				t.Fatalf("ListVisibleAreas() = (%+v, %v), want empty/cause", got, err)
			}
		})
	}
}

type areaListDBTX struct {
	DBTX
	rows       *areaListRows
	queryErr   error
	queryCalls int
	args       []any
}

func (database *areaListDBTX) Query(_ context.Context, _ string, args ...interface{}) (pgx.Rows, error) {
	database.queryCalls++
	database.args = args
	if database.queryErr != nil {
		return nil, database.queryErr
	}
	return database.rows, nil
}

type areaListRows struct {
	pgx.Rows
	areas      []Area
	index      int
	scanErr    error
	rowsErr    error
	closeCalls int
}

func (rows *areaListRows) Close() { rows.closeCalls++ }

func (rows *areaListRows) Next() bool {
	return rows.index < len(rows.areas)
}

func (rows *areaListRows) Scan(destinations ...any) error {
	if rows.scanErr != nil {
		return rows.scanErr
	}
	area := rows.areas[rows.index]
	rows.index++
	*(destinations[0].(*int64)) = area.ID
	*(destinations[1].(*string)) = area.Slug
	*(destinations[2].(*string)) = area.Name
	*(destinations[3].(*string)) = area.Description
	*(destinations[4].(*int32)) = area.DisplayOrder
	*(destinations[5].(*string)) = area.Visibility
	*(destinations[6].(*string)) = area.PostingMode
	*(destinations[7].(*int64)) = area.CreatedBy
	*(destinations[8].(*int64)) = area.UpdatedBy
	*(destinations[9].(*pgtype.Timestamptz)) = area.CreatedAt
	*(destinations[10].(*pgtype.Timestamptz)) = area.UpdatedAt
	return nil
}

func (rows *areaListRows) Err() error { return rows.rowsErr }
