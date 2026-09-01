package auth

import (
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestGenerateLoginMaterialReadsIndependent256BitValues(t *testing.T) {
	t.Parallel()

	raw := make([]byte, 96)
	for index := range raw {
		raw[index] = byte(index)
	}
	material, err := generateLoginMaterial(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("generateLoginMaterial() returned error: %v", err)
	}
	want := loginMaterial{
		state:        base64.RawURLEncoding.EncodeToString(raw[:32]),
		nonce:        base64.RawURLEncoding.EncodeToString(raw[32:64]),
		pkceVerifier: base64.RawURLEncoding.EncodeToString(raw[64:]),
	}
	if material != want {
		t.Fatalf("login material did not preserve the three independent entropy blocks")
	}
	for name, value := range map[string]string{"state": material.state, "nonce": material.nonce, "PKCE verifier": material.pkceVerifier} {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil || len(decoded) != 32 || len(value) != 43 {
			t.Errorf("%s = %d encoded/%d decoded bytes, decode error %v", name, len(value), len(decoded), err)
		}
	}
}

func TestGenerateLoginMaterialRejectsMissingOrFailedEntropy(t *testing.T) {
	t.Parallel()

	cause := errors.New("entropy failed")
	for _, test := range []struct {
		name    string
		reader  io.Reader
		wantErr error
	}{
		{name: "nil reader"},
		{name: "short reader", reader: strings.NewReader(strings.Repeat("x", 95)), wantErr: io.ErrUnexpectedEOF},
		{name: "reader failure", reader: errReader{cause: cause}, wantErr: cause},
		{name: "repeated values", reader: strings.NewReader(strings.Repeat("x", 96))},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			material, err := generateLoginMaterial(test.reader)
			if err == nil || material != (loginMaterial{}) {
				t.Fatalf("generateLoginMaterial() returned zero material = %v, error = %v", material == (loginMaterial{}), err)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want cause %v", err, test.wantErr)
			}
		})
	}
}

type errReader struct {
	cause error
}

func (reader errReader) Read([]byte) (int, error) {
	return 0, reader.cause
}
