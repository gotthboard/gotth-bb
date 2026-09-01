package config

import "testing"

func TestParseSessionCookieName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		want string
	}{
		{raw: "", want: "gotth_bb_session"},
		{raw: "gotth_bb_session", want: "gotth_bb_session"},
		{raw: "forum-session.1", want: "forum-session.1"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.raw, func(t *testing.T) {
			t.Parallel()

			got, err := ParseSessionCookieName(test.raw)
			if err != nil {
				t.Fatalf("ParseSessionCookieName(%q) returned error: %v", test.raw, err)
			}
			if got != test.want {
				t.Fatalf("ParseSessionCookieName(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func TestParseSessionCookieNameRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"session name",
		"session;name",
		"session/name",
		"session\nname",
		"séance",
		"__Host-gotth_bb_session",
		"__host-gotth_bb_session",
		"__Secure-gotth_bb_session",
		"__SECURE-gotth_bb_session",
		"__Http-gotth_bb_session",
		"__hOsT-hTtP-gotth_bb_session",
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if got, err := ParseSessionCookieName(raw); err == nil {
				t.Fatalf("ParseSessionCookieName(%q) = %q, want error", raw, got)
			}
		})
	}
}
