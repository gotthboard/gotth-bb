package render

import (
	"strings"
	"testing"
)

var benchmarkRenderedMarkdown RenderedMarkdown

func BenchmarkRenderMarkdown(b *testing.B) {
	pathological := strings.Repeat("| x | y |\n| - | - |\n| ~~x~~ | - [ ] y |\n", 1_400)
	if len(pathological) > MaximumMarkdownBytes {
		pathological = pathological[:MaximumMarkdownBytes]
	}
	for _, workload := range []struct {
		name   string
		source string
	}{
		{name: "minimum", source: "x"},
		{name: "small", source: "Hello **world** and https://example.org."},
		{name: "typical-4KiB", source: strings.Repeat("paragraph with **bold** text\n\n", 137)},
		{name: "large-32KiB", source: strings.Repeat("ordinary forum prose ", 1_638)},
		{name: "pathological-gfm", source: pathological},
		{name: "boundary-64KiB", source: strings.Repeat("x", MaximumMarkdownBytes)},
	} {
		b.Run(workload.name, func(b *testing.B) {
			if _, err := RenderMarkdown(workload.source); err != nil {
				b.Fatalf("validate workload: %v", err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(workload.source)))
			b.ResetTimer()
			for range b.N {
				var err error
				benchmarkRenderedMarkdown, err = RenderMarkdown(workload.source)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkRenderMarkdownRejected(b *testing.B) {
	for _, workload := range []struct {
		name   string
		source string
	}{
		{name: "empty", source: ""},
		{name: "over-boundary", source: strings.Repeat("x", MaximumMarkdownBytes+1)},
	} {
		b.Run(workload.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := RenderMarkdown(workload.source); err == nil {
					b.Fatal("invalid workload accepted")
				}
			}
		})
	}
}
