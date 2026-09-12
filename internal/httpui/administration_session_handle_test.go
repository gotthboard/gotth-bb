package httpui

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
	"time"
)

func TestAdministrationSessionHandleBindsAuthorityTargetAndSession(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 12, 19, 0, 0, 0, time.UTC)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x61}, sessionCookieTokenBytes))
	key, err := deriveAdministrationSessionActionKey(token)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := issueAdministrationSessionHandle(now, key, 7, 11, 13)
	if err != nil {
		t.Fatal(err)
	}
	if len(handle) != 87 {
		t.Fatalf("handle length = %d, want 87", len(handle))
	}
	target, session, err := verifyAdministrationSessionHandle(handle, now.Add(administrationSessionHandleLifetime), key, 7)
	if err != nil || target != 11 || session != 13 {
		t.Fatalf("verification = (%d, %d, %v)", target, session, err)
	}

	tampered := []byte(handle)
	if tampered[len(tampered)-1] == 'A' {
		tampered[len(tampered)-1] = 'B'
	} else {
		tampered[len(tampered)-1] = 'A'
	}
	otherToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x62}, sessionCookieTokenBytes))
	otherKey, err := deriveAdministrationSessionActionKey(otherToken)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		handle string
		now    time.Time
		key    [32]byte
		actor  int64
	}{
		{name: "tampered", handle: string(tampered), now: now, key: key, actor: 7},
		{name: "other session", handle: handle, now: now, key: otherKey, actor: 7},
		{name: "other actor", handle: handle, now: now, key: key, actor: 8},
		{name: "expired", handle: handle, now: now.Add(administrationSessionHandleLifetime + time.Second), key: key, actor: 7},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if target, session, err := verifyAdministrationSessionHandle(test.handle, test.now, test.key, test.actor); err == nil || target != 0 || session != 0 {
				t.Fatalf("verification = (%d, %d, %v), want zero/error", target, session, err)
			}
		})
	}
}

func TestAdministrationSessionActionKeyRejectsMalformedCredentialAndContext(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"", "short", base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x71}, sessionCookieTokenBytes-1))} {
		if key, err := deriveAdministrationSessionActionKey(token); err == nil || key != ([32]byte{}) {
			t.Fatalf("derive key for %q = (%x, %v)", token, key, err)
		}
	}
	if key := administrationSessionActionKeyFromContext(nil); key != ([32]byte{}) {
		t.Fatalf("nil context key = %x", key)
	}
	want := [32]byte{1, 2, 3}
	ctx := context.WithValue(context.Background(), administrationSessionActionKeyContextKey{}, want)
	if got := administrationSessionActionKeyFromContext(ctx); got != want {
		t.Fatalf("context key = %x, want %x", got, want)
	}
	for _, test := range []struct {
		now                    time.Time
		key                    [32]byte
		actor, target, session int64
	}{
		{key: want, actor: 1, target: 2, session: 3},
		{now: time.Now(), actor: 1, target: 2, session: 3},
		{now: time.Now(), key: want, target: 2, session: 3},
		{now: time.Now(), key: want, actor: 1, session: 3},
		{now: time.Now(), key: want, actor: 1, target: 2},
	} {
		if handle, err := issueAdministrationSessionHandle(test.now, test.key, test.actor, test.target, test.session); err == nil || handle != "" {
			t.Fatalf("invalid issue = (%q, %v)", handle, err)
		}
	}
}
