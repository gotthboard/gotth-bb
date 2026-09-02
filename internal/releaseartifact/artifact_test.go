package releaseartifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

type boundedFailWriter struct {
	remaining int
}

func (writer *boundedFailWriter) Write(data []byte) (int, error) {
	if writer.remaining <= 0 {
		return 0, errors.New("forced write failure")
	}
	if len(data) <= writer.remaining {
		writer.remaining -= len(data)
		return len(data), nil
	}
	written := writer.remaining
	writer.remaining = 0
	return written, errors.New("forced write failure")
}

func TestBuildProducesDeterministicAtomicArtifact(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	firstOutput := filepath.Join(t.TempDir(), "first")
	secondOutput := filepath.Join(t.TempDir(), "second")
	firstCalls := make([]string, 0, 8)
	first, err := Build(context.Background(), testConfig(repository, firstOutput), successfulRunner(t, &firstCalls))
	if err != nil {
		t.Fatalf("Build(first) error = %v", err)
	}
	second, err := Build(context.Background(), testConfig(repository, secondOutput), successfulRunner(t, nil))
	if err != nil {
		t.Fatalf("Build(second) error = %v", err)
	}
	if first.SHA256 != second.SHA256 {
		t.Fatalf("digests differ: %q != %q", first.SHA256, second.SHA256)
	}
	firstArchive, err := os.ReadFile(first.ArchivePath)
	if err != nil {
		t.Fatalf("ReadFile(first archive) error = %v", err)
	}
	secondArchive, err := os.ReadFile(second.ArchivePath)
	if err != nil {
		t.Fatalf("ReadFile(second archive) error = %v", err)
	}
	if !bytes.Equal(firstArchive, secondArchive) {
		t.Fatal("deterministic archives differ")
	}
	digest := sha256.Sum256(firstArchive)
	if got := hex.EncodeToString(digest[:]); got != first.SHA256 {
		t.Fatalf("archive digest = %q, want %q", got, first.SHA256)
	}
	checksum, err := os.ReadFile(first.ChecksumPath)
	if err != nil {
		t.Fatalf("ReadFile(checksum) error = %v", err)
	}
	wantChecksum := first.SHA256 + "  " + filepath.Base(first.ArchivePath) + "\n"
	if string(checksum) != wantChecksum {
		t.Fatalf("checksum = %q, want %q", checksum, wantChecksum)
	}

	root := fmt.Sprintf("gotth-bb-1.0.0-alpha.1-%s-%s", runtime.GOOS, runtime.GOARCH)
	wantEntries := []string{
		root + "/",
		root + "/DEPENDENCIES.txt",
		root + "/RELEASE.txt",
		root + "/gotth-bb",
		root + "/gotth-bb-migrate",
		root + "/gotth-bb-operator",
	}
	gotEntries := readArchive(t, firstArchive)
	if !reflect.DeepEqual(gotEntries, wantEntries) {
		t.Fatalf("archive entries = %q, want %q", gotEntries, wantEntries)
	}
	wantCalls := []string{
		"git rev-parse --verify HEAD",
		"git status --porcelain=v1 --untracked-files=normal",
		"go env GOVERSION",
		"go list -mod=readonly -m -f {{.GoVersion}}",
		"go list -mod=readonly -m all",
		"go build -mod=readonly -trimpath -buildvcs=false -ldflags -s -w -X=" + linkerPackage + ".version=1.0.0-alpha.1 -X=" + linkerPackage + ".commit=" + testCommit + " -o <build>/gotth-bb ./cmd/forum",
		"go build -mod=readonly -trimpath -buildvcs=false -ldflags -s -w -X=" + linkerPackage + ".version=1.0.0-alpha.1 -X=" + linkerPackage + ".commit=" + testCommit + " -o <build>/gotth-bb-migrate ./cmd/migrate",
		"go build -mod=readonly -trimpath -buildvcs=false -ldflags -s -w -X=" + linkerPackage + ".version=1.0.0-alpha.1 -X=" + linkerPackage + ".commit=" + testCommit + " -o <build>/gotth-bb-operator ./cmd/operator",
		"<build>/gotth-bb-migrate version",
		"<build>/gotth-bb-operator version",
		"git rev-parse --verify HEAD",
		"git status --porcelain=v1 --untracked-files=normal",
	}
	if !reflect.DeepEqual(firstCalls, wantCalls) {
		t.Fatalf("commands = %#v, want %#v", firstCalls, wantCalls)
	}
}

