//go:build linux && amd64

package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCapturedInputRemainsOriginalAfterCoordinatedPathReplacement(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	const relativePath = "runner.sh"
	original := []byte("#!/usr/bin/bash\nprintf '%s\\n' original\n")
	replacement := []byte("#!/usr/bin/bash\nprintf '%s\\n' replacement\n")
	path := filepath.Join(repository, relativePath)
	if err := os.WriteFile(path, original, 0o755); err != nil {
		t.Fatalf("write original runner: %v", err)
	}
	digest := sha256.Sum256(original)
	sealed, err := captureCriticalFile(repository, fileIdentity{
		relativePath: relativePath,
		size:         int64(len(original)),
		sha256:       fmt.Sprintf("%x", digest),
		mode:         0o755,
	})
	if err != nil {
		t.Fatalf("captureCriticalFile() returned error: %v", err)
	}
	t.Cleanup(func() { _ = sealed.Close() })

	replace := make(chan struct{})
	replaced := make(chan error, 1)
	go func() {
		<-replace
		next := path + ".replacement"
		if err := os.WriteFile(next, replacement, 0o755); err != nil {
			replaced <- err
			return
		}
		replaced <- os.Rename(next, path)
	}()
	close(replace)
	if err := <-replaced; err != nil {
		t.Fatalf("replace captured runner pathname: %v", err)
	}

	if _, err := sealed.WriteAt([]byte("x"), 0); err == nil {
		t.Fatal("sealed input accepted a write after capture")
	}
	const requiredSeals = unix.F_SEAL_SEAL | unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE
	seals, err := unix.FcntlInt(sealed.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&requiredSeals != requiredSeals {
		t.Fatalf("sealed input state = (%#x, %v), want %#x/nil", seals, err, requiredSeals)
	}
	if _, err := sealed.Seek(0, 0); err != nil {
		t.Fatalf("rewind sealed runner: %v", err)
	}
	command := exec.Command("/usr/bin/bash", "--noprofile", "--norc", "-p", "/proc/self/fd/3")
	command.Env = []string{"PATH=/usr/bin:/bin"}
	command.ExtraFiles = []*os.File{sealed}
	output, err := command.Output()
	if err != nil {
		t.Fatalf("execute sealed runner: %v", err)
	}
	if string(output) != "original\n" {
		t.Fatalf("sealed runner output = %q, want original", output)
	}
	live, err := os.ReadFile(path)
	if err != nil || string(live) != string(replacement) {
		t.Fatalf("replacement pathname = (%q, %v), want replacement/nil", live, err)
	}
}
