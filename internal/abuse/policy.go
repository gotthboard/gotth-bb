// Package abuse owns the bounded AN-05 request and destination policy.
package abuse

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/idna"
	"golang.org/x/sys/unix"
)

const (
	MaximumRulesFileBytes = 65_536
	MaximumRules          = 256
)

// Policy is an immutable canonical blocked-destination set.
type Policy struct {
	domains               []string
	urls                  []string
	requestLimit          uint32
	requestWindow         time.Duration
	requestClientCapacity uint32
	publicationLimit      uint32
	newAccountLimit       uint32
	publicationWindow     time.Duration
	newAccountPeriod      time.Duration
}

// RateProfile is the validated plain-value input copied into an immutable
// Policy. Callers retain no reference-bearing state.
type RateProfile struct {
	RequestLimit          uint32
	RequestWindow         time.Duration
	RequestClientCapacity uint32
	PublicationLimit      uint32
	NewAccountLimit       uint32
	PublicationWindow     time.Duration
	NewAccountPeriod      time.Duration
}

// PublicationProfile returns the four immutable durable-publication values.
func (policy Policy) PublicationProfile() (uint32, uint32, time.Duration, time.Duration) {
	return policy.publicationLimit, policy.newAccountLimit, policy.publicationWindow, policy.newAccountPeriod
}

// LoadPolicy descriptor-opens one bounded immutable rules file and validates
// its complete canonical form without exposing path or content in errors.
func LoadPolicy(path string, profile RateProfile) (Policy, error) {
	if path == "" || len(path) > 4096 || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return Policy{}, fmt.Errorf("abuse rules file is invalid")
	}
	if profile.RequestLimit == 0 || profile.RequestLimit > 100_000 ||
		profile.RequestClientCapacity == 0 || profile.RequestClientCapacity > 65_536 ||
		profile.PublicationLimit == 0 || profile.PublicationLimit > 100_000 ||
		profile.NewAccountLimit == 0 || profile.NewAccountLimit > profile.PublicationLimit ||
		profile.RequestWindow < time.Second || profile.RequestWindow > 24*time.Hour ||
		profile.PublicationWindow < time.Second || profile.PublicationWindow > 24*time.Hour ||
		profile.NewAccountPeriod < time.Minute || profile.NewAccountPeriod > 30*24*time.Hour {
		return Policy{}, fmt.Errorf("abuse rules file is invalid")
	}
	rootFD, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return Policy{}, fmt.Errorf("abuse rules file is invalid")
	}
	defer unix.Close(rootFD)
	fd, err := unix.Openat2(rootFD, strings.TrimPrefix(path, "/"), &unix.OpenHow{
		Flags: uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: uint64(unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS |
			unix.RESOLVE_NO_MAGICLINKS),
	})
	if err != nil {
		return Policy{}, fmt.Errorf("abuse rules file is invalid")
	}
	file := os.NewFile(uintptr(fd), "abuse rules")
	if file == nil {
		_ = unix.Close(fd)
		return Policy{}, fmt.Errorf("abuse rules file is invalid")
	}
	defer file.Close()
	policy, err := loadOpenedPolicy(file)
	if err != nil {
		return Policy{}, fmt.Errorf("abuse rules file is invalid")
	}
	policy.requestLimit = profile.RequestLimit
	policy.requestWindow = profile.RequestWindow
	policy.requestClientCapacity = profile.RequestClientCapacity
	policy.publicationLimit = profile.PublicationLimit
	policy.newAccountLimit = profile.NewAccountLimit
	policy.publicationWindow = profile.PublicationWindow
	policy.newAccountPeriod = profile.NewAccountPeriod
	return policy, nil
}

func loadOpenedPolicy(file *os.File) (Policy, error) {
	if file == nil {
		return Policy{}, fmt.Errorf("invalid opened rules file")
	}
	fd := int(file.Fd())
	var status unix.Stat_t
	writeErr := unix.Faccessat(fd, "", unix.W_OK, unix.AT_EMPTY_PATH|unix.AT_EACCESS)
	if err := unix.Fstat(fd, &status); err != nil || status.Mode&unix.S_IFMT != unix.S_IFREG || status.Mode&0o022 != 0 || writeErr == nil || !errors.Is(writeErr, unix.EACCES) {
		return Policy{}, fmt.Errorf("invalid opened rules file")
	}
	contents, err := io.ReadAll(io.LimitReader(file, MaximumRulesFileBytes+1))
	if err != nil || len(contents) > MaximumRulesFileBytes || !utf8.Valid(contents) {
		return Policy{}, fmt.Errorf("invalid opened rules file")
	}
	policy, err := decodePolicy(string(contents))
	if err != nil {
		return Policy{}, fmt.Errorf("invalid opened rules file")
	}
	return policy, nil
}