func TestBuildRejectsInvalidBoundaryBeforeCommands(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	baseOutput := filepath.Join(t.TempDir(), "release")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name       string
		ctx        context.Context
		configured Config
		runner     Runner
		want       string
	}{
		{name: "nil context", configured: testConfig(repository, baseOutput), runner: unexpectedRunner(t), want: "release context is required"},
		{name: "nil runner", ctx: context.Background(), configured: testConfig(repository, baseOutput), want: "release command runner is required"},
		{name: "canceled", ctx: canceled, configured: testConfig(repository, baseOutput), runner: unexpectedRunner(t), want: "release build canceled"},
		{name: "relative repository", ctx: context.Background(), configured: Config{RepositoryDirectory: ".", OutputDirectory: baseOutput, Version: "1.0.0-alpha.1", Commit: testCommit, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, runner: unexpectedRunner(t), want: "repository directory must be absolute"},
		{name: "relative output", ctx: context.Background(), configured: Config{RepositoryDirectory: repository, OutputDirectory: "release", Version: "1.0.0-alpha.1", Commit: testCommit, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, runner: unexpectedRunner(t), want: "output directory must be absolute"},
		{name: "same directory", ctx: context.Background(), configured: Config{RepositoryDirectory: repository, OutputDirectory: repository, Version: "1.0.0-alpha.1", Commit: testCommit, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, runner: unexpectedRunner(t), want: "output directory must differ"},
		{name: "development identity", ctx: context.Background(), configured: Config{RepositoryDirectory: repository, OutputDirectory: baseOutput, Version: "development", Commit: "unknown", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, runner: unexpectedRunner(t), want: "release identity is invalid"},
		{name: "invalid identity", ctx: context.Background(), configured: Config{RepositoryDirectory: repository, OutputDirectory: baseOutput, Version: "1.0.0-alpha.01", Commit: testCommit, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, runner: unexpectedRunner(t), want: "release identity is invalid"},
		{name: "foreign OS", ctx: context.Background(), configured: Config{RepositoryDirectory: repository, OutputDirectory: baseOutput, Version: "1.0.0-alpha.1", Commit: testCommit, GOOS: "not-" + runtime.GOOS, GOARCH: runtime.GOARCH}, runner: unexpectedRunner(t), want: "release platform must match"},
		{name: "foreign architecture", ctx: context.Background(), configured: Config{RepositoryDirectory: repository, OutputDirectory: baseOutput, Version: "1.0.0-alpha.1", Commit: testCommit, GOOS: runtime.GOOS, GOARCH: "not-" + runtime.GOARCH}, runner: unexpectedRunner(t), want: "release platform must match"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Build(test.ctx, test.configured, test.runner)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Build() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestBuildRejectsExistingOrUninspectableOutput(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	existing := t.TempDir()
	if _, err := Build(context.Background(), testConfig(repository, existing), unexpectedRunner(t)); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Build(existing output) error = %v", err)
	}
	invalid := filepath.Join(string(filepath.Separator), "tmp", "invalid\x00output")
	if _, err := Build(context.Background(), testConfig(repository, invalid), unexpectedRunner(t)); err == nil || !strings.Contains(err.Error(), "inspect output directory") {
		t.Fatalf("Build(invalid output) error = %v", err)
	}
}

func TestBuildRejectsRepositoryAndToolFailures(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	tests := []struct {
		name string
		run  Runner
		want string
	}{
		{name: "commit command", run: failCommand("git", "rev-parse"), want: "resolve repository commit"},
		{name: "malformed commit", run: replaceCommand("git", "rev-parse", []byte(testCommit)), want: "noncanonical single-line"},
		{name: "wrong commit", run: replaceCommand("git", "rev-parse", []byte("1123456789abcdef0123456789abcdef01234567\n")), want: "does not match"},
		{name: "status command", run: failCommand("git", "status"), want: "inspect repository state"},
		{name: "dirty", run: replaceCommand("git", "status", []byte(" M file\n")), want: "repository is dirty"},
		{name: "toolchain command", run: failCommand("go", "env"), want: "resolve Go toolchain"},
		{name: "malformed toolchain", run: replaceCommand("go", "env", []byte("1.26.6\n")), want: "toolchain identity is invalid"},
		{name: "required toolchain command", run: failRequiredVersion(), want: "resolve required Go toolchain"},
		{name: "wrong toolchain", run: replaceRequiredVersion([]byte("1.25.0\n")), want: "toolchain does not match"},
		{name: "dependencies command", run: failDependencies(), want: "resolve dependency manifest"},
		{name: "malformed dependencies", run: replaceDependencies([]byte("module\r\n")), want: "dependency manifest is invalid"},
		{name: "forum build", run: failBuild("./cmd/forum"), want: "build gotth-bb"},
		{name: "migration build", run: failBuild("./cmd/migrate"), want: "build gotth-bb-migrate"},
		{name: "operator build", run: failBuild("./cmd/operator"), want: "build gotth-bb-operator"},
		{name: "identity command", run: failIdentity(), want: "verify gotth-bb-migrate release identity"},
		{name: "wrong identity", run: replaceIdentity([]byte("wrong\n")), want: "identity does not match"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "release")
			_, err := Build(context.Background(), testConfig(repository, output), test.run)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Build() error = %v, want containing %q", err, test.want)
			}
			if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("partial output stat error = %v, want not exist", statErr)
			}
		})
	}
}

func TestBuildRejectsPostBuildRepositoryDriftAndCancellation(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	tests := []struct {
		name string
		run  func(context.CancelFunc) Runner
		want string
	}{
		{name: "head changed", want: "verify repository after build", run: func(context.CancelFunc) Runner {
			base := successfulRunnerForOverride()
			calls := 0
			return func(ctx context.Context, directory string, environment []string, name string, args ...string) ([]byte, error) {
				if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
					calls++
					if calls == 2 {
						return []byte("1123456789abcdef0123456789abcdef01234567\n"), nil
					}
				}
				return base(ctx, directory, environment, name, args...)
			}
		}},
		{name: "worktree changed", want: "release repository is dirty", run: func(context.CancelFunc) Runner {
			base := successfulRunnerForOverride()
			calls := 0
			return func(ctx context.Context, directory string, environment []string, name string, args ...string) ([]byte, error) {
				if name == "git" && len(args) > 0 && args[0] == "status" {
					calls++
					if calls == 2 {
						return []byte(" M changed.go\n"), nil
					}
				}
				return base(ctx, directory, environment, name, args...)
			}
		}},
		{name: "canceled before build", want: "release build canceled", run: func(cancel context.CancelFunc) Runner {
			base := successfulRunnerForOverride()
			return func(ctx context.Context, directory string, environment []string, name string, args ...string) ([]byte, error) {
				output, err := base(ctx, directory, environment, name, args...)
				if name == "go" && reflect.DeepEqual(args, []string{"list", "-mod=readonly", "-m", "all"}) {
					cancel()
				}
				return output, err
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			output := filepath.Join(t.TempDir(), "release")
			_, err := Build(ctx, testConfig(repository, output), test.run(cancel))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Build() error = %v, want containing %q", err, test.want)
			}
			if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("partial output stat error = %v", statErr)
			}
		})
	}
}

