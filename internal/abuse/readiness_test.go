package abuse

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestPublicationReadyAttestsCatalogThenPrivileges(t *testing.T) {
	t.Parallel()
	cause := errors.New("forced readiness failure")
	for _, test := range []struct {
		name      string
		rows      []publicationReadinessRow
		wantError string
	}{
		{name: "ready", rows: []publicationReadinessRow{{valid: true}, {valid: true}}},
		{name: "catalog query", rows: []publicationReadinessRow{{err: cause}}, wantError: "query publication catalog readiness"},
		{name: "catalog drift", rows: []publicationReadinessRow{{valid: false}}, wantError: "publication catalog readiness failed"},
		{name: "privilege query", rows: []publicationReadinessRow{{valid: true}, {err: cause}}, wantError: "query publication privilege readiness"},
		{name: "privilege drift", rows: []publicationReadinessRow{{valid: true}, {valid: false}}, wantError: "publication privilege readiness failed"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			database := &publicationReadinessDatabase{rows: test.rows}
			err := PublicationReady(context.Background(), database)
			if test.wantError == "" {
				if err != nil || database.calls != 2 {
					t.Fatalf("PublicationReady() = (%v, calls %d)", err, database.calls)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("PublicationReady() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestPublicationReadyRejectsIncompleteOrCanceledBoundary(t *testing.T) {
	t.Parallel()
	if err := PublicationReady(nil, &publicationReadinessDatabase{}); err == nil {
		t.Fatal("PublicationReady(nil context) returned nil")
	}
	if err := PublicationReady(context.Background(), nil); err == nil {
		t.Fatal("PublicationReady(nil database) returned nil")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := PublicationReady(canceled, &publicationReadinessDatabase{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("PublicationReady(canceled) error = %v", err)
	}
}

type publicationReadinessDatabase struct {
	rows  []publicationReadinessRow
	calls int
}

func (database *publicationReadinessDatabase) QueryRow(context.Context, string, ...any) pgx.Row {
	if database.calls >= len(database.rows) {
		return publicationReadinessRow{err: errors.New("unexpected readiness query")}
	}
	row := database.rows[database.calls]
	database.calls++
	return row
}

type publicationReadinessRow struct {
	valid bool
	err   error
}

func (row publicationReadinessRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(destinations) != 1 {
		return errors.New("unexpected readiness destination count")
	}
	value, ok := destinations[0].(*bool)
	if !ok {
		return errors.New("unexpected readiness destination")
	}
	*value = row.valid
	return nil
}
