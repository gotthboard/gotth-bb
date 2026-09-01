package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"testing"
)

func TestGenerateSessionMaterialReturnsOpaqueTokenAndExactHash(t *testing.T) {
	t.Parallel()

	raw := sequentialBytes(sessionTokenBytes)
	got, err := generateSessionMaterial(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("generateSessionMaterial() returned error: %v", err)
	}
	wantToken := base64.RawURLEncoding.EncodeToString(raw)
	wantHash := sha256.Sum256([]byte(wantToken))
	if got.token != wantToken || got.tokenHash != wantHash || len(got.token) != 43 {
		t.Fatalf("generateSessionMaterial() = %+v", got)
	}
}

func TestGenerateSessionMaterialRejectsEntropyFailureWithoutPartialResult(t *testing.T) {
	t.Parallel()

	cause := errors.New("entropy unavailable")
	for _, test := range []struct {
		reader  io.Reader
		wantErr error
	}{{reader: errReader{cause: cause}, wantErr: cause}, {reader: bytes.NewReader(make([]byte, sessionTokenBytes-1)), wantErr: io.ErrUnexpectedEOF}} {
		got, err := generateSessionMaterial(test.reader)
		if !errors.Is(err, test.wantErr) || got != (sessionMaterial{}) {
			t.Fatalf("generateSessionMaterial() = (%+v, %v), want zero/%v", got, err, test.wantErr)
		}
	}
	if got, err := generateSessionMaterial(nil); err == nil || got != (sessionMaterial{}) {
		t.Fatalf("generateSessionMaterial(nil) = (%+v, %v), want zero/error", got, err)
	}
}