func TestBuildRejectsArchiveFailureAndOutputRace(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	t.Run("missing built binary", func(t *testing.T) {
		output := filepath.Join(t.TempDir(), "release")
		base := successfulRunnerForOverride()
		runner := func(ctx context.Context, directory string, environment []string, name string, args ...string) ([]byte, error) {
			if name == "go" && len(args) > 0 && args[0] == "build" && args[len(args)-1] == "./cmd/forum" {
				return nil, nil
			}
			return base(ctx, directory, environment, name, args...)
		}
		if _, err := Build(context.Background(), testConfig(repository, output), runner); err == nil || !strings.Contains(err.Error(), "write release archive") {
			t.Fatalf("Build() error = %v", err)
		}
	})
	t.Run("output created concurrently", func(t *testing.T) {
		output := filepath.Join(t.TempDir(), "release")
		base := successfulRunnerForOverride()
		statusCalls := 0
		runner := func(ctx context.Context, directory string, environment []string, name string, args ...string) ([]byte, error) {
			result, err := base(ctx, directory, environment, name, args...)
			if name == "git" && len(args) > 0 && args[0] == "status" {
				statusCalls++
				if statusCalls == 2 {
					if mkdirErr := os.Mkdir(output, 0o755); mkdirErr != nil {
						t.Fatalf("Mkdir(racing output) error = %v", mkdirErr)
					}
				}
			}
			return result, err
		}
		if _, err := Build(context.Background(), testConfig(repository, output), runner); err == nil || !strings.Contains(err.Error(), "admit release output") {
			t.Fatalf("Build() error = %v", err)
		}
	})
}

