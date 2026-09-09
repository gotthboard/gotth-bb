package control

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

type readinessRow struct {
	query        string
	valid        bool
	registration string
	publishLimit int32
	err          error
}

func (row readinessRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if row.query == controlStateReadySQL {
		*(destinations[0].(*string)) = row.registration
		*(destinations[1].(*bool)) = false
		*(destinations[2].(*string)) = ""
		*(destinations[3].(*int32)) = row.publishLimit
		*(destinations[4].(*int32)) = 3
		*(destinations[5].(*int32)) = 600
		*(destinations[6].(*int32)) = 86400
		*(destinations[7].(*int32)) = 28800
		*(destinations[8].(*int32)) = 1800
		*(destinations[9].(*int64)) = 1
		return nil
	}
	*(destinations[0].(*bool)) = row.valid
	return nil
}

type readinessStub struct {
	valid        bool
	registration string
	publishLimit int32
	errAt        int
	calls        int
}

func (stub *readinessStub) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	stub.calls++
	row := readinessRow{query: query, valid: stub.valid, registration: stub.registration, publishLimit: stub.publishLimit}
	if stub.calls == stub.errAt {
		row.err = errors.New("database failure")
	}
	return row
}

func TestReadyAcceptsClosedSettingsAndExactBoundary(t *testing.T) {
	t.Parallel()

	stub := &readinessStub{valid: true, registration: "closed", publishLimit: 10}
	if err := Ready(context.Background(), stub, testCeilings(), false); err != nil {
		t.Fatalf("Ready() returned error: %v", err)
	}
	if stub.calls != 3 {
		t.Fatalf("Ready() query calls = %d, want 3", stub.calls)
	}
}

func TestReadyFailsClosedForCatalogStateSMTPAndPrivileges(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		stub readinessStub
		smtp bool
	}{
		{name: "catalog query", stub: readinessStub{valid: true, registration: "closed", publishLimit: 10, errAt: 1}},
		{name: "catalog false", stub: readinessStub{registration: "closed", publishLimit: 10}},
		{name: "state query", stub: readinessStub{valid: true, registration: "closed", publishLimit: 10, errAt: 2}},
		{name: "ceiling", stub: readinessStub{valid: true, registration: "closed", publishLimit: 11}},
		{name: "smtp", stub: readinessStub{valid: true, registration: "verified_email_open", publishLimit: 10}},
		{name: "privilege query", stub: readinessStub{valid: true, registration: "closed", publishLimit: 10, errAt: 3}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := Ready(context.Background(), &test.stub, testCeilings(), test.smtp); err == nil {
				t.Fatal("Ready() returned nil error")
			}
		})
	}
	if err := Ready(nil, &readinessStub{}, testCeilings(), false); err == nil {
		t.Fatal("Ready(nil context) returned nil")
	}
	if err := Ready(context.Background(), nil, testCeilings(), false); err == nil {
		t.Fatal("Ready(nil database) returned nil")
	}
	if err := Ready(context.Background(), &readinessStub{}, Ceilings{}, false); err == nil {
		t.Fatal("Ready(invalid ceilings) returned nil")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Ready(canceled, &readinessStub{}, testCeilings(), false); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ready(canceled) error = %v", err)
	}
}
