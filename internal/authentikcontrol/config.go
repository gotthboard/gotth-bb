package authentikcontrol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	maximumTokenBytes   = 4096
	maximumObjectsBytes = 8192
)

var (
	canonicalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	canonicalSlug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

// Object identifies one immutable Authentik flow.
type Object struct {
	Slug string `json:"slug"`
	UUID string `json:"uuid"`
}

// FlowObjects contains only the three admitted enrollment flows.
type FlowObjects struct {
	Open       Object `json:"open"`
	Approval   Object `json:"approval"`
	Invitation Object `json:"invitation"`
}

// GroupObjects contains the exact application-state groups.
type GroupObjects struct {
	Accepted  string `json:"accepted"`
	Pending   string `json:"pending"`
	Suspended string `json:"suspended"`
}

// Objects is the closed, non-secret object descriptor emitted by blueprint admission.
type Objects struct {
	Version      int          `json:"version"`
	IssuerOrigin string       `json:"issuer_origin"`
	Flows        FlowObjects  `json:"flows"`
	Groups       GroupObjects `json:"groups"`
}

// Secret owns the API token and deliberately provides no formatting method.
type Secret struct{ bytes []byte }

// destroy overwrites the owned copy when construction fails or the caller closes it.
func (secret *Secret) destroy() {
	if secret == nil {
		return
	}
	for index := range secret.bytes {
		secret.bytes[index] = 0
	}
	secret.bytes = nil
}

// Load opens the token and object descriptor without following symlinks and
// validates that the descriptor belongs to the exact configured issuer.
func Load(tokenPath, objectsPath, issuer string) (Secret, Objects, error) {
	tokenRaw, err := readImmutableFile(tokenPath, maximumTokenBytes)
	if err != nil || !utf8.Valid(tokenRaw) || len(tokenRaw) == 0 || bytes.ContainsAny(tokenRaw, "\r\x00") || bytes.Count(tokenRaw, []byte{'\n'}) != 0 || strings.TrimSpace(string(tokenRaw)) != string(tokenRaw) {
		clear(tokenRaw)
		return Secret{}, Objects{}, fmt.Errorf("Authentik control token is invalid")
	}
	secret := Secret{bytes: tokenRaw}
	objects, err := LoadObjects(objectsPath, issuer)
	if err != nil {
		secret.destroy()
		return Secret{}, Objects{}, fmt.Errorf("Authentik control objects are invalid")
	}
	return secret, objects, nil
}

// LoadObjects descriptor-opens and validates the non-secret closed object
// document without crossing the gateway-only token boundary.
func LoadObjects(objectsPath, issuer string) (Objects, error) {
	objectsRaw, err := readImmutableFile(objectsPath, maximumObjectsBytes)
	if err != nil || !utf8.Valid(objectsRaw) {
		clear(objectsRaw)
		return Objects{}, fmt.Errorf("Authentik control objects are invalid")
	}
	objects, err := decodeObjects(objectsRaw, issuer)
	clear(objectsRaw)
	if err != nil {
		return Objects{}, fmt.Errorf("Authentik control objects are invalid")
	}
	return objects, nil
}

func readImmutableFile(path string, maximum int64) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, errors.New("invalid path")
	}
	rootFD, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootFD)
	fd, err := unix.Openat2(rootFD, strings.TrimPrefix(path, "/"), &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: uint64(unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS),
	})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "Authentik control file")
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("invalid descriptor")
	}
	defer file.Close()
	var status unix.Stat_t
	writeErr := unix.Faccessat(fd, "", unix.W_OK, unix.AT_EMPTY_PATH|unix.AT_EACCESS)
	if err := unix.Fstat(fd, &status); err != nil || status.Mode&unix.S_IFMT != unix.S_IFREG || status.Mode&0o022 != 0 || writeErr == nil || !errors.Is(writeErr, unix.EACCES) {
		return nil, errors.New("mutable file")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || len(contents) == 0 || int64(len(contents)) > maximum {
		clear(contents)
		return nil, errors.New("invalid file size")
	}
	return contents, nil
}

func decodeObjects(raw []byte, issuer string) (Objects, error) {
	if duplicateObjectKey(raw) {
		return Objects{}, errors.New("duplicate object key")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var objects Objects
	if err := decoder.Decode(&objects); err != nil || requireEOF(decoder) != nil || objects.Version != 1 {
		return Objects{}, errors.New("invalid object document")
	}
	issuerURL, err := url.Parse(issuer)
	if err != nil || !validControlURL(issuerURL) {
		return Objects{}, errors.New("invalid issuer")
	}
	origin := (&url.URL{Scheme: issuerURL.Scheme, Host: issuerURL.Host}).String()
	if objects.IssuerOrigin != origin {
		return Objects{}, errors.New("issuer mismatch")
	}
	flows := []Object{objects.Flows.Open, objects.Flows.Approval, objects.Flows.Invitation}
	identities := make(map[string]struct{}, 6)
	for _, flow := range flows {
		if !canonicalSlug.MatchString(flow.Slug) || !canonicalUUID.MatchString(flow.UUID) {
			return Objects{}, errors.New("invalid flow")
		}
		if _, duplicate := identities[flow.UUID]; duplicate {
			return Objects{}, errors.New("reused identity")
		}
		identities[flow.UUID] = struct{}{}
	}
	for _, group := range []string{objects.Groups.Accepted, objects.Groups.Pending, objects.Groups.Suspended} {
		if !canonicalUUID.MatchString(group) {
			return Objects{}, errors.New("invalid group")
		}
		if _, duplicate := identities[group]; duplicate {
			return Objects{}, errors.New("reused identity")
		}
		identities[group] = struct{}{}
	}
	return objects, nil
}

func validControlURL(parsed *url.URL) bool {
	if parsed == nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	return parsed.Scheme == "http" && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost")
}

func duplicateObjectKey(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var walk func() bool
	walk = func() bool {
		token, err := decoder.Token()
		if err != nil {
			return true
		}
		delimiter, compound := token.(json.Delim)
		if !compound {
			return false
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				key, ok := keyToken.(string)
				if err != nil || !ok {
					return true
				}
				if _, exists := seen[key]; exists {
					return true
				}
				seen[key] = struct{}{}
				if walk() {
					return true
				}
			}
			end, err := decoder.Token()
			return err != nil || end != json.Delim('}')
		case '[':
			for decoder.More() {
				if walk() {
					return true
				}
			}
			end, err := decoder.Token()
			return err != nil || end != json.Delim(']')
		default:
			return true
		}
	}
	if walk() {
		return true
	}
	var trailing any
	return decoder.Decode(&trailing) != io.EOF
}

func requireEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