func TestCommandTextAndManifestBoundaries(t *testing.T) {
	t.Parallel()

	runnerError := errors.New("runner failed")
	if _, err := commandText(context.Background(), func(context.Context, string, []string, string, ...string) ([]byte, error) {
		return nil, runnerError
	}, "/tmp", nil, "test"); !errors.Is(err, runnerError) {
		t.Fatalf("commandText() error = %v, want %v", err, runnerError)
	}
	for _, output := range [][]byte{nil, []byte("value"), []byte("value\nextra\n")} {
		if _, err := commandText(context.Background(), replaceAll(output), "/tmp", nil, "test"); err == nil {
			t.Fatalf("commandText(%q) returned nil error", output)
		}
	}
	if got, err := commandText(context.Background(), replaceAll([]byte("value\n")), "/tmp", nil, "test"); err != nil || got != "value" {
		t.Fatalf("commandText() = (%q, %v), want (value, nil)", got, err)
	}
	for _, manifest := range [][]byte{nil, []byte("module"), []byte("module\r\n"), []byte("module\x00\n"), []byte("module\n\ndep\n")} {
		if _, err := canonicalManifest(manifest); err == nil {
			t.Fatalf("canonicalManifest(%q) returned nil error", manifest)
		}
	}
	want := []byte("module\ndependency v1.0.0\n")
	got, err := canonicalManifest(want)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("canonicalManifest() = (%q, %v)", got, err)
	}
}

func TestWriteEntryRejectsMissingAndNonregularSources(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	if err := writeEntry(writer, archiveEntry{name: "missing", sourcePath: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("writeEntry(missing) returned nil error")
	}
	directory := t.TempDir()
	if err := writeEntry(writer, archiveEntry{name: "directory", sourcePath: directory}); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("writeEntry(directory) error = %v", err)
	}
}

func TestWriteArchiveAndEntryPropagateWriterFailures(t *testing.T) {
	t.Parallel()

	existing := filepath.Join(t.TempDir(), "existing")
	if err := os.WriteFile(existing, []byte("existing"), 0o644); err != nil {
		t.Fatalf("WriteFile(existing) error = %v", err)
	}
	if _, err := writeArchive(existing, "root", nil); err == nil {
		t.Fatal("writeArchive(existing) returned nil error")
	}
	for _, limit := range []int{0, 512} {
		writer := tar.NewWriter(&boundedFailWriter{remaining: limit})
		if err := writeEntry(writer, archiveEntry{name: "entry", mode: 0o644, data: []byte("content")}); err == nil {
			t.Fatalf("writeEntry(limit=%d) returned nil error", limit)
		}
	}
}

