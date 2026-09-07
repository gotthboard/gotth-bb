package rerender

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	contentrender "github.com/gotthboard/gotth-bb/internal/render"
	"github.com/jackc/pgx/v5"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
)

type unusableDatabase struct{}

func (unusableDatabase) Begin(context.Context) (pgx.Tx, error) {
	return nil, nil
}

type unusablePreflightDatabase struct{}

func (unusablePreflightDatabase) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return nil, nil
}

func TestRunRejectsInvalidBoundaries(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		ctx       context.Context
		database  database
		batchSize int
	}{
		{name: "nil context", batchSize: 1},
		{name: "nil database", ctx: context.Background(), batchSize: 1},
		{name: "zero batch", ctx: context.Background(), database: unusableDatabase{}},
		{name: "oversized batch", ctx: context.Background(), database: unusableDatabase{}, batchSize: MaximumBatchSize + 1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := Run(test.ctx, test.database, test.batchSize); err == nil {
				t.Fatal("Run() accepted an invalid boundary")
			}
		})
	}
}

func TestRunAcceptsMaximumBatchBoundary(t *testing.T) {
	t.Parallel()

	err := Run(context.Background(), unusableDatabase{}, MaximumBatchSize)
	if err == nil || !strings.Contains(err.Error(), "returned no transaction") {
		t.Fatalf("Run(maximum batch) error = %v, want delegated transaction failure", err)
	}
}

func TestPreflightRejectsInvalidBoundaries(t *testing.T) {
	t.Parallel()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name      string
		ctx       context.Context
		database  preflightDatabase
		batchSize int
	}{
		{name: "nil context", database: unusablePreflightDatabase{}, batchSize: 1},
		{name: "nil database", ctx: context.Background(), batchSize: 1},
		{name: "canceled context", ctx: canceled, database: unusablePreflightDatabase{}, batchSize: 1},
		{name: "zero batch", ctx: context.Background(), database: unusablePreflightDatabase{}},
		{name: "oversized batch", ctx: context.Background(), database: unusablePreflightDatabase{}, batchSize: MaximumBatchSize + 1},
		{name: "nil transaction", ctx: context.Background(), database: unusablePreflightDatabase{}, batchSize: 1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := Preflight(test.ctx, test.database, test.batchSize); err == nil {
				t.Fatal("Preflight() accepted an invalid boundary")
			}
		})
	}
}

