package discovery

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
)

const (
	cursorVersion       = byte(1)
	cursorRecordBytes   = 77
	cursorPayloadBytes  = 45
	cursorMaximumAge    = 24 * time.Hour
	cursorFutureSkew    = 60 * time.Second
	cursorDomain        = "gotth-bb/activity-cursor/v1"
	audienceDomain      = "gotth-bb/activity-audience/v1"
	audienceFormat      = byte(1)
	audienceDigestBytes = 16
)

type ActivityBoundary struct {
	CreatedAt time.Time
	PostID    int64
}

type CursorKey struct {
	ID            uint32
	secret        [32]byte
	NotBefore     time.Time
	IssueNotAfter time.Time
}

type CursorKeyring struct {
	Active   CursorKey
	Previous *CursorKey
}

// Format prevents ordinary diagnostics from exposing key bytes.
//
// Complexity: time and auxiliary space are tight Theta(1).
func (CursorKey) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[CURSOR KEY]")
}

// Format prevents recursive diagnostics from traversing either key.
//
// Complexity: time and auxiliary space are tight Theta(1).
func (CursorKeyring) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[CURSOR KEYRING]")
}

type AuthenticatedCursor struct {
	boundary ActivityBoundary
	audience [audienceDigestBytes]byte
	secret   [32]byte
	keyID    uint32
	issuedAt time.Time
	previous bool
	keyEnd   time.Time
}

// Format prevents diagnostics from exposing the selected cursor secret or
// authenticated audience material.
//
// Complexity: time and auxiliary space are tight Theta(1).
func (AuthenticatedCursor) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[AUTHENTICATED CURSOR]")
}

