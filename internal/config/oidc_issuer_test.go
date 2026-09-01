package config

import (
	"strings"
	"testing"
)

func TestParseOIDCIssuerURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		production bool
	}{
		{name: "Authentik provider issuer", raw: "https://auth.example.com/application/o/gotth-bb/", production: true},
		{name: "Authentik underscore issuer", raw: "https://auth.example.com/application/o/GOTTH_BB/", production: true},
		{name: "development HTTP issuer", raw: "http://127.0.0.1:9000/application/o/gotth-bb/"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseOIDCIssuerURL(test.raw, test.production)
			if err != nil {
				t.Fatalf("ParseOIDCIssuerURL(%q) returned error: %v", test.raw, err)
			}
			if got.String() != test.raw {
				t.Fatalf("ParseOIDCIssuerURL(%q) = %q", test.raw, got.String())
			}
		})
	}
}

func TestParseOIDCIssuerURLRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		production bool
	}{
		{name: "empty", raw: "", production: true},
		{name: "relative", raw: "/application/o/gotth-bb/", production: true},
		{name: "opaque", raw: "https:application/o/gotth-bb/", production: true},
		{name: "missing host", raw: "https:///application/o/gotth-bb/", production: true},
		{name: "Authentik global issuer", raw: "https://auth.example.com/", production: true},
		{name: "wrong provider path", raw: "https://auth.example.com/authorize/", production: true},
		{name: "missing application slug", raw: "https://auth.example.com/application/o/", production: true},
		{name: "extra provider path segment", raw: "https://auth.example.com/application/o/team/gotth-bb/", production: true},
		{name: "escaped slug", raw: "https://auth.example.com/application/o/gotth%2Dbb/", production: true},
		{name: "invalid slug character", raw: "https://auth.example.com/application/o/gotth.bb/", production: true},
		{name: "reserved authorize slug", raw: "https://auth.example.com/application/o/authorize/", production: true},
		{name: "reserved token slug", raw: "https://auth.example.com/application/o/token/", production: true},
		{name: "reserved device slug", raw: "https://auth.example.com/application/o/device/", production: true},
		{name: "reserved userinfo slug", raw: "https://auth.example.com/application/o/userinfo/", production: true},
		{name: "reserved introspect slug", raw: "https://auth.example.com/application/o/introspect/", production: true},
		{name: "reserved revoke slug", raw: "https://auth.example.com/application/o/revoke/", production: true},
		{name: "unsupported scheme", raw: "ftp://auth.example.com/application/o/gotth-bb/"},
		{name: "noncanonical uppercase scheme", raw: "HTTPS://auth.example.com/application/o/gotth-bb/", production: true},
		{name: "HTTP production issuer", raw: "http://auth.example.com/application/o/gotth-bb/", production: true},
		{name: "credentials", raw: "https://user:pass@auth.example.com/application/o/gotth-bb/", production: true},
		{name: "query", raw: "https://auth.example.com/application/o/gotth-bb/?debug=true", production: true},
		{name: "empty query", raw: "https://auth.example.com/application/o/gotth-bb/?", production: true},
		{name: "fragment", raw: "https://auth.example.com/application/o/gotth-bb/#top", production: true},
		{name: "empty fragment", raw: "https://auth.example.com/application/o/gotth-bb/#", production: true},
		{name: "noncanonical space", raw: "https://auth.example.com/application/o/gotth bb/", production: true},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got, err := ParseOIDCIssuerURL(test.raw, test.production); err == nil {
				t.Fatalf("ParseOIDCIssuerURL(%q) = %q, want error", test.raw, got.String())
			}
		})
	}
}

func TestParseOIDCIssuerURLRedactsMalformedInput(t *testing.T) {
	t.Parallel()

	const secret = "do-not-log-issuer"
	_, err := ParseOIDCIssuerURL("https://"+secret+"%zz.example.com/", true)
	if err == nil {
		t.Fatal("ParseOIDCIssuerURL accepted malformed input")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("ParseOIDCIssuerURL error exposed configured value: %q", err)
	}
}
