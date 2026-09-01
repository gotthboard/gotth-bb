package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/governance"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestRunBootstrapsExactAdministratorAndReportsCommittedIDs(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 2, 0, 0, 0, 123456789, time.UTC)
	connection := &operatorTestConnection{}
	var output bytes.Buffer
	err := run(
		context.Background(),
		operatorMapLookup(map[string]string{"DATABASE_URL": "postgres://operator:secret@db.example.test:5433/forum?sslmode=require"}),
		[]string{"bootstrap-administrator", "--issuer", "https://auth.example.test/application/o/gotth-bb/", "--subject", "subject-1", "--operator", "operator@example.test"},
		&output,
		bytes.NewReader(bytes.Repeat([]byte{0x42}, 16)),
		func() time.Time { return now },
		func(ctx context.Context, configured *pgx.ConnConfig) (operatorConnection, error) {
			if ctx == nil || configured.Host != "db.example.test" || configured.Port != 5433 || configured.Database != "forum" || configured.User != "operator" {
				t.Fatal("connector did not receive exact redacted database configuration")
			}
			return connection, nil
		},
		func(ctx context.Context, database operatorConnection, clock func() time.Time, issuer, subject, operator string, requestID pgtype.UUID) (governance.BootstrapResult, error) {
			if ctx == nil || database != connection || !clock().Equal(now) || issuer != "https://auth.example.test/application/o/gotth-bb/" || subject != "subject-1" || operator != "operator@example.test" {
				t.Fatal("bootstrapper did not receive exact operator authority")
			}
			if !requestID.Valid || requestID.Bytes[6]>>4 != 4 || requestID.Bytes[8]>>6 != 2 {
				t.Fatalf("request ID = %+v, want valid RFC 4122 version 4 UUID", requestID)
			}
			return governance.BootstrapResult{UserID: 41, AuditID: 73}, nil
		},
	)
	if err != nil || output.String() != "administrator bootstrap committed: user_id=41 audit_id=73\n" || connection.closeCalls != 1 {
		t.Fatalf("run() = (%q, %v, close calls %d)", output.String(), err, connection.closeCalls)
	}
}

func TestRunRejectsInvalidDependenciesBeforeWork(t *testing.T) {
	t.Parallel()

	validLookup := operatorMapLookup(map[string]string{"DATABASE_URL": "postgres://operator@127.0.0.1/forum"})
	validArgs := []string{"bootstrap-administrator", "--issuer", "issuer", "--subject", "subject", "--operator", "operator"}
	validOutput := io.Discard
	validEntropy := bytes.NewReader(make([]byte, 16))
	validClock := time.Now
	validConnect := func(context.Context, *pgx.ConnConfig) (operatorConnection, error) { panic("connector must not run") }
	validBootstrap := func(context.Context, operatorConnection, func() time.Time, string, string, string, pgtype.UUID) (governance.BootstrapResult, error) {
		panic("bootstrapper must not run")
	}
	for _, test := range []struct {
		name      string
		ctx       context.Context
		lookup    func(string) (string, bool)
		args      []string
		output    io.Writer
		entropy   io.Reader
		clock     func() time.Time
		connect   connectionFactory
		bootstrap administratorBootstrapper
	}{
		{name: "nil context", lookup: validLookup, args: validArgs, output: validOutput, entropy: validEntropy, clock: validClock, connect: validConnect, bootstrap: validBootstrap},
		{name: "nil lookup", ctx: context.Background(), args: validArgs, output: validOutput, entropy: validEntropy, clock: validClock, connect: validConnect, bootstrap: validBootstrap},
		{name: "nil arguments", ctx: context.Background(), lookup: validLookup, output: validOutput, entropy: validEntropy, clock: validClock, connect: validConnect, bootstrap: validBootstrap},
		{name: "nil output", ctx: context.Background(), lookup: validLookup, args: validArgs, entropy: validEntropy, clock: validClock, connect: validConnect, bootstrap: validBootstrap},
		{name: "nil entropy", ctx: context.Background(), lookup: validLookup, args: validArgs, output: validOutput, clock: validClock, connect: validConnect, bootstrap: validBootstrap},
		{name: "nil clock", ctx: context.Background(), lookup: validLookup, args: validArgs, output: validOutput, entropy: validEntropy, connect: validConnect, bootstrap: validBootstrap},
		{name: "nil connector", ctx: context.Background(), lookup: validLookup, args: validArgs, output: validOutput, entropy: validEntropy, clock: validClock, bootstrap: validBootstrap},
		{name: "nil bootstrapper", ctx: context.Background(), lookup: validLookup, args: validArgs, output: validOutput, entropy: validEntropy, clock: validClock, connect: validConnect},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := run(test.ctx, test.lookup, test.args, test.output, test.entropy, test.clock, test.connect, test.bootstrap); err == nil {
				t.Fatal("run() accepted an invalid dependency")
			}
		})
	}
}

