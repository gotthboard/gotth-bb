package readiness

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	contentrender "github.com/gotthboard/gotth-bb/internal/render"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type readinessRow struct {
	valid bool
	err   error
	query string
}

func (row readinessRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if row.query == siteStateQuery {
		*(destinations[0].(*string)) = "GOTTH Board"
		*(destinations[1].(*string)) = "Community discussions, plainly organized."
		*(destinations[2].(*string)) = "blue"
		*(destinations[3].(*string)) = ""
		*(destinations[4].(*string)) = ""
		*(destinations[5].(*string)) = contentrender.RendererVersion
		*(destinations[6].(*int64)) = 1
		*(destinations[7].(*pgtype.Timestamptz)) = pgtype.Timestamptz{Time: time.Now(), Valid: true}
		return nil
	}
	*(destinations[0].(*bool)) = row.valid
	return nil
}

const siteStateQuery = `SELECT site_name, site_description, brand_theme,
       rules_markdown, rules_html, rules_renderer_version,
       administration_revision, updated_at
FROM public.site_settings
WHERE singleton
  AND (SELECT count(*) FROM public.site_settings) = 1`

type readinessDatabase struct {
	row          pgx.Row
	called       bool
	query        string
	arguments    []any
	queries      []string
	allArguments [][]any
}

func (database *readinessDatabase) QueryRow(_ context.Context, query string, arguments ...any) pgx.Row {
	database.called = true
	database.query = query
	database.arguments = arguments
	database.queries = append(database.queries, query)
	database.allArguments = append(database.allArguments, append([]any(nil), arguments...))
	if row, ok := database.row.(readinessRow); ok {
		row.query = query
		return row
	}
	return database.row
}

func TestCheckerAcceptsExactReleaseAndGovernanceState(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.FixedZone("test", -5*60*60))
	database := &readinessDatabase{row: readinessRow{valid: true}}
	migrationCalls := 0
	checker, err := New(database, func(ctx context.Context) error {
		migrationCalls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("migration verifier received no deadline")
		}
		return nil
	}, func() time.Time { return observedAt })
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if err := checker.Check(context.Background()); err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
	if migrationCalls != 1 || !database.called || len(database.queries) != 6 || database.queries[0] != governanceInvariantSQL {
		t.Fatalf("calls = (migrations %d, database %t, queries %d)", migrationCalls, database.called, len(database.queries))
	}
	arguments := database.allArguments[0]
	if len(arguments) != 5 || arguments[0] != observedAt.UTC() || arguments[1] != contentrender.RendererVersion ||
		arguments[2] != rendererConstraintDefinition || arguments[3] != renderedSizeConstraintDefinition ||
		arguments[4] != rendererCursorConstraintDefinition {
		t.Fatalf("query arguments = %+v, want UTC observation time, renderer version, and exact constraint definitions", arguments)
	}
	if want := fmt.Sprintf("CHECK ((octet_length(rendered_html) <= %d))", contentrender.MaximumRenderedHTMLBytes); renderedSizeConstraintDefinition != want {
		t.Fatalf("rendered-size readiness definition = %q, want %q", renderedSizeConstraintDefinition, want)
	}
}

func TestCheckerFailsClosed(t *testing.T) {
	t.Parallel()

	failure := errors.New("failure")
	validDatabase := &readinessDatabase{row: readinessRow{valid: true}}
	validVerifier := func(context.Context) error { return nil }
	validClock := func() time.Time { return time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC) }
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name    string
		checker *Checker
		ctx     context.Context
		cause   error
		queried bool
	}{
		{name: "nil checker", ctx: context.Background()},
		{name: "incomplete checker", checker: &Checker{}, ctx: context.Background()},
		{name: "nil context", checker: &Checker{database: validDatabase, verifyMigrations: validVerifier, now: validClock}},
		{name: "canceled", checker: &Checker{database: validDatabase, verifyMigrations: validVerifier, now: validClock}, ctx: canceled, cause: context.Canceled},
		{name: "migration failure", checker: &Checker{database: &readinessDatabase{row: readinessRow{valid: true}}, verifyMigrations: func(context.Context) error { return failure }, now: validClock}, ctx: context.Background(), cause: failure},
		{name: "zero clock", checker: &Checker{database: &readinessDatabase{row: readinessRow{valid: true}}, verifyMigrations: validVerifier, now: func() time.Time { return time.Time{} }}, ctx: context.Background()},
		{name: "query failure", checker: &Checker{database: &readinessDatabase{row: readinessRow{err: failure}}, verifyMigrations: validVerifier, now: validClock}, ctx: context.Background(), cause: failure},
		{name: "invalid governance", checker: &Checker{database: &readinessDatabase{row: readinessRow{valid: false}}, verifyMigrations: validVerifier, now: validClock}, ctx: context.Background()},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.checker.Check(test.ctx); err == nil {
				t.Fatal("Check() returned nil error")
			} else if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("Check() error = %v, want cause %v", err, test.cause)
			}
		})
	}

	if checker, err := New(nil, validVerifier, validClock); err == nil || checker != nil {
		t.Fatalf("New(nil database) = (%v, %v), want nil/error", checker, err)
	}
	if checker, err := New(validDatabase, nil, validClock); err == nil || checker != nil {
		t.Fatalf("New(nil verifier) = (%v, %v), want nil/error", checker, err)
	}
	if checker, err := New(validDatabase, validVerifier, nil); err == nil || checker != nil {
		t.Fatalf("New(nil clock) = (%v, %v), want nil/error", checker, err)
	}
}
