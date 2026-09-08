package abuse

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testRateProfile = RateProfile{
	RequestLimit: 300, RequestWindow: time.Minute, RequestClientCapacity: 4096,
	PublicationLimit: 10, NewAccountLimit: 3,
	PublicationWindow: 10 * time.Minute, NewAccountPeriod: 24 * time.Hour,
}

func TestDecodePolicyAcceptsOnlyCanonicalSortedRules(t *testing.T) {
	t.Parallel()
	contents := "domain=example.com\ndomain=sub.example.com\nurl=http://192.0.2.1/path?x=1\nurl=https://example.com/\n"
	policy, err := decodePolicy(contents)
	if err != nil {
		t.Fatalf("decodePolicy() returned error: %v", err)
	}
	if strings.Join(policy.domains, ",") != "example.com,sub.example.com" || strings.Join(policy.urls, ",") != "http://192.0.2.1/path?x=1,https://example.com/" {
		t.Fatalf("policy = domains %#v, URLs %#v", policy.domains, policy.urls)
	}
	if policy, err := decodePolicy(""); err != nil || len(policy.domains) != 0 || len(policy.urls) != 0 {
		t.Fatalf("empty policy = (%+v, %v)", policy, err)
	}
}

func TestDecodePolicyRejectsNoncanonicalOrAmbiguousRules(t *testing.T) {
	t.Parallel()
	rules := []string{
		"domain=example.com",
		"domain=example.com\r\n",
		"domain=example.com\n\n",
		"domain=example.com\ndomain=example.com\n",
		"domain=sub.example.com\ndomain=example.com\n",
		"domain=Example.com\n",
		"domain=example.com.\n",
		"domain=192.0.2.1\n",
		"domain=-bad.example\n",
		"domain=bücher.example\n",
		"domain=" + strings.Repeat("a", 64) + ".example\n",
		"domain=" + strings.Repeat("a.", 127) + "a\n",
		"url=ftp://example.com/\n",
		"url=https://user@example.com/\n",
		`url=https://example.com\path` + "\n",
		"url=https://example.com\n",
		"url=https://example.com:443/\n",
		"url=https://example.com:01/\n",
		"url=https://example.com:0/\n",
		"url=https://example.com:65536/\n",
		"url=https://example.com/%zz\n",
		"url=https://example.com/%7euser\n",
		"url=https://example.com/a/../b\n",
		"url=https://example.com/#fragment\n",
		"unknown=example.com\n",
		"domain=example.com\x00\n",
	}
	for _, contents := range rules {
		contents := contents
		t.Run(strings.ReplaceAll(contents, "/", "_"), func(t *testing.T) {
			t.Parallel()
			if policy, err := decodePolicy(contents); err == nil {
				t.Fatalf("decodePolicy(%q) = %+v", contents, policy)
			}
		})
	}
	many := strings.Repeat("domain=example.com\n", MaximumRules+1)
	if _, err := decodePolicy(many); err == nil {
		t.Fatal("decodePolicy() accepted too many rules")
	}
}

func TestLoadPolicyRejectsInvalidEncodingWithoutExposingInput(t *testing.T) {
	t.Parallel()
	for name, contents := range map[string][]byte{
		"invalid UTF-8": {0xff, '\n'},
		"BOM":           {0xef, 0xbb, 0xbf, 'd', 'o', 'm', 'a', 'i', 'n', '=', 'x', '\n'},
		"control":       []byte("domain=example.com\x7f\n"),
		"missing LF":    []byte("domain=example.com"),
	} {
		name, contents := name, contents
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "sensitive-rules-name")
			if err := os.WriteFile(path, contents, 0o400); err != nil {
				t.Fatalf("os.WriteFile() returned error: %v", err)
			}
			_, err := LoadPolicy(path, testRateProfile)
			if err == nil || strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "example.com") {
				t.Fatalf("LoadPolicy() error = %v", err)
			}
		})
	}
}

func TestCanonicalURLCoversHostsEscapesQueriesAndPaths(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"https://example.com/":                     "https://example.com/",
		"https://xn--bcher-kva.example/":           "https://xn--bcher-kva.example/",
		"http://192.0.2.1:8080/a//b/?x=%2F+y":      "http://192.0.2.1:8080/a//b/?x=%2F+y",
		"https://[2001:db8::1]/path?":              "https://[2001:db8::1]/path?",
		"https://example.com/a/%2E%2E/b":           "https://example.com/b",
		"https://example.com/a/..":                 "https://example.com/",
		"https://example.com/.":                    "https://example.com/",
		"https://example.com/%7Euser?q=%41%2f#old": "https://example.com/~user?q=A%2F",
	}
	for raw, want := range tests {
		raw, want := raw, want
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			got, err := canonicalURL(raw)
			if err != nil || got != want {
				t.Fatalf("canonicalURL(%q) = (%q, %v), want %q", raw, got, err, want)
			}
		})
	}
}