func decodePolicy(contents string) (Policy, error) {
	if contents == "" {
		return Policy{}, nil
	}
	if !strings.HasSuffix(contents, "\n") || strings.ContainsAny(contents, "\r\x00") {
		return Policy{}, fmt.Errorf("invalid line encoding")
	}
	lines := strings.Split(strings.TrimSuffix(contents, "\n"), "\n")
	if len(lines) > MaximumRules {
		return Policy{}, fmt.Errorf("too many rules")
	}
	policy := Policy{}
	previous := ""
	for _, line := range lines {
		if line == "" || hasControl(line) || previous != "" && line <= previous {
			return Policy{}, fmt.Errorf("invalid rule order")
		}
		previous = line
		switch {
		case strings.HasPrefix(line, "domain="):
			value := strings.TrimPrefix(line, "domain=")
			canonical, err := canonicalDomain(value)
			if err != nil || canonical != value {
				return Policy{}, fmt.Errorf("invalid domain rule")
			}
			policy.domains = append(policy.domains, canonical)
		case strings.HasPrefix(line, "url="):
			value := strings.TrimPrefix(line, "url=")
			canonical, err := canonicalURL(value)
			if err != nil || canonical != value {
				return Policy{}, fmt.Errorf("invalid URL rule")
			}
			policy.urls = append(policy.urls, canonical)
		default:
			return Policy{}, fmt.Errorf("invalid rule type")
		}
	}
	return policy, nil
}

func hasControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func canonicalDomain(raw string) (string, error) {
	if raw == "" || strings.HasSuffix(raw, ".") {
		return "", fmt.Errorf("invalid domain")
	}
	if _, err := netip.ParseAddr(raw); err == nil {
		return "", fmt.Errorf("domain rule must not be an address")
	}
	ascii, err := idna.Lookup.ToASCII(raw)
	if err != nil {
		return "", fmt.Errorf("invalid IDNA domain")
	}
	ascii = strings.ToLower(ascii)
	if !validDNSName(ascii) {
		return "", fmt.Errorf("invalid DNS name")
	}
	return ascii, nil
}

func validDNSName(host string) bool {
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for index := range len(label) {
			character := label[index]
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func canonicalURL(raw string) (string, error) {
	if strings.Contains(raw, "\\") {
		return "", fmt.Errorf("invalid URL separator")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("invalid URL")
	}
	hostname := parsed.Hostname()
	if hostname == "" || strings.HasSuffix(hostname, ".") {
		return "", fmt.Errorf("invalid URL host")
	}
	canonicalHost := ""
	if address, parseErr := netip.ParseAddr(hostname); parseErr == nil {
		address = address.Unmap()
		canonicalHost = address.String()
		if address.Is6() {
			canonicalHost = "[" + canonicalHost + "]"
		}
	} else {
		canonicalHost, err = canonicalDomain(hostname)
		if err != nil {
			return "", err
		}
	}
	port := parsed.Port()
	if port != "" {
		value, portErr := strconv.ParseUint(port, 10, 16)
		if portErr != nil || value == 0 || strconv.FormatUint(value, 10) != port {
			return "", fmt.Errorf("invalid URL port")
		}
		if !(parsed.Scheme == "http" && port == "80" || parsed.Scheme == "https" && port == "443") {
			canonicalHost += ":" + port
		}
	}
	pathValue := parsed.EscapedPath()
	if pathValue == "" {
		pathValue = "/"
	}
	pathValue, err = canonicalEscapes(pathValue)
	if err != nil {
		return "", err
	}
	pathValue = removeDotSegments(pathValue)
	query, err := canonicalEscapes(parsed.RawQuery)
	if err != nil {
		return "", err
	}
	result := parsed.Scheme + "://" + canonicalHost + pathValue
	if parsed.ForceQuery || query != "" {
		result += "?" + query
	}
	return result, nil
}

func canonicalEscapes(raw string) (string, error) {
	var output strings.Builder
	output.Grow(len(raw))
	const hexadecimal = "0123456789ABCDEF"
	for index := 0; index < len(raw); index++ {
		if raw[index] != '%' {
			output.WriteByte(raw[index])
			continue
		}
		if index+2 >= len(raw) {
			return "", fmt.Errorf("invalid percent escape")
		}
		high := strings.IndexByte(hexadecimal, raw[index+1])
		if high < 0 {
			high = strings.IndexByte("0123456789abcdef", raw[index+1])
		}
		low := strings.IndexByte(hexadecimal, raw[index+2])
		if low < 0 {
			low = strings.IndexByte("0123456789abcdef", raw[index+2])
		}
		if high < 0 || low < 0 {
			return "", fmt.Errorf("invalid percent escape")
		}
		value := byte(high<<4 | low)
		if isUnreserved(value) {
			output.WriteByte(value)
		} else {
			output.WriteByte('%')
			output.WriteByte(hexadecimal[value>>4])
			output.WriteByte(hexadecimal[value&15])
		}
		index += 2
	}
	return output.String(), nil
}

func isUnreserved(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || strings.ContainsRune("-._~", rune(value))
}

func removeDotSegments(raw string) string {
	segments := strings.Split(raw, "/")
	output := make([]string, 0, len(segments))
	for _, segment := range segments {
		switch segment {
		case ".":
		case "..":
			if len(output) > 1 {
				output = output[:len(output)-1]
			}
		default:
			output = append(output, segment)
		}
	}
	result := strings.Join(output, "/")
	if result == "" || result[0] != '/' {
		result = "/" + result
	}
	if strings.HasSuffix(raw, "/.") || strings.HasSuffix(raw, "/..") {
		result += "/"
	}
	return result
}
