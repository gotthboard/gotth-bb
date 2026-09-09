package postgresql_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fakeDocker = `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$FAKE_DOCKER_LOG"
block() {
  [ "${FAKE_BLOCK_STAGE:-}" != "$1" ] || {
    printf '%s\n' started >"$FAKE_BLOCK_MARKER"
    trap 'printf "%s\n" stopped >"$FAKE_BLOCK_STOPPED"; exit 143' HUP INT TERM
    while :; do sleep 1; done
  }
}
case "$*" in
  *pg_dump*)
    block dump
    [ "${FAKE_DUMP_FAIL:-0}" = 0 ] || exit 71
    if [ "${FAKE_DUMP_EMPTY:-0}" = 0 ]; then
      printf '%s' "${FAKE_ARCHIVE_CONTENT:-fake-custom-archive}"
    fi
    ;;
  *"pg_restore --list"*)
    cat >"$FAKE_LIST_INPUT"
    [ "${FAKE_LIST_FAIL:-0}" = 0 ] || exit 72
    ;;
  *"postgres --version"*)
    printf '%s\n' "${FAKE_POSTGRES_VERSION:-postgres (PostgreSQL) 17.10}"
    ;;
  *"test -n"*)
    [ "${FAKE_IDENTITY_FAIL:-0}" = 0 ] || exit 73
    ;;
  *psql*)
    [ "${FAKE_INSPECT_FAIL:-0}" = 0 ] || exit 74
    printf '%s\n' "${FAKE_RELATIONS:-0}"
    ;;
  *"pg_restore --username="*)
    block restore
    cat >"$FAKE_RESTORE_INPUT"
    [ "${FAKE_RESTORE_FAIL:-0}" = 0 ] || exit 75
    ;;
  *)
    printf '%s\n' 'unexpected docker invocation' >&2
    exit 76
    ;;
esac
`

func TestLogicalHelpersHaveValidShellSyntax(t *testing.T) {
	for _, name := range []string{"backup-logical.sh", "restore-logical.sh"} {
		command := exec.Command("sh", "-n", helperPath(t, name))
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("sh -n %s: %v\n%s", name, err, output)
		}
	}
}

func TestBackupLogicalAdmitsOnlyValidatedAtomicPair(t *testing.T) {
	fixture := newFakeDocker(t)
	archive := filepath.Join(t.TempDir(), "alpha 2.dump")
	result := fixture.run(t, "backup-logical.sh", nil, "gotth-bb-postgresql-17", archive)
	if result.err != nil {
		t.Fatalf("backup failed: %v\n%s", result.err, result.output)
	}
	content := mustRead(t, archive)
	if string(content) != "fake-custom-archive" || string(mustRead(t, fixture.listInput)) != string(content) {
		t.Fatalf("archive/list input mismatch: archive=%q list=%q", content, mustRead(t, fixture.listInput))
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	if got := string(mustRead(t, archive+".sha256")); got != digest+"\n" {
		t.Fatalf("sidecar = %q, want %q", got, digest+"\n")
	}
	info, err := os.Stat(archive)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("archive mode = %v, err %v", info.Mode().Perm(), err)
	}
	if !strings.Contains(result.output, "archive=alpha 2.dump bytes=19 sha256="+digest) || strings.Contains(result.output, "secret-sentinel") {
		t.Fatalf("unexpected bounded output: %q", result.output)
	}
	log := string(mustRead(t, fixture.log))
	for _, required := range []string{"--format=custom", "--no-privileges", "--serializable-deferrable", "--lock-wait-timeout=5s", "pg_restore --list"} {
		if !strings.Contains(log, required) {
			t.Fatalf("docker log lacks %q: %s", required, log)
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(filepath.Dir(archive), ".alpha 2.dump.*")); len(matches) != 0 {
		t.Fatalf("temporary files remain: %q", matches)
	}
}

func TestBackupLogicalRejectsUnsafeInputsAndFailures(t *testing.T) {
	tests := []struct {
		name      string
		container string
		archive   func(t *testing.T) string
		env       map[string]string
		prepare   func(t *testing.T, path string)
		want      string
	}{
		{name: "relative", container: "database", archive: func(*testing.T) string { return "backup.dump" }, want: "archive path must be absolute"},
		{name: "container", container: "bad/name", archive: cleanArchive, want: "invalid container name"},
		{name: "existing", container: "database", archive: cleanArchive, prepare: func(t *testing.T, path string) { mustWrite(t, path, []byte("old"), 0o600) }, want: "already exists"},
		{name: "symlink", container: "database", archive: cleanArchive, prepare: func(t *testing.T, path string) {
			if err := os.Symlink("missing", path); err != nil {
				t.Fatal(err)
			}
		}, want: "already exists"},
		{name: "dump failure", container: "database", archive: cleanArchive, env: map[string]string{"FAKE_DUMP_FAIL": "1"}, want: "database dump failed"},
		{name: "empty dump", container: "database", archive: cleanArchive, env: map[string]string{"FAKE_DUMP_EMPTY": "1"}, want: "database dump was empty"},
		{name: "list failure", container: "database", archive: cleanArchive, env: map[string]string{"FAKE_LIST_FAIL": "1"}, want: "archive validation failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFakeDocker(t)
			archive := test.archive(t)
			if test.prepare != nil {
				test.prepare(t, archive)
			}
			result := fixture.run(t, "backup-logical.sh", test.env, test.container, archive)
			if result.err == nil || !strings.Contains(result.output, test.want) {
				t.Fatalf("result = (%v, %q), want failure containing %q", result.err, result.output, test.want)
			}
			if test.prepare == nil && filepath.IsAbs(archive) {
				if _, err := os.Lstat(archive); !os.IsNotExist(err) {
					t.Fatalf("failed backup admitted archive: %v", err)
				}
				if _, err := os.Lstat(archive + ".sha256"); !os.IsNotExist(err) {
					t.Fatalf("failed backup admitted sidecar: %v", err)
				}
			}
		})
	}
}