func TestRunRejectsInvalidCommandBeforeConfiguration(t *testing.T) {
	t.Parallel()

	lookup := func(string) (string, bool) { panic("configuration must not be read") }
	for _, args := range [][]string{
		{},
		{"unknown"},
		{"bootstrap-administrator"},
		{"bootstrap-administrator", "--issuer", "issuer", "--subject", "subject"},
		{"bootstrap-administrator", "--issuer", "issuer", "--subject", "subject", "--operator", "operator", "extra"},
		{"bootstrap-administrator", "--unknown", "value", "--issuer", "issuer", "--subject", "subject", "--operator", "operator"},
		{"bootstrap-administrator", "--issuer", "issuer-a", "--issuer", "issuer-b", "--subject", "subject", "--operator", "operator"},
	} {
		args := args
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			t.Parallel()
			if err := run(context.Background(), lookup, args, io.Discard, bytes.NewReader(make([]byte, 16)), time.Now, panicOperatorConnect, panicAdministratorBootstrap); err == nil {
				t.Fatalf("run() accepted arguments %q", args)
			}
		})
	}
}

func TestRunStopsAtEveryFailureBoundary(t *testing.T) {
	t.Parallel()

	validArgs := []string{"bootstrap-administrator", "--issuer", "issuer", "--subject", "subject", "--operator", "operator"}
	validLookup := operatorMapLookup(map[string]string{"DATABASE_URL": "postgres://operator@127.0.0.1/forum"})
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	cause := errors.New("stage failed")

	t.Run("already canceled", func(t *testing.T) {
		if err := run(canceledContext, validLookup, validArgs, io.Discard, bytes.NewReader(make([]byte, 16)), time.Now, panicOperatorConnect, panicAdministratorBootstrap); !errors.Is(err, context.Canceled) {
			t.Fatalf("run() error = %v, want context cancellation", err)
		}
	})
	t.Run("redacted database configuration", func(t *testing.T) {
		const secret = "do-not-expose-operator-secret"
		err := run(context.Background(), operatorMapLookup(map[string]string{"DATABASE_URL": "postgres://" + secret + "%zz"}), validArgs, io.Discard, bytes.NewReader(make([]byte, 16)), time.Now, panicOperatorConnect, panicAdministratorBootstrap)
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("run() configuration error = %v", err)
		}
	})
	t.Run("entropy", func(t *testing.T) {
		err := run(context.Background(), validLookup, validArgs, io.Discard, errorReader{cause}, time.Now, panicOperatorConnect, panicAdministratorBootstrap)
		if !errors.Is(err, cause) {
			t.Fatalf("run() error = %v, want entropy cause", err)
		}
	})
	t.Run("canceled after entropy", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		entropy := readerFunc(func(buffer []byte) (int, error) {
			for index := range buffer {
				buffer[index] = byte(index)
			}
			cancel()
			return len(buffer), nil
		})
		err := run(ctx, validLookup, validArgs, io.Discard, entropy, time.Now, panicOperatorConnect, panicAdministratorBootstrap)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run() error = %v, want context cancellation", err)
		}
	})
	t.Run("connector", func(t *testing.T) {
		returned := &operatorTestConnection{}
		err := run(context.Background(), validLookup, validArgs, io.Discard, bytes.NewReader(make([]byte, 16)), time.Now, func(context.Context, *pgx.ConnConfig) (operatorConnection, error) {
			return returned, cause
		}, panicAdministratorBootstrap)
		if err == nil || errors.Is(err, cause) || returned.closeCalls != 1 {
			t.Fatalf("run() = (%v, close calls %d), want redacted connector failure and close", err, returned.closeCalls)
		}
	})
	t.Run("connector cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		err := run(ctx, validLookup, validArgs, io.Discard, bytes.NewReader(make([]byte, 16)), time.Now, func(context.Context, *pgx.ConnConfig) (operatorConnection, error) {
			cancel()
			return nil, cause
		}, panicAdministratorBootstrap)
		if !errors.Is(err, context.Canceled) || errors.Is(err, cause) {
			t.Fatalf("run() error = %v, want context cancellation only", err)
		}
	})
	t.Run("nil connection", func(t *testing.T) {
		err := run(context.Background(), validLookup, validArgs, io.Discard, bytes.NewReader(make([]byte, 16)), time.Now, func(context.Context, *pgx.ConnConfig) (operatorConnection, error) {
			return nil, nil
		}, panicAdministratorBootstrap)
		if err == nil {
			t.Fatal("run() accepted a nil connection")
		}
	})
	t.Run("bootstrap", func(t *testing.T) {
		connection := &operatorTestConnection{}
		err := run(context.Background(), validLookup, validArgs, io.Discard, bytes.NewReader(make([]byte, 16)), time.Now, func(context.Context, *pgx.ConnConfig) (operatorConnection, error) {
			return connection, nil
		}, func(context.Context, operatorConnection, func() time.Time, string, string, string, pgtype.UUID) (governance.BootstrapResult, error) {
			return governance.BootstrapResult{}, cause
		})
		if !errors.Is(err, cause) || connection.closeCalls != 1 {
			t.Fatalf("run() = (%v, close calls %d), want bootstrap cause and close", err, connection.closeCalls)
		}
	})
	t.Run("invalid bootstrap result", func(t *testing.T) {
		connection := &operatorTestConnection{}
		err := run(context.Background(), validLookup, validArgs, io.Discard, bytes.NewReader(make([]byte, 16)), time.Now, func(context.Context, *pgx.ConnConfig) (operatorConnection, error) {
			return connection, nil
		}, func(context.Context, operatorConnection, func() time.Time, string, string, string, pgtype.UUID) (governance.BootstrapResult, error) {
			return governance.BootstrapResult{}, nil
		})
		if err == nil || connection.closeCalls != 1 {
			t.Fatalf("run() = (%v, close calls %d), want invalid result failure and close", err, connection.closeCalls)
		}
	})
	t.Run("committed result output", func(t *testing.T) {
		connection := &operatorTestConnection{}
		err := run(context.Background(), validLookup, validArgs, errorWriter{cause}, bytes.NewReader(make([]byte, 16)), time.Now, func(context.Context, *pgx.ConnConfig) (operatorConnection, error) {
			return connection, nil
		}, func(context.Context, operatorConnection, func() time.Time, string, string, string, pgtype.UUID) (governance.BootstrapResult, error) {
			return governance.BootstrapResult{UserID: 1, AuditID: 2}, nil
		})
		if !errors.Is(err, cause) || !strings.Contains(err.Error(), "committed") || connection.closeCalls != 1 {
			t.Fatalf("run() = (%v, close calls %d), want explicit committed output failure", err, connection.closeCalls)
		}
	})
}

func operatorMapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func panicOperatorConnect(context.Context, *pgx.ConnConfig) (operatorConnection, error) {
	panic("connector must not run")
}

func panicAdministratorBootstrap(context.Context, operatorConnection, func() time.Time, string, string, string, pgtype.UUID) (governance.BootstrapResult, error) {
	panic("bootstrapper must not run")
}

type operatorTestConnection struct {
	closeCalls int
}

func (*operatorTestConnection) Begin(context.Context) (pgx.Tx, error) {
	panic("test bootstrapper owns transaction behavior")
}

func (connection *operatorTestConnection) Close(context.Context) error {
	connection.closeCalls++
	return nil
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }

type readerFunc func([]byte) (int, error)

func (reader readerFunc) Read(buffer []byte) (int, error) { return reader(buffer) }

type errorWriter struct{ err error }

func (writer errorWriter) Write([]byte) (int, error) { return 0, writer.err }
