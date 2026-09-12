package httpui

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"time"
)

const (
	administrationSessionActionKeyDomain = "gotth-bb/administration-session-action-key/v1"
	administrationSessionHandleDomain    = "gotth-bb/administration-session-revoke-handle/v1\x00"
	administrationSessionHandleBytes     = 65
	administrationSessionHandleLifetime  = 5 * time.Minute
)

type administrationSessionActionKeyContextKey struct{}

func deriveAdministrationSessionActionKey(sessionToken string) ([32]byte, error) {
	if len(sessionToken) != sessionCookieEncodedBytes {
		return [32]byte{}, fmt.Errorf("administration session credential has an invalid length")
	}
	var encoded [sessionCookieEncodedBytes]byte
	copy(encoded[:], sessionToken)
	defer clear(encoded[:])
	var decoded [sessionCookieTokenBytes]byte
	decodedLength, err := base64.RawURLEncoding.Strict().Decode(decoded[:], encoded[:])
	defer clear(decoded[:])
	if err != nil || decodedLength != sessionCookieTokenBytes {
		return [32]byte{}, fmt.Errorf("administration session credential has an invalid encoding")
	}
	mac := hmac.New(sha256.New, decoded[:])
	_, _ = mac.Write([]byte(administrationSessionActionKeyDomain))
	digest := mac.Sum(nil)
	defer clear(digest)
	var key [32]byte
	copy(key[:], digest)
	return key, nil
}

func administrationSessionActionKeyFromContext(ctx context.Context) [32]byte {
	if ctx == nil {
		return [32]byte{}
	}
	key, _ := ctx.Value(administrationSessionActionKeyContextKey{}).([32]byte)
	return key
}

func issueAdministrationSessionHandle(now time.Time, key [32]byte, actorUserID, targetUserID, sessionID int64) (string, error) {
	if now.IsZero() || key == ([32]byte{}) || actorUserID <= 0 || targetUserID <= 0 || sessionID <= 0 {
		return "", fmt.Errorf("administration session handle input is invalid")
	}
	var raw [administrationSessionHandleBytes]byte
	raw[0] = 1
	binary.BigEndian.PutUint64(raw[1:9], uint64(now.UTC().Truncate(time.Second).Add(administrationSessionHandleLifetime).Unix()))
	binary.BigEndian.PutUint64(raw[9:17], uint64(actorUserID))
	binary.BigEndian.PutUint64(raw[17:25], uint64(targetUserID))
	binary.BigEndian.PutUint64(raw[25:33], uint64(sessionID))
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte(administrationSessionHandleDomain))
	_, _ = mac.Write(raw[:33])
	copy(raw[33:], mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func verifyAdministrationSessionHandle(encoded string, now time.Time, key [32]byte, actorUserID int64) (int64, int64, error) {
	if len(encoded) != base64.RawURLEncoding.EncodedLen(administrationSessionHandleBytes) || now.IsZero() || key == ([32]byte{}) || actorUserID <= 0 {
		return 0, 0, fmt.Errorf("administration session handle input is invalid")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) != administrationSessionHandleBytes || base64.RawURLEncoding.EncodeToString(raw) != encoded || raw[0] != 1 {
		return 0, 0, fmt.Errorf("administration session handle is malformed")
	}
	defer clear(raw)
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte(administrationSessionHandleDomain))
	_, _ = mac.Write(raw[:33])
	if !hmac.Equal(raw[33:], mac.Sum(nil)) {
		return 0, 0, fmt.Errorf("administration session handle is invalid")
	}
	expires := int64(binary.BigEndian.Uint64(raw[1:9]))
	boundActor := int64(binary.BigEndian.Uint64(raw[9:17]))
	targetUserID := int64(binary.BigEndian.Uint64(raw[17:25]))
	sessionID := int64(binary.BigEndian.Uint64(raw[25:33]))
	if expires <= 0 || boundActor != actorUserID || targetUserID <= 0 || sessionID <= 0 ||
		now.UTC().Unix() > expires || time.Unix(expires, 0).After(now.UTC().Add(administrationSessionHandleLifetime)) {
		return 0, 0, fmt.Errorf("administration session handle is invalid")
	}
	return targetUserID, sessionID, nil
}