func TestReleaseEnvironmentPinsDocumentedArchitectureBaselines(t *testing.T) {
	t.Parallel()

	tests := []struct{ arch, want string }{
		{arch: "386", want: "GO386=sse2"},
		{arch: "amd64", want: "GOAMD64=v1"},
		{arch: "arm", want: "GOARM=7,hardfloat"},
		{arch: "arm64", want: "GOARM64=v8.0"},
		{arch: "mips", want: "GOMIPS=hardfloat"},
		{arch: "mipsle", want: "GOMIPS=hardfloat"},
		{arch: "mips64", want: "GOMIPS64=hardfloat"},
		{arch: "mips64le", want: "GOMIPS64=hardfloat"},
		{arch: "ppc64", want: "GOPPC64=power8"},
		{arch: "ppc64le", want: "GOPPC64=power8"},
		{arch: "riscv64", want: "GORISCV64=rva20u64"},
		{arch: "s390x", want: "GOARCH=s390x"},
	}
	for _, test := range tests {
		environment := releaseEnvironment("linux", test.arch)
		if got := environment[len(environment)-1]; got != test.want {
			t.Fatalf("releaseEnvironment(%q) final binding = %q, want %q", test.arch, got, test.want)
		}
	}
}

func testConfig(repository, output string) Config {
	return Config{
		RepositoryDirectory: repository,
		OutputDirectory:     output,
		Version:             "1.0.0-alpha.1",
		Commit:              testCommit,
		GOOS:                runtime.GOOS,
		GOARCH:              runtime.GOARCH,
	}
}

func successfulRunner(t *testing.T, calls *[]string) Runner {
	t.Helper()
	return func(ctx context.Context, directory string, environment []string, name string, args ...string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(directory) {
			t.Fatalf("command directory = %q, want absolute", directory)
		}
		recordedName := name
		if strings.HasPrefix(filepath.Base(name), "gotth-bb-") {
			recordedName = "<build>/" + filepath.Base(name)
		}
		recordedArgs := append([]string(nil), args...)
		if name == "go" && len(args) > 0 && args[0] == "build" {
			outputIndex := indexOf(args, "-o")
			if outputIndex < 0 || outputIndex+1 >= len(args) {
				t.Fatalf("build arguments lack output: %q", args)
			}
			output := args[outputIndex+1]
			if err := os.WriteFile(output, []byte("binary:"+args[len(args)-1]+"\n"), 0o755); err != nil {
				t.Fatalf("WriteFile(fake binary) error = %v", err)
			}
			recordedArgs[outputIndex+1] = "<build>/" + filepath.Base(output)
			assertReleaseEnvironment(t, environment)
		}
		if calls != nil {
			*calls = append(*calls, strings.Join(append([]string{recordedName}, recordedArgs...), " "))
		}
		switch {
		case name == "git" && reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD"}):
			return []byte(testCommit + "\n"), nil
		case name == "git" && len(args) > 0 && args[0] == "status":
			return nil, nil
		case name == "go" && reflect.DeepEqual(args, []string{"env", "GOVERSION"}):
			return []byte("go1.26.6-test\n"), nil
		case name == "go" && reflect.DeepEqual(args, []string{"list", "-mod=readonly", "-m", "-f", "{{.GoVersion}}"}):
			return []byte("1.26.6\n"), nil
		case name == "go" && reflect.DeepEqual(args, []string{"list", "-mod=readonly", "-m", "all"}):
			return []byte("git.dannyhunn.com/agents/gotth-bb\nexample.invalid/dependency v1.2.3\n"), nil
		case name == "go" && len(args) > 0 && args[0] == "build":
			return nil, nil
		case (filepath.Base(name) == "gotth-bb-migrate" || filepath.Base(name) == "gotth-bb-operator") && reflect.DeepEqual(args, []string{"version"}):
			return []byte("gotth-bb version=1.0.0-alpha.1 commit=" + testCommit + "\n"), nil
		default:
			t.Fatalf("unexpected command: %s %q", name, args)
			return nil, errors.New("unreachable")
		}
	}
}