// EncodeCursor emits one canonical authenticated activity boundary using the
// active key only while its issuance interval contains the database sample.
//
// Complexity: with g authority groups, time is O(g), Omega(1), and tight
// Theta(g) for canonical authenticated authority. Auxiliary space is O(1),
// Omega(1), and tight Theta(1); HMAC input is streamed without a group copy.
func (ring CursorKeyring) EncodeCursor(databaseNow time.Time, boundary ActivityBoundary, actor policy.AccessContext) (string, error) {
	if !validCursorKey(ring.Active) || !validMicrosecondTime(databaseNow) ||
		databaseNow.Before(ring.Active.NotBefore) || databaseNow.After(ring.Active.IssueNotAfter) ||
		!validMicrosecondTime(boundary.CreatedAt) || boundary.PostID <= 0 {
		return "", fmt.Errorf("activity cursor cannot be issued")
	}
	audience, err := audienceDigest(ring.Active.secret, actor)
	if err != nil {
		return "", fmt.Errorf("activity audience is invalid")
	}
	record := make([]byte, cursorRecordBytes)
	record[0] = cursorVersion
	binary.BigEndian.PutUint32(record[1:5], ring.Active.ID)
	binary.BigEndian.PutUint64(record[5:13], uint64(databaseNow.Unix()))
	binary.BigEndian.PutUint64(record[13:21], uint64(boundary.CreatedAt.UnixMicro()))
	binary.BigEndian.PutUint64(record[21:29], uint64(boundary.PostID))
	copy(record[29:45], audience[:])
	mac := hmac.New(sha256.New, ring.Active.secret[:])
	_, _ = io.WriteString(mac, cursorDomain)
	_, _ = mac.Write(record[:cursorPayloadBytes])
	copy(record[cursorPayloadBytes:], mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString(record), nil
}

// AuthenticateCursor validates canonical encoding, key identity/window, MAC,
// and database-time lifetime before exposing a value that may bind a session.
//
// Complexity: time and auxiliary space are tight Theta(1): the record is fixed
// at 77 bytes and at most two keys exist. No session or database query occurs.
func (ring CursorKeyring) AuthenticateCursor(encoded string, databaseNow time.Time) (AuthenticatedCursor, error) {
	authenticated, err := ring.VerifyCursor(encoded)
	if err != nil || authenticated.ValidateTime(databaseNow) != nil {
		return AuthenticatedCursor{}, fmt.Errorf("invalid activity cursor")
	}
	return authenticated, nil
}

// VerifyCursor authenticates the fixed record before optional-session work.
// Database-authoritative age checks intentionally remain a separate method.
//
// Complexity: time and auxiliary space are tight Theta(1) over fixed input.
func (ring CursorKeyring) VerifyCursor(encoded string) (AuthenticatedCursor, error) {
	if len(encoded) != EncodedCursorLength {
		return AuthenticatedCursor{}, fmt.Errorf("invalid activity cursor")
	}
	record, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(record) != cursorRecordBytes || base64.RawURLEncoding.EncodeToString(record) != encoded || record[0] != cursorVersion {
		return AuthenticatedCursor{}, fmt.Errorf("invalid activity cursor")
	}
	keyID := binary.BigEndian.Uint32(record[1:5])
	key, previous := ring.key(keyID)
	if key == nil || !validCursorKey(*key) {
		return AuthenticatedCursor{}, fmt.Errorf("invalid activity cursor")
	}
	mac := hmac.New(sha256.New, key.secret[:])
	_, _ = io.WriteString(mac, cursorDomain)
	_, _ = mac.Write(record[:cursorPayloadBytes])
	if subtle.ConstantTimeCompare(record[cursorPayloadBytes:], mac.Sum(nil)) != 1 {
		return AuthenticatedCursor{}, fmt.Errorf("invalid activity cursor")
	}
	issuedAt := time.Unix(int64(binary.BigEndian.Uint64(record[5:13])), 0).UTC()
	createdAt := time.UnixMicro(int64(binary.BigEndian.Uint64(record[13:21]))).UTC()
	postID := int64(binary.BigEndian.Uint64(record[21:29]))
	if !validSecondTime(issuedAt) || !validMicrosecondTime(createdAt) || postID <= 0 ||
		issuedAt.Before(key.NotBefore) || issuedAt.After(key.IssueNotAfter) {
		return AuthenticatedCursor{}, fmt.Errorf("invalid activity cursor")
	}
	authenticated := AuthenticatedCursor{
		boundary: ActivityBoundary{CreatedAt: createdAt, PostID: postID}, secret: key.secret,
		keyID: key.ID, issuedAt: issuedAt, previous: previous, keyEnd: key.IssueNotAfter,
	}
	copy(authenticated.audience[:], record[29:45])
	return authenticated, nil
}

// belongsTo prevents a caller from mixing a verified cursor with a different
// current keyring before database or audience work.
//
// Complexity: time and auxiliary space are tight Theta(1).
func (cursor AuthenticatedCursor) belongsTo(ring CursorKeyring) bool {
	key, previous := ring.key(cursor.keyID)
	return key != nil && previous == cursor.previous && subtle.ConstantTimeCompare(key.secret[:], cursor.secret[:]) == 1 && key.IssueNotAfter.Equal(cursor.keyEnd)
}

// ValidateTime applies database-authoritative cursor age/future and previous-
// key retirement checks after the MAC is already known valid.
//
// Complexity: time and auxiliary space are tight Theta(1).
func (cursor AuthenticatedCursor) ValidateTime(databaseNow time.Time) error {
	if !validMicrosecondTime(databaseNow) || !validSecondTime(cursor.issuedAt) ||
		cursor.issuedAt.After(databaseNow.Add(cursorFutureSkew)) || databaseNow.Sub(cursor.issuedAt) > cursorMaximumAge ||
		cursor.previous && databaseNow.After(cursor.keyEnd.Add(cursorMaximumAge+cursorFutureSkew)) {
		return fmt.Errorf("invalid activity cursor")
	}
	return nil
}

// BindAudience compares the post-MAC cursor audience with one complete
// canonical server-owned authority snapshot.
//
// Complexity: with g groups, time is O(g), Omega(1), and tight Theta(g) for a
// canonical authenticated snapshot; auxiliary space is tight Theta(1).
func (cursor AuthenticatedCursor) BindAudience(actor policy.AccessContext) (ActivityBoundary, error) {
	digest, err := audienceDigest(cursor.secret, actor)
	if err != nil || subtle.ConstantTimeCompare(digest[:], cursor.audience[:]) != 1 {
		return ActivityBoundary{}, fmt.Errorf("invalid activity cursor")
	}
	return cursor.boundary, nil
}

// key resolves an identifier across the fixed active/previous key set.
//
// Complexity: time and auxiliary space are tight Theta(1).
func (ring CursorKeyring) key(id uint32) (*CursorKey, bool) {
	if ring.Active.ID == id {
		return &ring.Active, false
	}
	if ring.Previous != nil && ring.Previous.ID == id {
		return ring.Previous, true
	}
	return nil, false
}

// audienceDigest authenticates one canonical authority snapshot without
// allocating a serialized group list.
//
// Complexity: with g groups, time is tight Theta(g) and auxiliary space is
// tight Theta(1).
func audienceDigest(secret [32]byte, actor policy.AccessContext) ([audienceDigestBytes]byte, error) {
	var result [audienceDigestBytes]byte
	if !actor.Valid() {
		return result, fmt.Errorf("invalid access context")
	}
	previous := int64(0)
	for _, groupID := range actor.GroupIDs {
		if groupID <= previous {
			return result, fmt.Errorf("invalid access context")
		}
		previous = groupID
	}
	mac := hmac.New(sha256.New, secret[:])
	_, _ = io.WriteString(mac, audienceDomain)
	_, _ = mac.Write([]byte{audienceFormat})
	authenticated, role, userID := byte(0), byte(0), int64(0)
	if actor.Authenticated {
		authenticated, userID = 1, actor.UserID
		role = byte(actor.Role)
	}
	_, _ = mac.Write([]byte{authenticated})
	var integer [8]byte
	binary.BigEndian.PutUint64(integer[:], uint64(userID))
	_, _ = mac.Write(integer[:])
	_, _ = mac.Write([]byte{role})
	for _, groupID := range actor.GroupIDs {
		binary.BigEndian.PutUint64(integer[:], uint64(groupID))
		_, _ = mac.Write(integer[:])
	}
	copy(result[:], mac.Sum(nil)[:audienceDigestBytes])
	return result, nil
}

// validCursorKey checks the fixed key metadata invariants.
//
// Complexity: time and auxiliary space are tight Theta(1).
func validCursorKey(key CursorKey) bool {
	return key.ID != 0 && validSecondTime(key.NotBefore) && validSecondTime(key.IssueNotAfter) && !key.IssueNotAfter.Before(key.NotBefore)
}

// validSecondTime checks the exact cursor-key timestamp precision.
//
// Complexity: time and auxiliary space are tight Theta(1).
func validSecondTime(value time.Time) bool {
	return validMicrosecondTime(value) && value.Nanosecond() == 0
}

// validMicrosecondTime checks PostgreSQL-compatible finite UTC precision.
//
// Complexity: time and auxiliary space are tight Theta(1).
func validMicrosecondTime(value time.Time) bool {
	return value.Location() == time.UTC && value.Year() >= 1 && value.Year() <= 9999 && value.Nanosecond()%1_000 == 0
}