func TestRestoreLogicalValidatesBeforeSingleTransactionRestore(t *testing.T) {
	fixture := newFakeDocker(t)
	archive := cleanArchive(t)
	writeArchivePair(t, archive, []byte("restorable-custom-archive"))
	result := fixture.run(t, "restore-logical.sh", nil, "clean-postgresql-17", archive)
	if result.err != nil {
		t.Fatalf("restore failed: %v\n%s", result.err, result.output)
	}
	if string(mustRead(t, fixture.listInput)) != "restorable-custom-archive" || string(mustRead(t, fixture.restoreInput)) != "restorable-custom-archive" {
		t.Fatal("validated and restored streams did not equal the admitted archive")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("restorable-custom-archive")))
	if !strings.Contains(result.output, "archive=backup.dump sha256="+digest+" target=clean-postgresql-17 result=committed") || strings.Contains(result.output, "secret-sentinel") {
		t.Fatalf("unexpected bounded output: %q", result.output)
	}
	log := string(mustRead(t, fixture.log))
	for _, required := range []string{"pg_restore --list", "postgres --version", "SELECT count(*)", "--exit-on-error", "--single-transaction", "--no-privileges"} {
		if !strings.Contains(log, required) {
			t.Fatalf("docker log lacks %q: %s", required, log)
		}
	}
	if strings.Index(log, "pg_restore --list") > strings.Index(log, "SELECT count(*)") || strings.Index(log, "SELECT count(*)") > strings.LastIndex(log, "--single-transaction") {
		t.Fatalf("validation/inspection/restore order is wrong: %s", log)
	}
}

func TestRestoreLogicalAcceptsExplicitAuthentikPostgreSQLMajor(t *testing.T) {
	fixture := newFakeDocker(t)
	archive := cleanArchive(t)
	writeArchivePair(t, archive, []byte("authentik-custom-archive"))
	result := fixture.run(t, "restore-logical.sh", map[string]string{
		"FAKE_POSTGRES_VERSION": "postgres (PostgreSQL) 16.10",
	}, "clean-authentik-postgresql", archive, "16")
	if result.err != nil || !strings.Contains(result.output, "result=committed") {
		t.Fatalf("Authentik restore = (%v, %q)", result.err, result.output)
	}
}

func TestRestoreLogicalFailsClosedBeforeOrDuringRestore(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		prepare func(t *testing.T, archive string)
		want    string
	}{
		{name: "digest mismatch", prepare: func(t *testing.T, archive string) {
			writeArchivePair(t, archive, []byte("archive"))
			mustWrite(t, archive+".sha256", []byte(strings.Repeat("0", 64)+"\n"), 0o600)
		}, want: "digest mismatch"},
		{name: "invalid sidecar", prepare: func(t *testing.T, archive string) {
			mustWrite(t, archive, []byte("archive"), 0o600)
			mustWrite(t, archive+".sha256", []byte("bad\n"), 0o600)
		}, want: "sidecar format"},
		{name: "sidecar extra line", prepare: func(t *testing.T, archive string) {
			writeArchivePair(t, archive, []byte("archive"))
			content := mustRead(t, archive+".sha256")
			mustWrite(t, archive+".sha256", append(content, '\n'), 0o600)
		}, want: "sidecar format"},
		{name: "archive validation", env: map[string]string{"FAKE_LIST_FAIL": "1"}, prepare: writeDefaultPair, want: "archive validation failed"},
		{name: "wrong major", env: map[string]string{"FAKE_POSTGRES_VERSION": "postgres (PostgreSQL) 18.1"}, prepare: writeDefaultPair, want: "not PostgreSQL major 17"},
		{name: "invalid expected major", prepare: writeDefaultPair, want: "invalid expected PostgreSQL major"},
		{name: "identity", env: map[string]string{"FAKE_IDENTITY_FAIL": "1"}, prepare: writeDefaultPair, want: "identity is unavailable"},
		{name: "inspection", env: map[string]string{"FAKE_INSPECT_FAIL": "1"}, prepare: writeDefaultPair, want: "clean-target inspection failed"},
		{name: "nonempty", env: map[string]string{"FAKE_RELATIONS": "1"}, prepare: writeDefaultPair, want: "target database is not clean"},
		{name: "restore failure", env: map[string]string{"FAKE_RESTORE_FAIL": "1"}, prepare: writeDefaultPair, want: "inspect target before retry"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFakeDocker(t)
			archive := cleanArchive(t)
			test.prepare(t, archive)
			args := []string{"clean-postgresql-17", archive}
			if test.name == "invalid expected major" {
				args = append(args, "16x")
			}
			result := fixture.run(t, "restore-logical.sh", test.env, args...)
			if result.err == nil || !strings.Contains(result.output, test.want) {
				t.Fatalf("result = (%v, %q), want failure containing %q", result.err, result.output, test.want)
			}
			if strings.Contains(result.output, "secret-sentinel") {
				t.Fatalf("secret leaked: %q", result.output)
			}
		})
	}
}

