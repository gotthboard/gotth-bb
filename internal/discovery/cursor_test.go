package discovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
)

func TestCursorRoundTripAndAudienceBinding(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 7, 18, 0, 0, 123_456_000, time.UTC)
	ring := testKeyring(now)
	actor := policy.AccessContext{Authenticated: true, UserID: 42, Role: policy.RoleMember, GroupIDs: []int64{3, 9}}
	boundary := ActivityBoundary{CreatedAt: now.Add(-time.Minute), PostID: 91}

	encoded, err := ring.EncodeCursor(now, boundary, actor)
	if err != nil {
		t.Fatalf("EncodeCursor() returned error: %v", err)
	}
	if len(encoded) != EncodedCursorLength || strings.Contains(encoded, "=") {
		t.Fatalf("encoded cursor = %q", encoded)
	}
	authenticated, err := ring.AuthenticateCursor(encoded, now)
	if err != nil {
		t.Fatalf("AuthenticateCursor() returned error: %v", err)
	}
	if got, err := authenticated.BindAudience(actor); err != nil || got != boundary {
		t.Fatalf("BindAudience() = %+v, %v", got, err)
	}
	changed := actor
	changed.GroupIDs = []int64{3, 10}
	if _, err := authenticated.BindAudience(changed); err == nil {
		t.Fatal("BindAudience() accepted changed groups")
	}
	changed = actor
	changed.Role = policy.RoleModerator
	if _, err := authenticated.BindAudience(changed); err == nil {
		t.Fatal("BindAudience() accepted changed role")
	}
	changed = actor
	changed.UserID++
	if _, err := authenticated.BindAudience(changed); err == nil {
		t.Fatal("BindAudience() accepted changed user")
	}
}

func TestCursorOccurrenceTimeEndpointsRoundTrip(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	ring := testKeyring(now)
	actor := policy.AccessContext{}
	for _, occurrence := range []time.Time{
		time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
		maximumSearchTime,
	} {
		encoded, err := ring.EncodeCursor(now, ActivityBoundary{CreatedAt: occurrence, PostID: 1}, actor)
		if err != nil {
			t.Fatalf("EncodeCursor(%s) returned error: %v", occurrence, err)
		}
		authenticated, err := ring.AuthenticateCursor(encoded, now)
		if err != nil {
			t.Fatalf("AuthenticateCursor(%s) returned error: %v", occurrence, err)
		}
		boundary, err := authenticated.BindAudience(actor)
		if err != nil || !boundary.CreatedAt.Equal(occurrence) {
			t.Fatalf("round trip %s = %+v, %v", occurrence, boundary, err)
		}
	}
}

func TestCursorRotationAndKeyringIdentity(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	oldRing := testKeyring(now)
	actor := policy.AccessContext{Authenticated: true, UserID: 42, Role: policy.RoleMember}
	encoded, err := oldRing.EncodeCursor(now, ActivityBoundary{CreatedAt: now, PostID: 91}, actor)
	if err != nil {
		t.Fatal(err)
	}
	previous := oldRing.Active
	rotated := CursorKeyring{
		Active:   CursorKey{ID: 8, secret: testKey(8).secret, NotBefore: now, IssueNotAfter: now.Add(time.Hour)},
		Previous: &previous,
	}
	authenticated, err := rotated.AuthenticateCursor(encoded, now)
	if err != nil || !authenticated.belongsTo(rotated) {
		t.Fatalf("rotated AuthenticateCursor() = %+v, %v", authenticated, err)
	}
	if _, err := authenticated.BindAudience(actor); err != nil {
		t.Fatalf("rotated BindAudience() returned error: %v", err)
	}
	if _, err := (CursorKeyring{Active: rotated.Active}).AuthenticateCursor(encoded, now); err == nil {
		t.Fatal("AuthenticateCursor() accepted cursor after previous-key removal")
	}
	wrongPrevious := previous
	wrongPrevious.secret = testKey(9).secret
	if authenticated.belongsTo(CursorKeyring{Active: rotated.Active, Previous: &wrongPrevious}) {
		t.Fatal("belongsTo() accepted a different keyring with the same key ID")
	}
	changedWindow := previous
	changedWindow.NotBefore = changedWindow.NotBefore.Add(time.Second)
	if authenticated.belongsTo(CursorKeyring{Active: rotated.Active, Previous: &changedWindow}) {
		t.Fatal("belongsTo() accepted changed key-window metadata")
	}
}

func TestCursorRejectsMalformedTamperedAndInvalidTimes(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	ring := testKeyring(now)
	actor := policy.AccessContext{}
	encoded, err := ring.EncodeCursor(now, ActivityBoundary{CreatedAt: now, PostID: 1}, actor)
	if err != nil {
		t.Fatal(err)
	}
	tampered := encoded[:len(encoded)-1] + "A"
	if tampered == encoded {
		tampered = encoded[:len(encoded)-1] + "B"
	}
	for _, input := range []string{encoded + "=", encoded[:102], strings.Repeat("!", EncodedCursorLength), tampered} {
		if got, err := ring.AuthenticateCursor(input, now); err == nil {
			t.Fatalf("AuthenticateCursor(%q) = %+v, want error", input, got)
		}
	}
	for _, observed := range []time.Time{now.Add(-61 * time.Second), now.Add(24*time.Hour + time.Microsecond)} {
		if got, err := ring.AuthenticateCursor(encoded, observed); err == nil {
			t.Fatalf("AuthenticateCursor at %s = %+v, want error", observed, got)
		}
	}
	if _, err := ring.EncodeCursor(now, ActivityBoundary{CreatedAt: time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), PostID: 1}, actor); err == nil {
		t.Fatal("EncodeCursor() accepted year zero occurrence")
	}
}