func TestPreflightPostEnforcesRendererAndRedactionIntegrity(t *testing.T) {
	t.Parallel()

	currentHTML := currentHTMLForTest(t, "valid **source**")
	for _, test := range []struct {
		name          string
		candidate     post
		redacted      bool
		alpha3Applied bool
		wantError     bool
	}{
		{name: "premature exact current", candidate: post{markdown: "valid **source**", originalHTML: currentHTML, originalVersion: contentrender.RendererVersion}, wantError: true},
		{name: "applied exact current", candidate: post{markdown: "valid **source**", originalHTML: currentHTML, originalVersion: contentrender.RendererVersion}, alpha3Applied: true},
		{name: "applied forged current", candidate: post{markdown: "valid **source**", originalHTML: "<p>forged</p>\n", originalVersion: contentrender.RendererVersion}, alpha3Applied: true, wantError: true},
		{name: "applied invalid current source", candidate: post{markdown: " ", originalHTML: "<p>forged</p>\n", originalVersion: contentrender.RendererVersion}, alpha3Applied: true, wantError: true},
		{name: "redacted current", candidate: post{markdown: "valid **source**", originalHTML: currentHTML, originalVersion: contentrender.RendererVersion}, redacted: true, alpha3Applied: true, wantError: true},
		{name: "exact redaction", candidate: post{markdown: "[Content removed by moderation]", originalHTML: "<p>Content removed by moderation.</p>", originalVersion: "moderation-redaction-v1"}, redacted: true},
		{name: "forged redaction", candidate: post{markdown: "[Content removed by moderation]", originalHTML: "<p>forged</p>", originalVersion: "moderation-redaction-v1"}, redacted: true, wantError: true},
		{name: "unknown under limit converts", candidate: post{markdown: "valid **source**", originalHTML: "discarded", originalVersion: "unknown-v1"}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := test.candidate
			err := preflightPost(context.Background(), &candidate, test.redacted, test.alpha3Applied)
			if test.wantError {
				if err == nil {
					t.Fatal("preflightPost() returned nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("preflightPost() returned error: %v", err)
			}
			if test.candidate.originalVersion == "unknown-v1" && (candidate.nextVersion != contentrender.RendererVersion || candidate.nextHTML != currentHTML) {
				t.Fatalf("unknown renderer result = (%q, %q), want canonical p2", candidate.nextHTML, candidate.nextVersion)
			}
		})
	}
}

func TestPreparePostUsesCurrentRendererForOrdinaryLegacyRow(t *testing.T) {
	t.Parallel()

	candidate := post{
		markdown:        "~~ordinary~~",
		originalHTML:    legacyHTMLForTest(t, "~~ordinary~~"),
		originalVersion: contentrender.LegacyRendererVersion,
	}
	if err := preparePost(context.Background(), &candidate); err != nil {
		t.Fatalf("preparePost() returned error: %v", err)
	}
	if candidate.nextVersion != contentrender.RendererVersion || candidate.nextHTML != "<p><del>ordinary</del></p>\n" {
		t.Fatalf("prepared ordinary post = (%q, %q), want current p2 result", candidate.nextHTML, candidate.nextVersion)
	}
}

func TestPreparePostPreservesOnlyExactOversizedP1Output(t *testing.T) {
	t.Parallel()

	source := strings.Repeat("- [x]\n", contentrender.MaximumMarkdownBytes/len("- [x]\n"))
	legacyHTML := legacyHTMLForTest(t, source)
	if len(legacyHTML) == 0 || len(legacyHTML) > contentrender.MaximumRenderedHTMLBytes {
		t.Fatalf("legacy fixture bytes = %d, want 1..%d", len(legacyHTML), contentrender.MaximumRenderedHTMLBytes)
	}
	for _, test := range []struct {
		name      string
		candidate post
		wantError bool
	}{
		{name: "exact p1", candidate: post{markdown: source, originalHTML: legacyHTML, originalVersion: contentrender.LegacyRendererVersion}},
		{name: "unknown old renderer", candidate: post{markdown: source, originalHTML: legacyHTML, originalVersion: "legacy-p1"}, wantError: true},
		{name: "tampered p1 HTML", candidate: post{markdown: source, originalHTML: legacyHTML + "tampered", originalVersion: contentrender.LegacyRendererVersion}, wantError: true},
		{name: "invalid source", candidate: post{markdown: " \n ", originalHTML: "<p>x</p>\n", originalVersion: contentrender.LegacyRendererVersion}, wantError: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := test.candidate
			err := preparePost(context.Background(), &candidate)
			if test.wantError {
				if err == nil || candidate.nextVersion == legacyPreservedRendererVersion {
					t.Fatalf("preparePost() = (%+v, %v), want fail-closed", candidate, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("preparePost() returned error: %v", err)
			}
			if candidate.nextHTML != legacyHTML || candidate.nextVersion != legacyPreservedRendererVersion {
				t.Fatalf("compatibility result = (%d bytes, %q), want exact %d-byte p1 HTML and marker", len(candidate.nextHTML), candidate.nextVersion, len(legacyHTML))
			}
		})
	}
}

func TestPreparePostRejectsInvalidBoundaries(t *testing.T) {
	t.Parallel()

	if err := preparePost(context.Background(), nil); err == nil {
		t.Fatal("preparePost(context, nil) returned nil")
	}
	if err := preparePost(nil, &post{}); err == nil {
		t.Fatal("preparePost(nil, post) returned nil")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := preparePost(canceled, &post{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("preparePost(canceled) error = %v, want context.Canceled", err)
	}
}

func TestPreparePostObservesCancellationBeforeLegacyReconstruction(t *testing.T) {
	t.Parallel()

	source := strings.Repeat("- [x]\n", contentrender.MaximumMarkdownBytes/len("- [x]\n"))
	candidate := post{
		markdown:        source,
		originalHTML:    legacyHTMLForTest(t, source),
		originalVersion: contentrender.LegacyRendererVersion,
	}
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(5*time.Millisecond, cancel)
	defer timer.Stop()
	err := preparePost(ctx, &candidate)
	if !errors.Is(err, context.Canceled) || candidate.nextVersion != "" {
		t.Fatalf("preparePost(canceled during p2) = (%q, %v), want no compatibility result and context.Canceled", candidate.nextVersion, err)
	}
}

func legacyHTMLForTest(t *testing.T, source string) string {
	t.Helper()
	var rendered bytes.Buffer
	if err := goldmark.New().Convert([]byte(source), &rendered); err != nil {
		t.Fatalf("legacy Goldmark render: %v", err)
	}
	policy := bluemonday.NewPolicy()
	policy.AllowElements("p", "em", "strong", "ul", "ol", "li", "a", "blockquote", "pre", "code", "br")
	policy.AllowAttrs("href").OnElements("a")
	policy.AllowRelativeURLs(true)
	policy.AllowURLSchemes("http", "https")
	policy.RequireNoFollowOnLinks(true)
	policy.RequireNoReferrerOnLinks(true)
	return policy.Sanitize(rendered.String())
}

func currentHTMLForTest(t *testing.T, source string) string {
	t.Helper()
	rendered, err := contentrender.RenderMarkdown(source)
	if err != nil {
		t.Fatalf("render current Markdown: %v", err)
	}
	html, _, err := rendered.PersistenceValues()
	if err != nil {
		t.Fatalf("read current rendered Markdown: %v", err)
	}
	return html
}

func BenchmarkPreparePost(b *testing.B) {
	denseSource := strings.Repeat("- [x]\n", contentrender.MaximumMarkdownBytes/len("- [x]\n"))
	denseHTML := legacyHTMLForBenchmark(b, denseSource)
	for _, benchmark := range []struct {
		name      string
		candidate post
	}{
		{name: "ordinary-p2", candidate: post{markdown: "ordinary **post**", originalHTML: "<p>ordinary <strong>post</strong></p>\n", originalVersion: contentrender.LegacyRendererVersion}},
		{name: "oversized-p1-preserved", candidate: post{markdown: denseSource, originalHTML: denseHTML, originalVersion: contentrender.LegacyRendererVersion}},
	} {
		benchmark := benchmark
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				candidate := benchmark.candidate
				if err := preparePost(context.Background(), &candidate); err != nil {
					b.Fatalf("preparePost() returned error: %v", err)
				}
			}
		})
	}
}

func BenchmarkPrepareMaximumCompatibilityBatch(b *testing.B) {
	denseSource := strings.Repeat("- [x]\n", contentrender.MaximumMarkdownBytes/len("- [x]\n"))
	denseHTML := legacyHTMLForBenchmark(b, denseSource)
	candidates := make([]post, MaximumBatchSize)
	for index := range candidates {
		candidates[index] = post{
			markdown:        strings.Clone(denseSource),
			originalHTML:    strings.Clone(denseHTML),
			originalVersion: contentrender.LegacyRendererVersion,
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for index := range candidates {
			candidates[index].nextHTML = ""
			candidates[index].nextVersion = ""
			if err := preparePost(context.Background(), &candidates[index]); err != nil {
				b.Fatalf("preparePost(%d) returned error: %v", index, err)
			}
		}
	}
}

func legacyHTMLForBenchmark(b *testing.B, source string) string {
	b.Helper()
	var rendered bytes.Buffer
	if err := goldmark.New().Convert([]byte(source), &rendered); err != nil {
		b.Fatalf("legacy Goldmark render: %v", err)
	}
	policy := bluemonday.NewPolicy()
	policy.AllowElements("p", "em", "strong", "ul", "ol", "li", "a", "blockquote", "pre", "code", "br")
	policy.AllowAttrs("href").OnElements("a")
	policy.AllowRelativeURLs(true)
	policy.AllowURLSchemes("http", "https")
	policy.RequireNoFollowOnLinks(true)
	policy.RequireNoReferrerOnLinks(true)
	return policy.Sanitize(rendered.String())
}