func TestLogicalHelpersTerminateActiveStreamsOnCancellation(t *testing.T) {
	for _, test := range []struct {
		name, helper, stage string
		prepare             func(t *testing.T, archive string)
	}{
		{name: "backup dump", helper: "backup-logical.sh", stage: "dump"},
		{name: "database restore", helper: "restore-logical.sh", stage: "restore", prepare: writeDefaultPair},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFakeDocker(t)
			archive := cleanArchive(t)
			if test.prepare != nil {
				test.prepare(t, archive)
			}
			marker := filepath.Join(fixture.directory, "blocked")
			stopped := filepath.Join(fixture.directory, "stopped")
			command := fixture.command(t, test.helper, map[string]string{
				"FAKE_BLOCK_STAGE":   test.stage,
				"FAKE_BLOCK_MARKER":  marker,
				"FAKE_BLOCK_STOPPED": stopped,
			}, "clean-postgresql-17", archive)
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			waitForFile(t, marker)
			if err := command.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatalf("signal helper: %v", err)
			}
			finished := make(chan error, 1)
			go func() { finished <- command.Wait() }()
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("canceled helper returned success")
				}
			case <-time.After(3 * time.Second):
				_ = command.Process.Kill()
				t.Fatal("canceled helper did not terminate")
			}
			waitForFile(t, stopped)
			if test.helper == "backup-logical.sh" {
				if _, err := os.Lstat(archive); !os.IsNotExist(err) {
					t.Fatalf("canceled backup admitted archive: %v", err)
				}
				if matches, _ := filepath.Glob(filepath.Join(filepath.Dir(archive), ".backup.dump.*")); len(matches) != 0 {
					t.Fatalf("canceled backup retained temporary files: %q", matches)
				}
			}
		})
	}
}

type fakeDockerFixture struct {
	directory, log, listInput, restoreInput string
}

type commandResult struct {
	output string
	err    error
}

func newFakeDocker(t *testing.T) fakeDockerFixture {
	t.Helper()
	directory := t.TempDir()
	mustWrite(t, filepath.Join(directory, "docker"), []byte(fakeDocker), 0o700)
	return fakeDockerFixture{
		directory:    directory,
		log:          filepath.Join(directory, "docker.log"),
		listInput:    filepath.Join(directory, "list.input"),
		restoreInput: filepath.Join(directory, "restore.input"),
	}
}

func (fixture fakeDockerFixture) run(t *testing.T, helper string, extra map[string]string, arguments ...string) commandResult {
	t.Helper()
	command := fixture.command(t, helper, extra, arguments...)
	output, err := command.CombinedOutput()
	return commandResult{output: string(output), err: err}
}

func (fixture fakeDockerFixture) command(t *testing.T, helper string, extra map[string]string, arguments ...string) *exec.Cmd {
	t.Helper()
	command := exec.Command(helperPath(t, helper), arguments...)
	environment := append(os.Environ(),
		"PATH="+fixture.directory+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_DOCKER_LOG="+fixture.log,
		"FAKE_LIST_INPUT="+fixture.listInput,
		"FAKE_RESTORE_INPUT="+fixture.restoreInput,
		"POSTGRES_PASSWORD=secret-sentinel",
	)
	for key, value := range extra {
		environment = append(environment, key+"="+value)
	}
	command.Env = environment
	return command
}

func helperPath(t *testing.T, name string) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve helper source path")
	}
	return filepath.Join(filepath.Dir(source), name)
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", filepath.Base(path))
}

func cleanArchive(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "backup.dump")
}

func writeDefaultPair(t *testing.T, archive string) {
	t.Helper()
	writeArchivePair(t, archive, []byte("archive"))
}

func writeArchivePair(t *testing.T, archive string, content []byte) {
	t.Helper()
	mustWrite(t, archive, content, 0o600)
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	mustWrite(t, archive+".sha256", []byte(digest+"\n"), 0o600)
}

func mustWrite(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}
