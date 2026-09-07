package rerender

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type unusableDatabase struct{}

func (unusableDatabase) Begin(context.Context) (pgx.Tx, error) {
	return nil, nil
}

func TestRunRejectsInvalidBoundaries(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		ctx       context.Context
		database  database
		output    io.Writer
		batchSize int
	}{
		{name: "nil context", output: io.Discard, batchSize: 1},
		{name: "nil database", ctx: context.Background(), output: io.Discard, batchSize: 1},
		{name: "nil output", ctx: context.Background(), database: unusableDatabase{}, batchSize: 1},
		{name: "zero batch", ctx: context.Background(), database: unusableDatabase{}, output: io.Discard},
		{name: "oversized batch", ctx: context.Background(), database: unusableDatabase{}, output: io.Discard, batchSize: MaximumBatchSize + 1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := Run(test.ctx, test.database, test.output, test.batchSize); err == nil {
				t.Fatal("Run() accepted an invalid boundary")
			}
		})
	}
}

func TestProgressLineContainsNoContent(t *testing.T) {
	t.Parallel()

	line := progressLine(BatchResult{Converted: 17, Complete: false})
	if line != "renderer_migration target=goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2 converted=17 complete=false\n" || strings.Contains(line, "markdown") {
		t.Fatalf("progress line = %q", line)
	}
}

func TestRunAcceptsMaximumBatchBoundary(t *testing.T) {
	t.Parallel()

	err := Run(context.Background(), unusableDatabase{}, io.Discard, MaximumBatchSize)
	if err == nil || !strings.Contains(err.Error(), "returned no transaction") {
		t.Fatalf("Run(maximum batch) error = %v, want delegated transaction failure", err)
	}
}
