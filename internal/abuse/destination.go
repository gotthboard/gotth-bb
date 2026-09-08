package abuse

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// ErrBlockedDestination is the fixed, non-sensitive destination-policy
// rejection class. It never retains the submitted destination or matched rule.
var ErrBlockedDestination = errors.New("blocked destination")

// DestinationPolicy is the immutable blocked-destination subset of the
// startup policy. The zero value is invalid, so every caller must deliberately
// supply either the loaded runtime policy or a validated empty policy.
type DestinationPolicy struct {
	validated bool
	domains   []string
	urls      []string
}

// DestinationPolicy returns the immutable destination subset of a validated
// startup policy.
func (policy Policy) DestinationPolicy() DestinationPolicy {
	return DestinationPolicy{validated: policy.validated, domains: policy.domains, urls: policy.urls}
}

// NewEmptyDestinationPolicy returns a deliberate validated policy with no
// blocked destinations. It exists for isolated composition and tests.
func NewEmptyDestinationPolicy() DestinationPolicy {
	return DestinationPolicy{validated: true}
}

// Valid reports whether the value came from a validated constructor.
func (policy DestinationPolicy) Valid() bool {
	return policy.validated && len(policy.domains)+len(policy.urls) <= MaximumRules
}

// Check rejects one blocked or browser-ambiguous resolved Markdown
// destination. Relative references, fragments, mailto links, and other
// non-HTTP schemes remain under the existing renderer and sanitizer policy.
//
// Complexity: for destination bytes n and at most 256 rules, time is O(n*r)
// and auxiliary space O(n), where r is the bounded rule count. No I/O, DNS,
// network access, cache mutation, or attacker-controlled error text occurs.
func (policy DestinationPolicy) Check(destination []byte) error {
	if !policy.Valid() {
		return fmt.Errorf("destination policy is invalid")
	}
	raw := string(destination)
	if strings.Contains(raw, `\`) || strings.HasPrefix(raw, "//") {
		return ErrBlockedDestination
	}
	canonical, host, external, err := canonicalAuthoredURL(raw)
	if err != nil {
		return ErrBlockedDestination
	}
	if !external {
		return nil
	}
	for _, domain := range policy.domains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return ErrBlockedDestination
		}
	}
	if _, found := slices.BinarySearch(policy.urls, canonical); found {
		return ErrBlockedDestination
	}
	return nil
}

// canonicalAuthoredURL produces the same comparison key as canonicalURL while
// accepting harmless authored variants that rules deliberately canonicalize.
func canonicalAuthoredURL(raw string) (canonical, host string, external bool, err error) {
	parsed, parseErr := url.Parse(raw)
	if parseErr != nil {
		if looksLikeHTTP(raw) {
			return "", "", false, parseErr
		}
		return "", "", false, nil
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		if looksLikeHTTP(raw) {
			return "", "", false, fmt.Errorf("invalid HTTP destination")
		}
		return "", "", false, nil
	}
	if parsed.Opaque != "" || parsed.Host == "" {
		return "", "", false, fmt.Errorf("invalid HTTP destination")
	}
	hostname := parsed.Hostname()
	if strings.HasSuffix(hostname, ".") {
		hostname = strings.TrimSuffix(hostname, ".")
	}
	canonicalHost, canonicalDomainHost, hostErr := canonicalAuthoredHost(hostname)
	if hostErr != nil {
		return "", "", false, hostErr
	}
	port := parsed.Port()
	if port != "" {
		value, portErr := strconv.ParseUint(port, 10, 16)
		if portErr != nil || value == 0 {
			return "", "", false, fmt.Errorf("invalid URL port")
		}
		port = strconv.FormatUint(value, 10)
		if !(scheme == "http" && port == "80" || scheme == "https" && port == "443") {
			canonicalHost += ":" + port
		}
	}
	pathValue := parsed.EscapedPath()
	if pathValue == "" {
		pathValue = "/"
	}
	pathValue, err = canonicalEscapes(pathValue)
	if err != nil {
		return "", "", false, err
	}
	pathValue = removeDotSegments(pathValue)
	query, queryErr := canonicalEscapes(parsed.RawQuery)
	if queryErr != nil {
		return "", "", false, queryErr
	}
	canonical = scheme + "://" + canonicalHost + pathValue
	if parsed.ForceQuery || query != "" {
		canonical += "?" + query
	}
	return canonical, canonicalDomainHost, true, nil
}

func canonicalAuthoredHost(hostname string) (urlHost, domainHost string, err error) {
	if hostname == "" || strings.HasSuffix(hostname, ".") {
		return "", "", fmt.Errorf("invalid URL host")
	}
	if address, parseErr := netip.ParseAddr(hostname); parseErr == nil {
		if address.Zone() != "" {
			return "", "", fmt.Errorf("invalid URL host")
		}
		address = address.Unmap()
		domainHost = address.String()
		urlHost = domainHost
		if address.Is6() {
			urlHost = "[" + urlHost + "]"
		}
		return urlHost, domainHost, nil
	}
	domainHost, err = canonicalDomain(hostname)
	return domainHost, domainHost, err
}

func looksLikeHTTP(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.HasPrefix(lower, "http:") || strings.HasPrefix(lower, "https:")
}
