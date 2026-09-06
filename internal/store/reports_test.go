package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestListModerationReportsDerivesAuthorityAndMapsRows(t *testing.T) {
	t.Parallel()

	now := reportStoreTime(12)
	reader := &reportReaderStub{listRows: []db.ListActiveReportsForModerationRow{
		{
			ID: 1, Reason: "First reason", Status: "open", CreatedAt: reportStoreTimestamp(10),
			ReporterDisplayName: "Reporter", TargetType: "topic", TargetID: 41,
			TargetLabel: "Visible topic", TotalActive: 27,
		},
		{
			ID: 2, Reason: "Second reason", Status: "in_review", CreatedAt: reportStoreTimestamp(11),
			ReporterDisplayName: "Other", AssignedTo: pgtype.Int8{Int64: 9, Valid: true},
			AssigneeDisplayName: pgtype.Text{String: "Moderator", Valid: true},
			TargetType:          "post", TargetID: 52, TargetLabel: "Post topic", TotalActive: 27,
		},
	}}
	actor := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}
	got, err := ListModerationReports(context.Background(), reader, actor, 2, now)
	if err != nil || len(got.Reports) != 2 || got.Number != 2 || got.Total != 27 || got.TotalPages != 2 {
		t.Fatalf("ListModerationReports() = (%+v, %v)", got, err)
	}
	if got.Reports[0].TargetType != "topic" || got.Reports[0].Assignee != "" ||
		got.Reports[1].TargetType != "post" || got.Reports[1].Assignee != "Moderator" {
		t.Fatalf("mapped reports = %+v", got.Reports)
	}
	wantParameters := db.ListActiveReportsForModerationParams{
		ActorUserID: 7,
		ActorRole:   "administrator",
		ObservedAt:  pgtype.Timestamptz{Time: now, Valid: true},
		PageOffset:  ReportPageSize,
		PageLimit:   ReportPageSize,
	}
	if reader.listCalls != 1 || !reflect.DeepEqual(reader.listParameters, wantParameters) {
		t.Fatalf("list calls/parameters = (%d, %+v), want (1, %+v)", reader.listCalls, reader.listParameters, wantParameters)
	}
}

func TestListModerationReportsEmptyAndFailureBoundaries(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleModerator}
	now := reportStoreTime(12)
	first, err := ListModerationReports(context.Background(), &reportReaderStub{}, actor, 1, now)
	if err != nil || first.Number != 1 || first.Total != 0 || first.TotalPages != 0 || len(first.Reports) != 0 {
		t.Fatalf("empty first page = (%+v, %v)", first, err)
	}
	for _, test := range []struct {
		name   string
		ctx    context.Context
		reader reportReader
		actor  policy.AccessContext
		page   int32
		now    time.Time
		cause  error
	}{
		{name: "nil context", reader: &reportReaderStub{}, actor: actor, page: 1, now: now},
		{name: "nil reader", ctx: context.Background(), actor: actor, page: 1, now: now},
		{name: "invalid actor", ctx: context.Background(), reader: &reportReaderStub{}, page: 1, now: now},
		{name: "muted staff", ctx: context.Background(), reader: &reportReaderStub{}, actor: policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleModerator, MutedUntil: timePointer(now)}, page: 1, now: now, cause: pgx.ErrNoRows},
		{name: "page zero", ctx: context.Background(), reader: &reportReaderStub{}, actor: actor, now: now, cause: pgx.ErrNoRows},
		{name: "empty later page", ctx: context.Background(), reader: &reportReaderStub{}, actor: actor, page: 2, now: now, cause: pgx.ErrNoRows},
		{name: "query failure", ctx: context.Background(), reader: &reportReaderStub{listErr: context.Canceled}, actor: actor, page: 1, now: now, cause: context.Canceled},
		{name: "malformed row", ctx: context.Background(), reader: &reportReaderStub{listRows: []db.ListActiveReportsForModerationRow{{ID: -1}}}, actor: actor, page: 1, now: now},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, gotErr := ListModerationReports(test.ctx, test.reader, test.actor, test.page, test.now)
			if gotErr == nil || !reflect.DeepEqual(got, ModerationReportPage{}) || test.cause != nil && !errors.Is(gotErr, test.cause) {
				t.Fatalf("ListModerationReports() = (%+v, %v), want zero/error containing %v", got, gotErr, test.cause)
			}
		})
	}
}

