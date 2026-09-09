package registration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFingerprintKeyAcceptsExactImmutableRawKey(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "fingerprint.key")
	raw := make([]byte, 32)
	for index := range raw {
		raw[index] = byte(index + 1)
	}
	if err := os.WriteFile(path, raw, 0o400); err != nil {
		t.Fatal(err)
	}
	key, err := LoadFingerprintKey(path)
	if err != nil || key[0] != 1 || key[31] != 32 {
		t.Fatalf("key = (%x, %v)", key, err)
	}
}

func TestLoadFingerprintKeyRejectsUnsafeFiles(t *testing.T) {
	directory := t.TempDir()
	valid := filepath.Join(directory, "valid.key")
	if err := os.WriteFile(valid, make([]byte, 31), 0o400); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(directory, "link.key")
	if err := os.Symlink(valid, symlink); err != nil {
		t.Fatal(err)
	}
	writable := filepath.Join(directory, "writable.key")
	if err := os.WriteFile(writable, make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	zero := filepath.Join(directory, "zero.key")
	if err := os.WriteFile(zero, make([]byte, 32), 0o400); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"relative", symlink, valid, writable, zero} {
		if key, err := LoadFingerprintKey(path); err == nil || key != ([32]byte{}) {
			t.Fatalf("unsafe key %q accepted: (%x, %v)", path, key, err)
		}
	}
}
