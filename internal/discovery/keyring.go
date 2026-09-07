package discovery

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const maximumCursorKeyringBytes = 1_024

// LoadCursorKeyring opens one immutable regular file without following its
// final symlink, verifies non-writability from the opened descriptor, and
// decodes the closed JSON schema without exposing bytes in errors.
//
// Complexity: for n <= 1,024 bytes, time and auxiliary space are O(n),
// Omega(1), and tight Theta(n) on valid input. It performs one open/fstat/read
// sequence and starts no background work.
func LoadCursorKeyring(path string) (CursorKeyring, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return CursorKeyring{}, fmt.Errorf("activity cursor keyring is invalid")
	}
	rootFD, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return CursorKeyring{}, fmt.Errorf("activity cursor keyring is invalid")
	}
	defer unix.Close(rootFD)
	fd, err := unix.Openat2(rootFD, strings.TrimPrefix(path, "/"), &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: uint64(unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS),
	})
	if err != nil {
		return CursorKeyring{}, fmt.Errorf("activity cursor keyring is invalid")
	}
	file := os.NewFile(uintptr(fd), "activity cursor keyring")
	if file == nil {
		_ = unix.Close(fd)
		return CursorKeyring{}, fmt.Errorf("activity cursor keyring is invalid")
	}
	defer file.Close()
	var status unix.Stat_t
	writeAccessErr := unix.Faccessat(fd, "", unix.W_OK, unix.AT_EMPTY_PATH|unix.AT_EACCESS)
	if err := unix.Fstat(fd, &status); err != nil || status.Mode&unix.S_IFMT != unix.S_IFREG || status.Mode&0o022 != 0 || writeAccessErr == nil || !errors.Is(writeAccessErr, unix.EACCES) {
		return CursorKeyring{}, fmt.Errorf("activity cursor keyring is invalid")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximumCursorKeyringBytes+1))
	if err != nil || len(contents) == 0 || len(contents) > maximumCursorKeyringBytes || !utf8.Valid(contents) {
		return CursorKeyring{}, fmt.Errorf("activity cursor keyring is invalid")
	}
	ring, err := decodeCursorKeyring(contents)
	if err != nil {
		return CursorKeyring{}, fmt.Errorf("activity cursor keyring is invalid")
	}
	return ring, nil
}

// decodeCursorKeyring parses the closed top-level schema and validates the
// fixed active/previous relationship.
//
// Complexity: for n <= 1,024 bytes, time and auxiliary space are tight
// Theta(n).
func decodeCursorKeyring(contents []byte) (CursorKeyring, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return CursorKeyring{}, fmt.Errorf("invalid keyring object")
	}
	seen := make(map[string]bool, 3)
	var ring CursorKeyring
	version := json.Number("")
	for decoder.More() {
		nameToken, err := decoder.Token()
		name, ok := nameToken.(string)
		if err != nil || !ok || seen[name] {
			return CursorKeyring{}, fmt.Errorf("invalid keyring field")
		}
		seen[name] = true
		switch name {
		case "version":
			if err := decoder.Decode(&version); err != nil {
				return CursorKeyring{}, err
			}
		case "active":
			key, err := decodeCursorKeyObject(decoder)
			if err != nil {
				return CursorKeyring{}, err
			}
			ring.Active = key
		case "previous":
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return CursorKeyring{}, err
			}
			if !bytes.Equal(raw, []byte("null")) {
				previousDecoder := json.NewDecoder(bytes.NewReader(raw))
				previousDecoder.UseNumber()
				key, err := decodeCursorKeyObject(previousDecoder)
				if err != nil || requireJSONEOF(previousDecoder) != nil {
					return CursorKeyring{}, fmt.Errorf("invalid previous key")
				}
				ring.Previous = &key
			}
		default:
			return CursorKeyring{}, fmt.Errorf("unknown keyring field")
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') || requireJSONEOF(decoder) != nil ||
		len(seen) != 3 || !seen["version"] || !seen["active"] || !seen["previous"] || version.String() != "1" ||
		!validCursorKey(ring.Active) || ring.Previous != nil && (!validCursorKey(*ring.Previous) || ring.Previous.ID == ring.Active.ID) {
		return CursorKeyring{}, fmt.Errorf("invalid keyring")
	}
	return ring, nil
}

// decodeCursorKeyObject parses one closed four-field key object.
//
// Complexity: input is bounded by the 1,024-byte parent document, so time and
// auxiliary space are O(n), Omega(1), and tight Theta(n) for a valid key.
func decodeCursorKeyObject(decoder *json.Decoder) (CursorKey, error) {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return CursorKey{}, fmt.Errorf("invalid cursor key object")
	}
	seen := make(map[string]bool, 4)
	var id json.Number
	var encoded, notBeforeRaw, issueNotAfterRaw string
	for decoder.More() {
		nameToken, err := decoder.Token()
		name, ok := nameToken.(string)
		if err != nil || !ok || seen[name] {
			return CursorKey{}, fmt.Errorf("invalid cursor key field")
		}
		seen[name] = true
		switch name {
		case "id":
			err = decoder.Decode(&id)
		case "key":
			err = decoder.Decode(&encoded)
		case "not_before":
			err = decoder.Decode(&notBeforeRaw)
		case "issue_not_after":
			err = decoder.Decode(&issueNotAfterRaw)
		default:
			return CursorKey{}, fmt.Errorf("unknown cursor key field")
		}
		if err != nil {
			return CursorKey{}, err
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') || len(seen) != 4 {
		return CursorKey{}, fmt.Errorf("invalid cursor key")
	}
	parsedID, err := strconv.ParseUint(id.String(), 10, 32)
	if err != nil || parsedID == 0 || strconv.FormatUint(parsedID, 10) != id.String() {
		return CursorKey{}, fmt.Errorf("invalid cursor key id")
	}
	secretBytes, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(secretBytes) != 32 || base64.RawURLEncoding.EncodeToString(secretBytes) != encoded {
		return CursorKey{}, fmt.Errorf("invalid cursor key bytes")
	}
	notBefore, err := parseCursorKeyTime(notBeforeRaw)
	if err != nil {
		return CursorKey{}, err
	}
	issueNotAfter, err := parseCursorKeyTime(issueNotAfterRaw)
	if err != nil || issueNotAfter.Before(notBefore) || issueNotAfter.After(maximumSearchTime.Add(-cursorMaximumAge-cursorFutureSkew)) {
		return CursorKey{}, fmt.Errorf("invalid cursor key window")
	}
	key := CursorKey{ID: uint32(parsedID), NotBefore: notBefore, IssueNotAfter: issueNotAfter}
	copy(key.secret[:], secretBytes)
	return key, nil
}

// parseCursorKeyTime accepts canonical UTC RFC3339 seconds only.
//
// Complexity: time and auxiliary space are tight Theta(1) over fixed-width
// accepted timestamps.
func parseCursorKeyTime(raw string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil || parsed.Location() != time.UTC || parsed.Nanosecond() != 0 || parsed.Format(time.RFC3339) != raw || !validSecondTime(parsed) {
		return time.Time{}, fmt.Errorf("invalid cursor key time")
	}
	return parsed, nil
}

// requireJSONEOF rejects every trailing JSON value.
//
// Complexity: time and auxiliary space are O(n), Omega(1), and tight Theta(n)
// when trailing material exists; n is bounded by the 1,024-byte document.
func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