func readArchive(t *testing.T, compressed []byte) []string {
	t.Helper()
	gzipReader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("gzip.NewReader() error = %v", err)
	}
	defer gzipReader.Close()
	if !gzipReader.Header.ModTime.IsZero() || gzipReader.Header.OS != 255 || gzipReader.Header.Name != "" {
		t.Fatalf("gzip header = %+v", gzipReader.Header)
	}
	tarReader := tar.NewReader(gzipReader)
	names := make([]string, 0, 6)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next() error = %v", err)
		}
		if !header.ModTime.Equal(time.Unix(0, 0).UTC()) || header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" {
			t.Fatalf("nondeterministic tar header = %+v", header)
		}
		content, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatalf("ReadAll(%q) error = %v", header.Name, err)
		}
		if strings.HasSuffix(header.Name, "RELEASE.txt") && !strings.Contains(string(content), "go_version=go1.26.6-test\n") {
			t.Fatalf("release metadata = %q", content)
		}
		names = append(names, header.Name)
	}
	return names
}

func unexpectedRunner(t *testing.T) Runner {
	t.Helper()
	return func(context.Context, string, []string, string, ...string) ([]byte, error) {
		t.Fatal("unexpected command")
		return nil, errors.New("unreachable")
	}
}

func replaceAll(output []byte) Runner {
	return func(context.Context, string, []string, string, ...string) ([]byte, error) { return output, nil }
}

func replaceCommand(command, firstArg string, output []byte) Runner {
	base := successfulRunnerForOverride()
	return func(ctx context.Context, directory string, environment []string, name string, args ...string) ([]byte, error) {
		if name == command && len(args) > 0 && args[0] == firstArg {
			return output, nil
		}
		return base(ctx, directory, environment, name, args...)
	}
}

func failCommand(command, firstArg string) Runner {
	base := successfulRunnerForOverride()
	return func(ctx context.Context, directory string, environment []string, name string, args ...string) ([]byte, error) {
		if name == command && len(args) > 0 && args[0] == firstArg {
			return nil, errors.New("command failed")
		}
		return base(ctx, directory, environment, name, args...)
	}
}

func failRequiredVersion() Runner {
	base := successfulRunnerForOverride()
	return func(ctx context.Context, directory string, environment []string, name string, args ...string) ([]byte, error) {
		if name == "go" && reflect.DeepEqual(args, []string{"list", "-mod=readonly", "-m", "-f", "{{.GoVersion}}"}) {
			return nil, errors.New("version failed")
		}
		return base(ctx, directory, environment, name, args...)
	}
}

func replaceRequiredVersion(output []byte) Runner {
	base := successfulRunnerForOverride()
	return func(ctx context.Context, directory string, environment []string, name string, args ...string) ([]byte, error) {
		if name == "go" && reflect.DeepEqual(args, []string{"list", "-mod=readonly", "-m", "-f", "{{.GoVersion}}"}) {
			return output, nil
		}
		return base(ctx, directory, environment, name, args...)
	}
}

func failDependencies() Runner {
	base := successfulRunnerForOverride()
	return func(ctx context.Context, directory string, environment []string, name string, args ...string) ([]byte, error) {
		if name == "go" && reflect.DeepEqual(args, []string{"list", "-mod=readonly", "-m", "all"}) {
			return nil, errors.New("dependencies failed")
		}
		return base(ctx, directory, environment, name, args...)
	}
}

