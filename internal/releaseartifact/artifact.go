// Package releaseartifact builds one deterministic, checksummed native release
// archive from an exact clean repository commit.
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
	"hash"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/buildinfo"
)

const linkerPackage = "git.dannyhunn.com/agents/gotth-bb/internal/buildinfo"

// Runner executes one bounded repository command and returns its standard
// output. Implementations must not return unbounded or secret-bearing error
// output to callers.
type Runner func(context.Context, string, []string, string, ...string) ([]byte, error)

// Config identifies the repository state and native platform to package.
type Config struct {
	RepositoryDirectory string
	OutputDirectory     string
	Version             string
	Commit              string
	GOOS                string
	GOARCH              string
}

// Result identifies the immutable archive and its digest.
type Result struct {
	ArchivePath  string
	ChecksumPath string
	SHA256       string
}

type archiveEntry struct {
	name       string
	mode       int64
	sourcePath string
	data       []byte
}

// Build verifies an exact clean native checkout, builds all three release
// executables with one linker identity, executes the database-free identity
// check, and atomically admits a normalized tar.gz plus SHA256SUMS directory.
//
// Complexity: for source/build work b, dependency-manifest bytes d, executable
// bytes n, and compressed output bytes z, time is O(b+d+n+z), Omega(n), and
// auxiliary memory is O(d), Omega(1); executable contents are streamed once
// rather than retained in memory.
func Build(ctx context.Context, configured Config, run Runner) (Result, error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("release context is required")
	}
	if run == nil {
		return Result{}, fmt.Errorf("release command runner is required")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("release build canceled: %w", err)
	}
	if !filepath.IsAbs(configured.RepositoryDirectory) {
		return Result{}, fmt.Errorf("repository directory must be absolute")
	}
	if !filepath.IsAbs(configured.OutputDirectory) {
		return Result{}, fmt.Errorf("output directory must be absolute")
	}
	if configured.RepositoryDirectory == configured.OutputDirectory {
		return Result{}, fmt.Errorf("output directory must differ from repository directory")
	}
	release, err := buildinfo.Validate(configured.Version, configured.Commit)
	if err != nil || release.Version == "development" {
		return Result{}, fmt.Errorf("release identity is invalid")
	}
	if configured.GOOS != runtime.GOOS || configured.GOARCH != runtime.GOARCH {
		return Result{}, fmt.Errorf("release platform must match the native builder")
	}
	if _, err := os.Lstat(configured.OutputDirectory); err == nil {
		return Result{}, fmt.Errorf("output directory already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("inspect output directory: %w", err)
	}
	if err := verifyRepository(ctx, run, configured.RepositoryDirectory, configured.Commit); err != nil {
		return Result{}, fmt.Errorf("verify repository before build: %w", err)
	}
	environment := releaseEnvironment(configured.GOOS, configured.GOARCH)
	goVersion, err := commandText(ctx, run, configured.RepositoryDirectory, environment, "go", "env", "GOVERSION")
	if err != nil {
		return Result{}, fmt.Errorf("resolve Go toolchain: %w", err)
	}
	if !strings.HasPrefix(goVersion, "go") || strings.ContainsAny(goVersion, "\x00\r\n") {
		return Result{}, fmt.Errorf("Go toolchain identity is invalid")
	}
	requiredGoVersion, err := commandText(ctx, run, configured.RepositoryDirectory, environment, "go", "list", "-mod=readonly", "-m", "-f", "{{.GoVersion}}")
	if err != nil {
		return Result{}, fmt.Errorf("resolve required Go toolchain: %w", err)
	}
	if goVersion != "go"+requiredGoVersion && !strings.HasPrefix(goVersion, "go"+requiredGoVersion+"-") {
		return Result{}, fmt.Errorf("Go toolchain does not match repository requirement")
	}
	dependencies, err := run(ctx, configured.RepositoryDirectory, environment, "go", "list", "-mod=readonly", "-m", "all")
	if err != nil {
		return Result{}, fmt.Errorf("resolve dependency manifest: %w", err)
	}
	dependencies, err = canonicalManifest(dependencies)
	if err != nil {
		return Result{}, err
	}

	parent := filepath.Dir(configured.OutputDirectory)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Result{}, fmt.Errorf("create release output parent: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, ".gotth-bb-release-")
	if err != nil {
		return Result{}, fmt.Errorf("create release staging directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	buildDirectory := filepath.Join(temporary, "build")
	stageDirectory := filepath.Join(temporary, "stage")
	if err := os.Mkdir(buildDirectory, 0o755); err != nil {
		return Result{}, fmt.Errorf("create release build directory: %w", err)
	}
	if err := os.Mkdir(stageDirectory, 0o755); err != nil {
		return Result{}, fmt.Errorf("create release artifact directory: %w", err)
	}

	linkerFlags := fmt.Sprintf("-s -w -X=%s.version=%s -X=%s.commit=%s", linkerPackage, release.Version, linkerPackage, release.Commit)
	binaries := []struct{ name, command string }{
		{name: "gotth-bb", command: "./cmd/forum"},
		{name: "gotth-bb-migrate", command: "./cmd/migrate"},
		{name: "gotth-bb-operator", command: "./cmd/operator"},
	}
	entries := make([]archiveEntry, 0, len(binaries)+2)
	root := fmt.Sprintf("gotth-bb-%s-%s-%s", release.Version, configured.GOOS, configured.GOARCH)
	for _, binary := range binaries {
		if err := ctx.Err(); err != nil {
			return Result{}, fmt.Errorf("release build canceled: %w", err)
		}
		output := filepath.Join(buildDirectory, binary.name)
		if _, err := run(ctx, configured.RepositoryDirectory, environment, "go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags", linkerFlags, "-o", output, binary.command); err != nil {
			return Result{}, fmt.Errorf("build %s: %w", binary.name, err)
		}
		entries = append(entries, archiveEntry{name: root + "/" + binary.name, mode: 0o755, sourcePath: output})
	}
	expectedIdentity := fmt.Sprintf("gotth-bb version=%s commit=%s\n", release.Version, release.Commit)
	for _, binary := range []string{"gotth-bb-migrate", "gotth-bb-operator"} {
		identity, err := run(ctx, configured.RepositoryDirectory, nil, filepath.Join(buildDirectory, binary), "version")
		if err != nil {
			return Result{}, fmt.Errorf("verify %s release identity: %w", binary, err)
		}
		if string(identity) != expectedIdentity {
			return Result{}, fmt.Errorf("%s release identity does not match artifact", binary)
		}
	}
	if err := verifyRepository(ctx, run, configured.RepositoryDirectory, configured.Commit); err != nil {
		return Result{}, fmt.Errorf("verify repository after build: %w", err)
	}
	metadata := fmt.Sprintf("version=%s\ncommit=%s\ngoos=%s\ngoarch=%s\ngo_version=%s\ngo_required=%s\n", release.Version, release.Commit, configured.GOOS, configured.GOARCH, goVersion, requiredGoVersion)
	entries = append(entries,
		archiveEntry{name: root + "/DEPENDENCIES.txt", mode: 0o644, data: dependencies},
		archiveEntry{name: root + "/RELEASE.txt", mode: 0o644, data: []byte(metadata)},
	)
	sort.Slice(entries, func(left, right int) bool { return entries[left].name < entries[right].name })
	archiveName := root + ".tar.gz"
	archivePath := filepath.Join(stageDirectory, archiveName)
	digest, err := writeArchive(archivePath, root, entries)
	if err != nil {
		return Result{}, fmt.Errorf("write release archive: %w", err)
	}
	checksumPath := filepath.Join(stageDirectory, "SHA256SUMS")
	if err := os.WriteFile(checksumPath, []byte(digest+"  "+archiveName+"\n"), 0o644); err != nil {
		return Result{}, fmt.Errorf("write release checksum: %w", err)
	}
	if err := os.Rename(stageDirectory, configured.OutputDirectory); err != nil {
		return Result{}, fmt.Errorf("admit release output: %w", err)
	}
	return Result{
		ArchivePath:  filepath.Join(configured.OutputDirectory, archiveName),
		ChecksumPath: filepath.Join(configured.OutputDirectory, "SHA256SUMS"),
		SHA256:       digest,
	}, nil
}

func verifyRepository(ctx context.Context, run Runner, directory, commit string) error {
	head, err := commandText(ctx, run, directory, nil, "git", "rev-parse", "--verify", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve repository commit: %w", err)
	}
	if head != commit {
		return fmt.Errorf("release commit does not match repository HEAD")
	}
	status, err := run(ctx, directory, nil, "git", "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return fmt.Errorf("inspect repository state: %w", err)
	}
	if len(status) != 0 {
		return fmt.Errorf("release repository is dirty")
	}
	return nil
}

func releaseEnvironment(goos, goarch string) []string {
	environment := append(os.Environ(),
		"CGO_ENABLED=0",
		"GOENV=off",
		"GOEXPERIMENT=",
		"GOFIPS140=off",
		"GOFLAGS=",
		"GOTOOLCHAIN=local",
		"GOWORK=off",
		"GOOS="+goos,
		"GOARCH="+goarch,
	)
	variant := ""
	switch goarch {
	case "386":
		variant = "GO386=sse2"
	case "amd64":
		variant = "GOAMD64=v1"
	case "arm":
		variant = "GOARM=7,hardfloat"
	case "arm64":
		variant = "GOARM64=v8.0"
	case "mips", "mipsle":
		variant = "GOMIPS=hardfloat"
	case "mips64", "mips64le":
		variant = "GOMIPS64=hardfloat"
	case "ppc64", "ppc64le":
		variant = "GOPPC64=power8"
	case "riscv64":
		variant = "GORISCV64=rva20u64"
	}
	if variant != "" {
		environment = append(environment, variant)
	}
	return environment
}

func commandText(ctx context.Context, run Runner, directory string, environment []string, name string, args ...string) (string, error) {
	output, err := run(ctx, directory, environment, name, args...)
	if err != nil {
		return "", err
	}
	if len(output) == 0 || output[len(output)-1] != '\n' || strings.Count(string(output), "\n") != 1 {
		return "", fmt.Errorf("command returned a noncanonical single-line result")
	}
	return string(output[:len(output)-1]), nil
}

func canonicalManifest(manifest []byte) ([]byte, error) {
	if len(manifest) == 0 || manifest[len(manifest)-1] != '\n' || strings.ContainsAny(string(manifest), "\x00\r") {
		return nil, fmt.Errorf("dependency manifest is invalid")
	}
	for _, line := range strings.Split(string(manifest[:len(manifest)-1]), "\n") {
		if line == "" {
			return nil, fmt.Errorf("dependency manifest is invalid")
		}
	}
	return manifest, nil
}

func writeArchive(path, root string, entries []archiveEntry) (string, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	gzipWriter, err := gzip.NewWriterLevel(io.MultiWriter(file, digest), gzip.BestCompression)
	if err != nil {
		_ = file.Close()
		return "", err
	}
	gzipWriter.Header.ModTime = time.Time{}
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	archiveErr := tarWriter.WriteHeader(&tar.Header{
		Name: root + "/", Mode: 0o755, Typeflag: tar.TypeDir, ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatUSTAR,
	})
	for _, entry := range entries {
		if archiveErr != nil {
			break
		}
		archiveErr = writeEntry(tarWriter, entry)
	}
	archiveErr = errors.Join(archiveErr, tarWriter.Close(), gzipWriter.Close(), file.Close())
	if archiveErr != nil {
		return "", archiveErr
	}
	return encodeDigest(digest), nil
}

func writeEntry(writer *tar.Writer, entry archiveEntry) error {
	var (
		reader io.Reader = bytes.NewReader(entry.data)
		size   int64     = int64(len(entry.data))
		file   *os.File
	)
	if entry.sourcePath != "" {
		var err error
		file, err = os.Open(entry.sourcePath)
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("archive source is not a regular file")
		}
		reader, size = file, info.Size()
	}
	header := &tar.Header{
		Name: entry.name, Size: size, Mode: entry.mode, Typeflag: tar.TypeReg,
		ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatUSTAR,
	}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	_, err := io.Copy(writer, reader)
	return err
}

func encodeDigest(digest hash.Hash) string {
	return hex.EncodeToString(digest.Sum(nil))
}
