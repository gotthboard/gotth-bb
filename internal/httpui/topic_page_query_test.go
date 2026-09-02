package httpui

import "testing"

func TestParseTopicPageQueryAcceptsCanonicalPages(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		raw  string
		want int32
	}{
		{want: 1},
		{raw: "page=1", want: 1},
		{raw: "page=9", want: 9},
		{raw: "page=10000", want: 10000},
	} {
		test := test
		t.Run(test.raw, func(t *testing.T) {
			t.Parallel()
			got, err := parseTopicPageQuery(test.raw, 10000)
			if err != nil || got != test.want {
				t.Fatalf("parseTopicPageQuery(%q) = (%d, %v), want (%d, nil)", test.raw, got, err, test.want)
			}
		})
	}
}

func TestParseTopicPageQueryRejectsNoncanonicalOrExcessiveInput(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"page=",
		"page=0",
		"page=01",
		"page=+1",
		"page=-1",
		"page=10001",
		"page=999999999999999999999999999999999999",
		"Page=1",
		"page=%31",
		"page=1+0",
		"page=1&page=2",
		"page=1&other=2",
		"other=1",
		"page=1;other=2",
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if got, err := parseTopicPageQuery(raw, 10000); err == nil || got != 0 {
				t.Fatalf("parseTopicPageQuery(%q) = (%d, %v), want zero/error", raw, got, err)
			}
		})
	}
	for _, maximum := range []int32{0, -1} {
		if got, err := parseTopicPageQuery("page=1", maximum); err == nil || got != 0 {
			t.Fatalf("maximum %d = (%d, %v), want zero/error", maximum, got, err)
		}
	}
}
