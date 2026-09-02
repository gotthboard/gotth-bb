package config

import (
	"net/url"
	"strings"
	"testing"
)

func TestParseRegistrationURL(t *testing.T) {
	t.Parallel()

	issuer := url.URL{Scheme: "https", Host: "auth.example.com", Path: "/application/o/gotth-bb/"}
	raw := "https://auth.example.com/if/flow/gotth-bb-enrollment/"
	got, err := ParseRegistrationURL(raw, issuer, true)
	if err != nil || got.String() != raw {
		t.Fatalf("ParseRegistrationURL() = (%q, %v)", got.String(), err)
	}
}

func TestParseRegistrationURLRejectsInvalidValuesWithoutEcho(t *testing.T) {
	t.Parallel()

	issuer := url.URL{Scheme: "https", Host: "auth.example.com", Path: "/application/o/gotth-bb/"}
	secret := "do-not-echo-registration-input"
	tests := []string{
		"", "http://auth.example.com/if/flow/gotth-bb-enrollment/",
		"https://other.example.com/if/flow/gotth-bb-enrollment/",
		"https://auth.example.com/if/flow/", "https://auth.example.com/if/flow/two/segments/",
		"https://auth.example.com/if/flow/invalid.slug/", "https://auth.example.com/if/flow/gotth-bb-enrollment",
		"https://user@auth.example.com/if/flow/gotth-bb-enrollment/",
		"https://auth.example.com/if/flow/gotth-bb-enrollment/?next=" + secret,
		"https://auth.example.com/if/flow/gotth-bb-enrollment/#" + secret,
		"https://auth.example.com/if/flow/gotth%2Dbbenrollment/",
		"https://auth.example.com/%zz" + secret,
		strings.Repeat("x", 2049),
	}
	for _, raw := range tests {
		got, err := ParseRegistrationURL(raw, issuer, true)
		if err == nil || got != (url.URL{}) {
			t.Fatalf("ParseRegistrationURL(%q) = (%q, %v), want zero/error", raw, got.String(), err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposed configured URL: %q", err)
		}
	}
}

func TestParseRegistrationURLAllowsSameOriginHTTPOutsideProduction(t *testing.T) {
	t.Parallel()

	issuer := url.URL{Scheme: "http", Host: "127.0.0.1:9000", Path: "/application/o/gotth-bb/"}
	raw := "http://127.0.0.1:9000/if/flow/local-enrollment/"
	got, err := ParseRegistrationURL(raw, issuer, false)
	if err != nil || got.String() != raw {
		t.Fatalf("ParseRegistrationURL() = (%q, %v)", got.String(), err)
	}
}
