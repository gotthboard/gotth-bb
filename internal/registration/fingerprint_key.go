package registration

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// LoadFingerprintKey descriptor-opens the Board-only raw 256-bit key. It
// rejects every symlink and writable regular file and never includes key bytes
// in an error.
func LoadFingerprintKey(path string) ([32]byte, error) {
	var key [32]byte
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return key, fmt.Errorf("invitation fingerprint key is invalid")
	}
	rootFD, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return key, fmt.Errorf("invitation fingerprint key is invalid")
	}
	defer unix.Close(rootFD)
	fd, err := unix.Openat2(rootFD, strings.TrimPrefix(path, "/"), &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: uint64(unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS),
	})
	if err != nil {
		return key, fmt.Errorf("invitation fingerprint key is invalid")
	}
	file := os.NewFile(uintptr(fd), "invitation fingerprint key")
	if file == nil {
		_ = unix.Close(fd)
		return key, fmt.Errorf("invitation fingerprint key is invalid")
	}
	defer file.Close()
	var status unix.Stat_t
	writeErr := unix.Faccessat(fd, "", unix.W_OK, unix.AT_EMPTY_PATH|unix.AT_EACCESS)
	if err := unix.Fstat(fd, &status); err != nil || status.Mode&unix.S_IFMT != unix.S_IFREG || status.Mode&0o022 != 0 || writeErr == nil || !errors.Is(writeErr, unix.EACCES) {
		return key, fmt.Errorf("invitation fingerprint key is invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(len(key)+1)))
	defer clear(raw)
	if err != nil || len(raw) != len(key) {
		return key, fmt.Errorf("invitation fingerprint key is invalid")
	}
	copy(key[:], raw)
	if key == ([32]byte{}) {
		return [32]byte{}, fmt.Errorf("invitation fingerprint key is invalid")
	}
	return key, nil
}
