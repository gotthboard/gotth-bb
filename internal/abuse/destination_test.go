package abuse

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestDestinationPolicyMatchesCanonicalDomainsAndExactURLs(t *testing.T) {
	t.Parallel()
	loaded, err := decodePolicy("domain=xn--bcher-kva.example\nurl=http://[2001:db8::1]:8080/a\nurl=https://exact.example/a/b?x=A%2F\n")
	if err != nil {
		t.Fatalf("decodePolicy() returned error: %v", err)
	}
	loaded = loaded.withRateProfile(testRateProfile)
	policy := loaded.DestinationPolicy()
	blocked := []string{
		"https://b\u00fccher.example/path", "https://sub.b\u00fccher.example./path",
		"HTTPS://user:secret@EXACT.example:0443/a/./c/../b?x=%41%2f#ignored",
		"http://[2001:0db8:0:0:0:0:0:1]:08080/a#ignored",
	}
	for _, destination := range blocked {
		if err := policy.Check([]byte(destination)); !errors.Is(err, ErrBlockedDestination) {
			t.Fatalf("Check(%q) error = %v, want blocked", destination, err)
		}
	}
	allowed := []string{
		"https://sibling-b\u00fccher.example/path", "https://exact.example/a/b?x=A%2F&other=1",
		"https://exact.example:8443/a/b?x=A%2F", "https://exact.example/a%2Fb?x=A%2F",
		"https://exact.example/a/b?other=1&x=A%2F", "http://[2001:db8::2]:8080/a",
		"/local/path", "../relative", "#anchor", "mailto:user@example.com", "ftp://example.com/file",
	}
	for _, destination := range allowed {
		if err := policy.Check([]byte(destination)); err != nil {
			t.Fatalf("Check(%q) returned error: %v", destination, err)
		}
	}
}

func TestDestinationPolicyRejectsBrowserAmbiguityWithoutRetainingInput(t *testing.T) {
	t.Parallel()
	policy := NewEmptyDestinationPolicy()
	for _, destination := range []string{
		"//attacker.example/path", `\\attacker.example\path`, `/\attacker.example/path`,
		`https:\\attacker.example\path`, "http:attacker.example/path", "https://example.com/%zz",
	} {
		err := policy.Check([]byte(destination))
		if !errors.Is(err, ErrBlockedDestination) || strings.Contains(err.Error(), "attacker") || strings.Contains(err.Error(), "%zz") {
			t.Fatalf("Check(%q) error = %v", destination, err)
		}
	}
	if err := (DestinationPolicy{}).Check([]byte("https://example.com/")); err == nil {
		t.Fatal("zero DestinationPolicy accepted input")
	}
}

func TestDestinationPolicyCoversEmptyAndMaximumRuleSets(t *testing.T) {
	t.Parallel()
	if policy := NewEmptyDestinationPolicy(); !policy.Valid() || policy.Check([]byte("https://example.com/")) != nil {
		t.Fatalf("empty destination policy = %+v", policy)
	}
	var contents strings.Builder
	for index := range MaximumRules {
		_, _ = fmt.Fprintf(&contents, "domain=%03d.example.com\n", index)
	}
	loaded, err := decodePolicy(contents.String())
	if err != nil {
		t.Fatalf("decodePolicy(maximum) returned error: %v", err)
	}
	loaded = loaded.withRateProfile(testRateProfile)
	if err := loaded.DestinationPolicy().Check([]byte("https://255.example.com/path")); !errors.Is(err, ErrBlockedDestination) {
		t.Fatalf("maximum policy Check() error = %v", err)
	}
	contents.Reset()
	for index := range MaximumRules {
		_, _ = fmt.Fprintf(&contents, "url=https://%03d.example.com/path?x=%d\n", index, index)
	}
	loaded, err = decodePolicy(contents.String())
	if err != nil {
		t.Fatalf("decodePolicy(maximum URLs) returned error: %v", err)
	}
	loaded = loaded.withRateProfile(testRateProfile)
	if err := loaded.DestinationPolicy().Check([]byte("https://255.example.com/path?x=255#ignored")); !errors.Is(err, ErrBlockedDestination) {
		t.Fatalf("maximum URL policy Check() error = %v", err)
	}
}