func TestLoadPolicyUsesASealedRegularReadOnlyFile(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "rules")
	if err := os.WriteFile(path, []byte("domain=example.com\n"), 0o400); err != nil {
		t.Fatalf("os.WriteFile() returned error: %v", err)
	}
	policy, err := LoadPolicy(path, testRateProfile)
	if err != nil || len(policy.domains) != 1 || policy.domains[0] != "example.com" {
		t.Fatalf("LoadPolicy() = (%+v, %v)", policy, err)
	}
	established, newAccount, window, period := policy.PublicationProfile()
	if established != 10 || newAccount != 3 || window != 10*time.Minute || period != 24*time.Hour {
		t.Fatalf("PublicationProfile() = (%d, %d, %s, %s)", established, newAccount, window, period)
	}
	limiter, err := policy.NewRequestLimiter(strings.NewReader(strings.Repeat("k", 32)), time.Now)
	if err != nil || limiter == nil {
		t.Fatalf("Policy.NewRequestLimiter() = (%v, %v)", limiter, err)
	}

	for _, test := range []struct {
		name string
		path string
	}{
		{name: "relative", path: "rules"},
		{name: "root", path: "/"},
		{name: "unclean", path: directory + "/../" + filepath.Base(directory) + "/rules"},
		{name: "NUL", path: directory + "/rules\x00other"},
		{name: "long", path: "/" + strings.Repeat("a", 4096)},
		{name: "missing", path: filepath.Join(directory, "missing")},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			if got, loadErr := LoadPolicy(test.path, testRateProfile); loadErr == nil {
				t.Fatalf("LoadPolicy(%q) = %+v", test.path, got)
			}
		})
	}
}

func TestOpenedPolicyIsStableAcrossPathReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "rules")
	if err := os.WriteFile(path, []byte("domain=before.example\n"), 0o400); err != nil {
		t.Fatalf("os.WriteFile() returned error: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("os.Open() returned error: %v", err)
	}
	defer file.Close()
	replacement := filepath.Join(directory, "replacement")
	if err := os.WriteFile(replacement, []byte("domain=after.example\n"), 0o400); err != nil {
		t.Fatalf("os.WriteFile() replacement returned error: %v", err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatalf("os.Rename() returned error: %v", err)
	}
	policy, err := loadOpenedPolicy(file)
	if err != nil || len(policy.domains) != 1 || policy.domains[0] != "before.example" {
		t.Fatalf("loadOpenedPolicy() = (%+v, %v)", policy, err)
	}
	if policy, err := loadOpenedPolicy(nil); err == nil {
		t.Fatalf("loadOpenedPolicy(nil) = %+v", policy)
	}
}

func TestLoadPolicyRejectsWritableSymlinkDirectoryAndOversizeFiles(t *testing.T) {
	directory := t.TempDir()
	valid := filepath.Join(directory, "valid")
	if err := os.WriteFile(valid, []byte("domain=example.com\n"), 0o400); err != nil {
		t.Fatalf("os.WriteFile() returned error: %v", err)
	}
	writable := filepath.Join(directory, "writable")
	if err := os.WriteFile(writable, []byte("domain=example.com\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() returned error: %v", err)
	}
	symlink := filepath.Join(directory, "symlink")
	if err := os.Symlink(valid, symlink); err != nil {
		t.Fatalf("os.Symlink() returned error: %v", err)
	}
	oversize := filepath.Join(directory, "oversize")
	if err := os.WriteFile(oversize, []byte(strings.Repeat("x", MaximumRulesFileBytes+1)), 0o400); err != nil {
		t.Fatalf("os.WriteFile() returned error: %v", err)
	}
	for _, path := range []string{writable, symlink, directory, oversize, "/dev/null"} {
		if got, err := LoadPolicy(path, testRateProfile); err == nil {
			t.Fatalf("LoadPolicy(%q) = %+v", path, got)
		}
	}
}

func TestLoadPolicyRejectsInvalidRateProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules")
	if err := os.WriteFile(path, nil, 0o400); err != nil {
		t.Fatalf("os.WriteFile() returned error: %v", err)
	}
	profiles := []RateProfile{
		{},
		{RequestLimit: 100_001, RequestWindow: time.Minute, RequestClientCapacity: 1, PublicationLimit: 1, NewAccountLimit: 1, PublicationWindow: time.Minute, NewAccountPeriod: time.Minute},
		{RequestLimit: 1, RequestWindow: time.Second - 1, RequestClientCapacity: 1, PublicationLimit: 1, NewAccountLimit: 1, PublicationWindow: time.Minute, NewAccountPeriod: time.Minute},
		{RequestLimit: 1, RequestWindow: time.Minute, RequestClientCapacity: 65_537, PublicationLimit: 1, NewAccountLimit: 1, PublicationWindow: time.Minute, NewAccountPeriod: time.Minute},
		{RequestLimit: 1, RequestWindow: time.Minute, RequestClientCapacity: 1, PublicationLimit: 1, NewAccountLimit: 2, PublicationWindow: time.Minute, NewAccountPeriod: time.Minute},
		{RequestLimit: 1, RequestWindow: time.Minute, RequestClientCapacity: 1, PublicationLimit: 1, NewAccountLimit: 1, PublicationWindow: 24*time.Hour + 1, NewAccountPeriod: time.Minute},
		{RequestLimit: 1, RequestWindow: time.Minute, RequestClientCapacity: 1, PublicationLimit: 1, NewAccountLimit: 1, PublicationWindow: time.Minute, NewAccountPeriod: 30*24*time.Hour + 1},
	}
	for index, profile := range profiles {
		if policy, err := LoadPolicy(path, profile); err == nil {
			t.Fatalf("profile %d = %+v", index, policy)
		}
	}
}

func TestDecodePolicyAcceptsExactlyMaximumRules(t *testing.T) {
	t.Parallel()
	var contents strings.Builder
	for index := range MaximumRules {
		_, _ = fmt.Fprintf(&contents, "domain=%03d.example.com\n", index)
	}
	policy, err := decodePolicy(contents.String())
	if err != nil || len(policy.domains) != MaximumRules {
		t.Fatalf("decodePolicy(maximum) = (%d rules, %v)", len(policy.domains), err)
	}
}