func TestAudienceDigestRejectsNoncanonicalAuthority(t *testing.T) {
	t.Parallel()

	key := testKey(1)
	for _, actor := range []policy.AccessContext{
		{Authenticated: true, UserID: 1, Role: policy.RoleMember, GroupIDs: []int64{2, 2}},
		{Authenticated: true, UserID: 1, Role: policy.RoleMember, GroupIDs: []int64{2, 1}},
		{Authenticated: true, UserID: 1, Role: policy.RoleMember, GroupIDs: []int64{0}},
	} {
		if _, err := audienceDigest(key.secret, actor); err == nil {
			t.Fatalf("audienceDigest(%+v) accepted invalid authority", actor)
		}
	}
}

func TestLoadCursorKeyringStrictFileAndJSON(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "keyring.json")
	json := validKeyringJSON()
	if err := os.WriteFile(path, []byte(json), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := LoadCursorKeyring(path)
	if err != nil || ring.Active.ID != 7 {
		t.Fatalf("LoadCursorKeyring() = %+v, %v", ring, err)
	}

	symlink := filepath.Join(directory, "link")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCursorKeyring(symlink); err == nil {
		t.Fatal("LoadCursorKeyring() followed symlink")
	}
	realDirectory := filepath.Join(directory, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	ancestorFile := filepath.Join(realDirectory, "ancestor-keyring.json")
	if err := os.WriteFile(ancestorFile, []byte(json), 0o400); err != nil {
		t.Fatal(err)
	}
	ancestorLink := filepath.Join(directory, "ancestor-link")
	if err := os.Symlink(realDirectory, ancestorLink); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCursorKeyring(filepath.Join(ancestorLink, "ancestor-keyring.json")); err == nil {
		t.Fatal("LoadCursorKeyring() followed ancestor symlink")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCursorKeyring(path); err == nil {
		t.Fatal("LoadCursorKeyring() accepted writable file")
	}
}

func TestLoadCursorKeyringRejectsSchemaDrift(t *testing.T) {
	t.Parallel()

	valid := validKeyringJSON()
	for index, contents := range []string{
		strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(valid, `"previous":null`, `"unknown":null,"previous":null`, 1),
		valid + `{}`,
		strings.Replace(valid, `"id":7`, `"id":0`, 1),
		strings.Replace(valid, `AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE`, `bad=`, 1),
		strings.Replace(valid, `2026-09-07T00:00:00Z`, `2026-09-07T00:00:00.1Z`, 1),
		strings.Replace(valid, `2026-09-08T00:00:00Z`, `9999-12-31T23:59:59Z`, 1),
		strings.Replace(valid, `"previous":null`, strings.Replace(`"previous":{"id":7,"key":"KEY","not_before":"2026-09-07T00:00:00Z","issue_not_after":"2026-09-08T00:00:00Z"}`, "KEY", "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE", 1), 1),
	} {
		path := filepath.Join(t.TempDir(), "keyring.json")
		if err := os.WriteFile(path, []byte(contents), 0o400); err != nil {
			t.Fatal(err)
		}
		if got, err := LoadCursorKeyring(path); err == nil {
			t.Fatalf("case %d LoadCursorKeyring() = %+v, want error", index, got)
		}
	}
	directory := t.TempDir()
	if got, err := LoadCursorKeyring(directory); err == nil {
		t.Fatalf("LoadCursorKeyring(directory) = %+v, want error", got)
	}
	oversized := filepath.Join(t.TempDir(), "oversized.json")
	if err := os.WriteFile(oversized, []byte(strings.Repeat(" ", maximumCursorKeyringBytes+1)), 0o400); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadCursorKeyring(oversized); err == nil {
		t.Fatalf("LoadCursorKeyring(oversized) = %+v, want error", got)
	}
}

func TestLoadCursorKeyringAcceptsPreviousKey(t *testing.T) {
	t.Parallel()

	previous := `{"id":6,"key":"AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI","not_before":"2026-09-06T00:00:00Z","issue_not_after":"2026-09-07T12:00:00Z"}`
	contents := strings.Replace(validKeyringJSON(), `"previous":null`, `"previous":`+previous, 1)
	path := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(path, []byte(contents), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := LoadCursorKeyring(path)
	if err != nil || ring.Previous == nil || ring.Previous.ID != 6 {
		t.Fatalf("LoadCursorKeyring() = %+v, %v", ring, err)
	}
}

func validKeyringJSON() string {
	return `{"version":1,"active":{"id":7,"key":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE","not_before":"2026-09-07T00:00:00Z","issue_not_after":"2026-09-08T00:00:00Z"},"previous":null}`
}

func testKeyring(now time.Time) CursorKeyring {
	now = now.Truncate(time.Second)
	return CursorKeyring{Active: CursorKey{ID: 7, secret: testKey(7).secret, NotBefore: now.Add(-time.Hour), IssueNotAfter: now.Add(time.Hour)}}
}

func testKey(seed byte) CursorKey {
	var key [32]byte
	for index := range key {
		key[index] = seed
	}
	return CursorKey{ID: uint32(seed), secret: key}
}
