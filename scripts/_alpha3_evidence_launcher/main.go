//go:build linux && amd64

// alpha3-evidence-launcher is the static, environment-clearing entry point for
// the Linux/amd64 Alpha.3 PostgreSQL evidence runners. The Bash programs are
// deliberately not supported as direct entry points.
package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const launcherName = "alpha3-evidence-launcher"

type fileIdentity struct {
	relativePath string
	size         int64
	sha256       string
	mode         os.FileMode
}

var criticalFiles = []fileIdentity{
	{
		relativePath: "scripts/verify-alpha3-rerender-performance.sh",
		size:         8493,
		sha256:       "713ad41a854f084eb1b55f4f607e213e709d1df4a0d9836ab6c24faf3db6d9fa",
		mode:         0o755,
	},
	{
		relativePath: "scripts/verify-alpha3-population-performance.sh",
		size:         8828,
		sha256:       "e6eb50a93f4c7db72358fa5ed4e860c8b4548676b81d71eca42c886378357453",
		mode:         0o755,
	},
	{
		relativePath: "scripts/lib/alpha3-evidence-custody.sh",
		size:         4265,
		sha256:       "068c32ee973723bb1f3803ecc63ac00f27f030b6377e25dfb4b4df1eae70778a",
		mode:         0o644,
	},
}

var forbiddenLocalGitConfiguration = map[string]bool{
	"core.attributesfile":       true,
	"core.checkstat":            true,
	"core.fsmonitor":            true,
	"core.fsmonitorhookversion": true,
	"core.ignorestat":           true,
	"core.trustctime":           true,
	"core.untrackedcache":       true,
}

func main() {
	if len(os.Args) != 2 {
		fatalf("usage: %s rerender|population", launcherName)
	}

	innerName := ""
	switch os.Args[1] {
	case "rerender":
		innerName = "verify-alpha3-rerender-performance.sh"
	case "population":
		innerName = "verify-alpha3-population-performance.sh"
	default:
		fatalf("unsupported Alpha.3 evidence mode %q", os.Args[1])
	}

	executable, err := os.Executable()
	if err != nil {
		fatalf("resolve launcher executable: %v", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		fatalf("resolve launcher symlinks: %v", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		fatalf("resolve absolute launcher path: %v", err)
	}
	if filepath.Base(executable) != launcherName {
		fatalf("launcher basename is %q, want %q", filepath.Base(executable), launcherName)
	}

	scriptsDir := filepath.Dir(executable)
	repository := filepath.Dir(scriptsDir)
	inner := filepath.Join(scriptsDir, innerName)
	for _, identity := range criticalFiles {
		verifyCriticalFile(repository, identity)
	}
	gitEnvironment := []string{
		"HOME=/nonexistent",
		"PATH=/usr/bin:/bin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"TZ=UTC",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_COUNT=0",
		"GIT_OPTIONAL_LOCKS=0",
	}
	rootOutput, err := gitOutput(repository, gitEnvironment, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(string(rootOutput)) != repository {
		fatalf("launcher path is not inside its exact Git worktree")
	}
	rejectLocalGitConfiguration(repository, gitEnvironment, "--local")
	worktreeConfigOutput, worktreeConfigErr := gitOutput(repository, gitEnvironment, "rev-parse", "--git-path", "config.worktree")
	if worktreeConfigErr != nil {
		fatalf("resolve worktree Git configuration: %v", worktreeConfigErr)
	}
	worktreeConfig := absoluteGitPath(repository, worktreeConfigOutput)
	if info, statErr := os.Lstat(worktreeConfig); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			fatalf("worktree Git configuration is not a regular file")
		}
		if info.Size() != 0 {
			rejectGitConfigurationFile(repository, gitEnvironment, worktreeConfig)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		fatalf("inspect worktree Git configuration: %v", statErr)
	}

	flagsOutput, err := gitOutput(repository, gitEnvironment, "ls-files", "-v", "-z")
	if err != nil {
		fatalf("inspect tracked-file index flags: %v", err)
	}
	for _, record := range strings.Split(string(flagsOutput), "\x00") {
		if record == "" {
			continue
		}
		tag := record[0]
		if tag == 'S' || (tag >= 'a' && tag <= 'z') {
			fatalf("tracked-file index flags may hide mutations: %q", record)
		}
	}

	attributesOutput, err := gitOutput(repository, gitEnvironment, "rev-parse", "--git-path", "info/attributes")
	if err != nil {
		fatalf("resolve Git info/attributes: %v", err)
	}
	attributesPath := absoluteGitPath(repository, attributesOutput)
	if info, statErr := os.Lstat(attributesPath); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != 0 {
			fatalf("Git info/attributes must be absent or an empty regular file")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		fatalf("inspect Git info/attributes: %v", statErr)
	}

	statusOutput, err := gitOutput(repository, gitEnvironment, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		fatalf("inspect committed source status: %v", err)
	}
	if len(statusOutput) != 0 {
		fatalf("Alpha.3 evidence requires a clean committed source tree")
	}

	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		fatalf("GOTTH_BB_TEST_DATABASE_URL is required")
	}
	evidenceOutput := os.Getenv("GOTTH_BB_EVIDENCE_OUTPUT")
	if evidenceOutput == "" {
		fatalf("GOTTH_BB_EVIDENCE_OUTPUT is required")
	}

	environment := append([]string{}, gitEnvironment...)
	environment = append(environment,
		"GOTTH_BB_TEST_DATABASE_URL="+databaseURL,
		"GOTTH_BB_EVIDENCE_OUTPUT="+evidenceOutput,
		"GOTTH_BB_EVIDENCE_LAUNCHER_PID="+strconv.Itoa(os.Getpid()),
		"GOTTH_BB_EVIDENCE_LAUNCHER_PATH="+executable,
	)
	if container := os.Getenv("GOTTH_BB_POSTGRES_CONTAINER"); container != "" {
		environment = append(environment, "GOTTH_BB_POSTGRES_CONTAINER="+container)
	}

	command := exec.Command("/usr/bin/bash", "--noprofile", "--norc", "-p", inner)
	command.Dir = repository
	command.Env = environment
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			os.Exit(exitError.ExitCode())
		}
		fatalf("run evidence process: %v", err)
	}
}

