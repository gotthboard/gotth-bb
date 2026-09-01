package httpui

import "testing"

func TestURLBuilderPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		basePath string
		segments []string
		want     string
	}{
		{name: "root deployment root", basePath: "", want: "/"},
		{name: "root deployment route", basePath: "", segments: []string{"login"}, want: "/login"},
		{name: "prefixed root", basePath: "/bb", want: "/bb/"},
		{name: "prefixed route", basePath: "/bb", segments: []string{"health", "live"}, want: "/bb/health/live"},
		{name: "alternate prefix", basePath: "/community/board", segments: []string{"topics", "42"}, want: "/community/board/topics/42"},
		{name: "hostile segment escaped", basePath: "/bb", segments: []string{"areas", "staff/../../admin?q=1#x"}, want: "/bb/areas/staff%2F..%2F..%2Fadmin%3Fq=1%23x"},
		{name: "unicode segment escaped", basePath: "/bb", segments: []string{"areas", "café"}, want: "/bb/areas/caf%C3%A9"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			builder, err := NewURLBuilder(test.basePath)
			if err != nil {
				t.Fatalf("NewURLBuilder(%q) returned error: %v", test.basePath, err)
			}
			if got := builder.Path(test.segments...); got != test.want {
				t.Fatalf("Path(%q) = %q, want %q", test.segments, got, test.want)
			}
		})
	}
}

func TestNewURLBuilderRejectsInvalidBasePath(t *testing.T) {
	t.Parallel()

	if _, err := NewURLBuilder("/bb/.."); err == nil {
		t.Fatal("NewURLBuilder accepted an unsafe base path")
	}
}