func replaceDependencies(output []byte) Runner {
	base := successfulRunnerForOverride()
	return func(ctx context.Context, directory string, environment []string, name string, args ...string) ([]byte, error) {
		if name == "go" && reflect.DeepEqual(args, []string{"list", "-mod=readonly", "-m", "all"}) {
			return output, nil
		}
		return base(ctx, directory, environment, name, args...)
	}
}

func failBuild(target string) Runner {
	base := successfulRunnerForOverride()
	return func(ctx context.Context, directory string, environment []string, name string, args ...string) ([]byte, error) {
		if name == "go" && len(args) > 0 && args[0] == "build" && args[len(args)-1] == target {
			return nil, errors.New("build failed")
		}
		return base(ctx, directory, environment, name, args...)
	}
}

func failIdentity() Runner {
	base := successfulRunnerForOverride()
	return func(ctx context.Context, directory string, environment []string, name string, args ...string) ([]byte, error) {
		if filepath.Base(name) == "gotth-bb-migrate" || filepath.Base(name) == "gotth-bb-operator" {
			return nil, errors.New("identity failed")
		}
		return base(ctx, directory, environment, name, args...)
	}
}

func replaceIdentity(output []byte) Runner {
	base := successfulRunnerForOverride()
	return func(ctx context.Context, directory string, environment []string, name string, args ...string) ([]byte, error) {
		if filepath.Base(name) == "gotth-bb-migrate" || filepath.Base(name) == "gotth-bb-operator" {
			return output, nil
		}
		return base(ctx, directory, environment, name, args...)
	}
}

func successfulRunnerForOverride() Runner {
	return func(ctx context.Context, directory string, environment []string, name string, args ...string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch {
		case name == "git" && len(args) > 0 && args[0] == "rev-parse":
			return []byte(testCommit + "\n"), nil
		case name == "git" && len(args) > 0 && args[0] == "status":
			return nil, nil
		case name == "go" && len(args) > 0 && args[0] == "env":
			return []byte("go1.26.6-test\n"), nil
		case name == "go" && reflect.DeepEqual(args, []string{"list", "-mod=readonly", "-m", "-f", "{{.GoVersion}}"}):
			return []byte("1.26.6\n"), nil
		case name == "go" && len(args) > 0 && args[0] == "list":
			return []byte("git.dannyhunn.com/agents/gotth-bb\n"), nil
		case name == "go" && len(args) > 0 && args[0] == "build":
			outputIndex := indexOf(args, "-o")
			if outputIndex < 0 || outputIndex+1 >= len(args) {
				return nil, errors.New("missing build output")
			}
			return nil, os.WriteFile(args[outputIndex+1], []byte("binary\n"), 0o755)
		case filepath.Base(name) == "gotth-bb-migrate" || filepath.Base(name) == "gotth-bb-operator":
			return []byte("gotth-bb version=1.0.0-alpha.1 commit=" + testCommit + "\n"), nil
		default:
			return nil, errors.New("unexpected command")
		}
	}
}

func indexOf(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}

func assertReleaseEnvironment(t *testing.T, environment []string) {
	t.Helper()
	want := map[string]string{
		"CGO_ENABLED":  "0",
		"GOENV":        "off",
		"GOEXPERIMENT": "",
		"GOFIPS140":    "off",
		"GOFLAGS":      "",
		"GOTOOLCHAIN":  "local",
		"GOWORK":       "off",
		"GOOS":         runtime.GOOS,
		"GOARCH":       runtime.GOARCH,
	}
	if runtime.GOARCH == "amd64" {
		want["GOAMD64"] = "v1"
	}
	got := make(map[string]string, len(want))
	for _, binding := range environment {
		name, value, found := strings.Cut(binding, "=")
		if _, needed := want[name]; found && needed {
			got[name] = value
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("release environment = %q, want %q", got, want)
	}
}