func TestGetModerationReportMapsEmptyAndPopulatedNotes(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleModerator}
	now := reportStoreTime(12)
	for _, test := range []struct {
		name       string
		row        db.GetReportForModerationRow
		notes      []db.ListReportNotesRow
		wantNotes  int
		wantStatus string
	}{
		{
			name: "open without notes",
			row: db.GetReportForModerationRow{
				ID: 41, ReportedBy: 5, ReporterDisplayName: "Reporter", Reason: "Reason", Status: "open",
				CreatedAt: reportStoreTimestamp(9), UpdatedAt: reportStoreTimestamp(10),
				TargetType: "topic", TargetID: 90, TargetTopicID: 90, TargetLabel: "Topic",
			},
			notes:      []db.ListReportNotesRow{{AuthorizedReportID: 41}},
			wantStatus: "open",
		},
		{
			name: "resolved with note",
			row: db.GetReportForModerationRow{
				ID: 41, ReportedBy: 5, ReporterDisplayName: "Reporter", Reason: "Reason", Status: "resolved",
				AssignedTo: pgtype.Int8{Int64: 7, Valid: true}, AssigneeDisplayName: pgtype.Text{String: "Moderator", Valid: true},
				Resolution: pgtype.Text{String: "Handled", Valid: true}, ResolvedBy: pgtype.Int8{Int64: 7, Valid: true},
				ResolverDisplayName: pgtype.Text{String: "Moderator", Valid: true}, ResolvedAt: reportStoreTimestamp(11),
				CreatedAt: reportStoreTimestamp(9), UpdatedAt: reportStoreTimestamp(11),
				TargetType: "user", TargetID: 90, TargetLabel: "Target user",
			},
			notes: []db.ListReportNotesRow{{
				AuthorizedReportID: 41, ID: pgtype.Int8{Int64: 2, Valid: true},
				AuthorID: pgtype.Int8{Int64: 7, Valid: true}, AuthorDisplayName: pgtype.Text{String: "Moderator", Valid: true},
				Body: pgtype.Text{String: "Private note", Valid: true}, CreatedAt: reportStoreTimestamp(10),
			}},
			wantNotes: 1, wantStatus: "resolved",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reader := &reportReaderStub{detailRow: test.row, noteRows: test.notes}
			got, err := GetModerationReport(context.Background(), reader, actor, 41, now)
			if err != nil || got.ID != 41 || got.Status != test.wantStatus || len(got.Notes) != test.wantNotes {
				t.Fatalf("GetModerationReport() = (%+v, %v)", got, err)
			}
			if reader.detailCalls != 1 || reader.noteCalls != 1 || reader.detailParameters.ActorRole != "moderator" || reader.noteParameters.ActorRole != "moderator" || !reader.detailParameters.ObservedAt.Time.Equal(now) || !reader.noteParameters.ObservedAt.Time.Equal(now) {
				t.Fatalf("detail parameters = (%+v, %+v)", reader.detailParameters, reader.noteParameters)
			}
		})
	}
}

