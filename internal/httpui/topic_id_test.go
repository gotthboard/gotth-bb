package httpui

import (
	"math"
	"testing"
)

func TestParseTopicIDAcceptsCanonicalPositiveInt64(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		raw  string
		want int64
	}{
		{raw: "1", want: 1},
		{raw: "9", want: 9},
		{raw: "42", want: 42},
		{raw: "9223372036854775807", want: math.MaxInt64},
	} {
		test := test
		t.Run(test.raw, func(t *testing.T) {
			t.Parallel()
			got, err := parseTopicID(test.raw)
			if err != nil || got != test.want {
				t.Fatalf("parseTopicID(%q) = (%d, %v), want (%d, nil)", test.raw, got, err, test.want)
			}
		})
	}
}

func TestParseTopicIDRejectsNoncanonicalOrOutOfRangeInput(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"",
		"0",
		"00",
		"01",
		"+1",
		"-1",
		"1.0",
		"1/2",
		"%31",
		"9223372036854775808",
		"999999999999999999999999999999999999",
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if got, err := parseTopicID(raw); err == nil || got != 0 {
				t.Fatalf("parseTopicID(%q) = (%d, %v), want zero/error", raw, got, err)
			}
		})
	}
}
