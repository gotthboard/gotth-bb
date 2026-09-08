package administration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
)

type completionReadStub struct {
	areas []db.ListAreasForAdministrationPageRow
}

func (stub completionReadStub) ListAreasForAdministrationPage(context.Context, db.ListAreasForAdministrationPageParams) ([]db.ListAreasForAdministrationPageRow, error) {
	return stub.areas, nil
}
func (completionReadStub) LoadAreaForAdministrationPage(context.Context, db.LoadAreaForAdministrationPageParams) (db.LoadAreaForAdministrationPageRow, error) {
	panic("unexpected area load")
}
func (completionReadStub) ListAreaGroupsForAdministrationPage(context.Context, db.ListAreaGroupsForAdministrationPageParams) ([]db.ListAreaGroupsForAdministrationPageRow, error) {
	panic("unexpected group load")
}

func TestListAreaPageUsesDisplayOrderAndIDSentinel(t *testing.T) {
	t.Parallel()
	rows := make([]db.ListAreasForAdministrationPageRow, 26)
	for index := range rows {
		order := int32(index / 4)
		rows[index] = db.ListAreasForAdministrationPageRow{AreaPresent: true, ID: int64(index + 10), Slug: "area-" + string(rune('a'+index)), Name: "Area", DisplayOrder: order, Visibility: string(policy.VisibilityPublic), PostingMode: string(policy.PostingNormal), AdministrationRevision: 1}
	}
	page, err := ListAreaPage(context.Background(), completionReadStub{areas: rows}, policy.AccessContext{Authenticated: true, UserID: 1, Role: policy.RoleAdministrator}, time.Now(), 0, 0)
	if err != nil || len(page.Areas) != 25 || page.NextAfterOrder != rows[24].DisplayOrder || page.NextAfterID != rows[24].ID {
		t.Fatalf("ListAreaPage() = (%+v,%v)", page, err)
	}
	if _, err := ListAreaPage(context.Background(), completionReadStub{areas: append(rows, rows[25])}, policy.AccessContext{Authenticated: true, UserID: 1, Role: policy.RoleAdministrator}, time.Now(), 0, 0); !errors.Is(err, ErrAdministrationUnavailable) {
		t.Fatalf("oversized area page error = %v", err)
	}
}

func TestListAreaPageAcceptsZeroOrderPositiveIDCursor(t *testing.T) {
	t.Parallel()
	page, err := ListAreaPage(context.Background(), completionReadStub{areas: []db.ListAreasForAdministrationPageRow{{AreaPresent: false}}}, policy.AccessContext{Authenticated: true, UserID: 1, Role: policy.RoleAdministrator}, time.Now(), 0, 9)
	if err != nil || len(page.Areas) != 0 {
		t.Fatalf("zero-order cursor page = (%+v,%v)", page, err)
	}
}
