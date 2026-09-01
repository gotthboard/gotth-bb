package migration

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
)

type stubOwnedReleaseConnection struct {
	*stubReleaseConnection
	closeCalled     bool
	closeContextErr error
	closeErr        error
}

func (connection *stubOwnedReleaseConnection) Close(ctx context.Context) error {
	connection.closeCalled = true
	connection.closeContextErr = ctx.Err()
	return connection.closeErr
}

func TestApplyWithConnectorOwnsAndClosesDedicatedConnection(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	releaseConnection := readyReleaseConnection(coordinatorQueryResult{rows: coordinatorAppliedRows(2)})
	releaseConnection.queryHook = cancel
	owned := &stubOwnedReleaseConnection{stubReleaseConnection: releaseConnection}
	configured := &pgx.ConnConfig{}
	connectorCalled := false
	connector := func(gotContext context.Context, gotConfig *pgx.ConnConfig) (ownedMigrationConnection, error) {
		connectorCalled = true
		if gotContext != ctx || gotConfig != configured {
			t.Fatalf("connector arguments = (%v, %p), want exact caller values", gotContext, gotConfig)
		}
		return owned, nil
	}
	if err := applyWithConnector(ctx, configured, coordinatorMigrationFS(), connector); err != nil {
		t.Fatalf("applyWithConnector() returned error: %v", err)
	}
	if !connectorCalled || !owned.closeCalled || owned.closeContextErr != nil {
		t.Fatalf("ownership = (connector %t, close %t, close context %v), want (true, true, nil)", connectorCalled, owned.closeCalled, owned.closeContextErr)
	}
}

func TestApplyWithConnectorRejectsInputsAndPreservesFailures(t *testing.T) {
	t.Parallel()

	connectFailure := errors.New("connect failure")
	closeFailure := errors.New("close failure")
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	connectCanceledContext, connectCancel := context.WithCancel(context.Background())
	validConfig := &pgx.ConnConfig{}
	validFS := coordinatorMigrationFS()
	inconsistentOwned := &stubOwnedReleaseConnection{stubReleaseConnection: readyReleaseConnection(), closeErr: closeFailure}
	unusedConnector := func(context.Context, *pgx.ConnConfig) (ownedMigrationConnection, error) {
		t.Fatal("connector called for invalid input")
		return nil, nil
	}
	tests := []struct {
		name       string
		ctx        context.Context
		config     *pgx.ConnConfig
		filesystem fstest.MapFS
		connector  migrationConnector
		wantCause  error
		wantSecond error
		owned      *stubOwnedReleaseConnection
	}{
		{name: "nil context", config: validConfig, filesystem: validFS, connector: unusedConnector},
		{name: "nil config", ctx: context.Background(), filesystem: validFS, connector: unusedConnector},
		{name: "nil connector", ctx: context.Background(), config: validConfig, filesystem: validFS},
		{name: "canceled context", ctx: canceledContext, config: validConfig, filesystem: validFS, connector: unusedConnector, wantCause: context.Canceled},
		{name: "connect failure", ctx: context.Background(), config: validConfig, filesystem: validFS, connector: func(context.Context, *pgx.ConnConfig) (ownedMigrationConnection, error) { return nil, connectFailure }, wantCause: connectFailure},
		{name: "connection plus connect failure", ctx: context.Background(), config: validConfig, filesystem: validFS, connector: func(context.Context, *pgx.ConnConfig) (ownedMigrationConnection, error) {
			return inconsistentOwned, connectFailure
		}, wantCause: connectFailure, wantSecond: closeFailure, owned: inconsistentOwned},
		{name: "canceled during connect", ctx: connectCanceledContext, config: validConfig, filesystem: validFS, connector: func(context.Context, *pgx.ConnConfig) (ownedMigrationConnection, error) {
			connectCancel()
			return nil, connectFailure
		}, wantCause: context.Canceled},
		{name: "nil connection", ctx: context.Background(), config: validConfig, filesystem: validFS, connector: func(context.Context, *pgx.ConnConfig) (ownedMigrationConnection, error) { return nil, nil }},
		{name: "coordinator and close failure", ctx: context.Background(), config: validConfig, filesystem: fstest.MapFS{}, connector: func(context.Context, *pgx.ConnConfig) (ownedMigrationConnection, error) {
			return &stubOwnedReleaseConnection{stubReleaseConnection: readyReleaseConnection(), closeErr: closeFailure}, nil
		}, wantCause: closeFailure},
		{name: "close failure", ctx: context.Background(), config: validConfig, filesystem: validFS, connector: func(context.Context, *pgx.ConnConfig) (ownedMigrationConnection, error) {
			return &stubOwnedReleaseConnection{stubReleaseConnection: readyReleaseConnection(coordinatorQueryResult{rows: coordinatorAppliedRows(2)}), closeErr: closeFailure}, nil
		}, wantCause: closeFailure},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := applyWithConnector(test.ctx, test.config, test.filesystem, test.connector)
			if err == nil {
				t.Fatal("applyWithConnector() returned nil error")
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("applyWithConnector() error = %v, want cause %v", err, test.wantCause)
			}
			if test.wantSecond != nil && !errors.Is(err, test.wantSecond) {
				t.Fatalf("applyWithConnector() error = %v, want second cause %v", err, test.wantSecond)
			}
			if test.owned != nil && (!test.owned.closeCalled || test.owned.closeContextErr != nil) {
				t.Fatalf("inconsistent connection close = (called %t, context %v), want (true, nil)", test.owned.closeCalled, test.owned.closeContextErr)
			}
		})
	}
}