func verifyCriticalFile(repository string, identity fileIdentity) {
	path := filepath.Join(repository, filepath.FromSlash(identity.relativePath))
	info, err := os.Lstat(path)
	if err != nil {
		fatalf("inspect critical evidence file %s: %v", identity.relativePath, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		fatalf("critical evidence file is not a regular file: %s", identity.relativePath)
	}
	if info.Size() != identity.size || info.Mode().Perm() != identity.mode {
		fatalf("critical evidence file identity mismatch: %s", identity.relativePath)
	}
	file, err := os.Open(path)
	if err != nil {
		fatalf("open critical evidence file %s: %v", identity.relativePath, err)
	}
	hash := sha256.New()
	bytesRead, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		fatalf("hash critical evidence file %s", identity.relativePath)
	}
	if bytesRead != identity.size || fmt.Sprintf("%x", hash.Sum(nil)) != identity.sha256 {
		fatalf("critical evidence file identity mismatch: %s", identity.relativePath)
	}
}

func gitOutput(repository string, environment []string, arguments ...string) ([]byte, error) {
	safeArguments := []string{
		"--no-optional-locks",
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "core.ignoreStat=false",
	}
	safeArguments = append(safeArguments, arguments...)
	command := exec.Command("/usr/bin/git", safeArguments...)
	command.Dir = repository
	command.Env = environment
	return command.Output()
}

func absoluteGitPath(repository string, output []byte) string {
	path := strings.TrimSpace(string(output))
	if path == "" {
		fatalf("Git returned an empty metadata path")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repository, path)
	}
	return filepath.Clean(path)
}

func rejectLocalGitConfiguration(repository string, environment []string, scope string) {
	output, err := gitOutput(repository, environment, "config", scope, "--includes", "--name-only", "--list")
	if err != nil {
		fatalf("inspect %s Git configuration: %v", scope, err)
	}
	rejectGitConfigurationNames(output)
}

func rejectGitConfigurationFile(repository string, environment []string, path string) {
	output, err := gitOutput(repository, environment, "config", "--file", path, "--includes", "--name-only", "--list")
	if err != nil {
		fatalf("inspect worktree Git configuration: %v", err)
	}
	rejectGitConfigurationNames(output)
}

func rejectGitConfigurationNames(output []byte) {
	for _, rawName := range strings.Split(string(output), "\n") {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if name == "" {
			continue
		}
		if forbiddenLocalGitConfiguration[name] || (strings.HasPrefix(name, "tar.") && strings.HasSuffix(name, ".command")) {
			fatalf("local Git configuration may alter source custody: %s", name)
		}
	}
}

func fatalf(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "alpha3 evidence launcher: "+format+"\n", arguments...)
	os.Exit(2)
}