func TestGetModerationReportFailsClosedOnRevocationAndMalformedRows(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}
	now := reportStoreTime(12)
	valid := db.GetReportForModerationRow{
		ID: 41, ReportedBy: 5, ReporterDisplayName: "Reporter", Reason: "Reason", Status: "open",
		CreatedAt: reportStoreTimestamp(9), UpdatedAt: reportStoreTimestamp(10),
		TargetType: "post", TargetID: 91, TargetTopicID: 90, TargetPage: 1, TargetLabel: "Topic",
	}
	for _, test := range []struct {
		name   string
		reader reportReader
		cause  error
	}{
		{name: "detail failure", reader: &reportReaderStub{detailErr: context.DeadlineExceeded}, cause: context.DeadlineExceeded},
		{name: "malformed detail", reader: &reportReaderStub{detailRow: db.GetReportForModerationRow{ID: -1}}},
		{name: "notes failure", reader: &reportReaderStub{detailRow: valid, noteErr: context.Canceled}, cause: context.Canceled},
		{name: "authority revoked between reads", reader: &reportReaderStub{detailRow: valid}, cause: pgx.ErrNoRows},
		{name: "wrong authorization sentinel", reader: &reportReaderStub{detailRow: valid, noteRows: []db.ListReportNotesRow{{AuthorizedReportID: 42}}}},
		{name: "malformed empty sentinel", reader: &reportReaderStub{detailRow: valid, noteRows: []db.ListReportNotesRow{{AuthorizedReportID: 41, ID: pgtype.Int8{Int64: 2, Valid: true}}}}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := GetModerationReport(context.Background(), test.reader, actor, 41, now)
			if err == nil || !reflect.DeepEqual(got, ModerationReportDetail{}) || test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("GetModerationReport() = (%+v, %v), want zero/error containing %v", got, err, test.cause)
			}
		})
	}
	if got, err := GetModerationReport(nil, &reportReaderStub{}, actor, 41, now); err == nil || !reflect.DeepEqual(got, ModerationReportDetail{}) {
		t.Fatalf("nil context = (%+v, %v)", got, err)
	}
	if got, err := GetModerationReport(context.Background(), nil, actor, 41, now); err == nil || !reflect.DeepEqual(got, ModerationReportDetail{}) {
		t.Fatalf("nil reader = (%+v, %v)", got, err)
	}
	if got, err := GetModerationReport(context.Background(), &reportReaderStub{}, policy.AccessContext{}, 41, now); !errors.Is(err, pgx.ErrNoRows) || !reflect.DeepEqual(got, ModerationReportDetail{}) {
		t.Fatalf("unauthorized actor = (%+v, %v)", got, err)
	}
}

type reportReaderStub struct {
	listRows         []db.ListActiveReportsForModerationRow
	listErr          error
	detailRow        db.GetReportForModerationRow
	detailErr        error
	noteRows         []db.ListReportNotesRow
	noteErr          error
	listCalls        int
	detailCalls      int
	noteCalls        int
	listParameters   db.ListActiveReportsForModerationParams
	detailParameters db.GetReportForModerationParams
	noteParameters   db.ListReportNotesParams
}

func (stub *reportReaderStub) ListActiveReportsForModeration(_ context.Context, parameters db.ListActiveReportsForModerationParams) ([]db.ListActiveReportsForModerationRow, error) {
	stub.listCalls++
	stub.listParameters = parameters
	return stub.listRows, stub.listErr
}

func (stub *reportReaderStub) GetReportForModeration(_ context.Context, parameters db.GetReportForModerationParams) (db.GetReportForModerationRow, error) {
	stub.detailCalls++
	stub.detailParameters = parameters
	return stub.detailRow, stub.detailErr
}

func (stub *reportReaderStub) ListReportNotes(_ context.Context, parameters db.ListReportNotesParams) ([]db.ListReportNotesRow, error) {
	stub.noteCalls++
	stub.noteParameters = parameters
	return stub.noteRows, stub.noteErr
}

func reportStoreTime(hour int) time.Time {
	return time.Date(2026, time.September, 6, hour, 0, 0, 0, time.UTC)
}

func reportStoreTimestamp(hour int) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: reportStoreTime(hour), Valid: true}
}

func timePointer(value time.Time) *time.Time {
	return &value
}
