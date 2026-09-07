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

	"golang.org/x/sys/unix"
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
		size:         8950,
		sha256:       "1efb702d08ae94f6fc1a4db36bc4b4d9776d9bb6dc6eb58665f5b40944ddca26",
		mode:         0o755,
	},
	{
		relativePath: "scripts/verify-alpha3-population-performance.sh",
		size:         9285,
		sha256:       "019fe49157bd71d7c2e752d05ef1d88ace9e91226095b45da6bb54968a4eb38f",
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
	sealedFiles := make(map[string]*os.File, len(criticalFiles))
	for _, identity := range criticalFiles {
		sealed, captureErr := captureCriticalFile(repository, identity)
		if captureErr != nil {
			fatalf("critical evidence file identity mismatch: %s: %v", identity.relativePath, captureErr)
		}
		defer sealed.Close()
		sealedFiles[identity.relativePath] = sealed
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
		"GOTTH_BB_EVIDENCE_REPOSITORY_ROOT="+repository,
		"GOTTH_BB_EVIDENCE_RUNNER_FD=3",
		"GOTTH_BB_EVIDENCE_LIBRARY_FD=4",
	)
	if container := os.Getenv("GOTTH_BB_POSTGRES_CONTAINER"); container != "" {
		environment = append(environment, "GOTTH_BB_POSTGRES_CONTAINER="+container)
	}

	runner := sealedFiles[filepath.ToSlash(filepath.Join("scripts", innerName))]
	library := sealedFiles["scripts/lib/alpha3-evidence-custody.sh"]
	if runner == nil || library == nil {
		fatalf("captured evidence input set is incomplete")
	}
	command := exec.Command("/usr/bin/bash", "--noprofile", "--norc", "-p", "/proc/self/fd/3")
	command.Dir = repository
	command.Env = environment
	command.ExtraFiles = []*os.File{runner, library}
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

func captureCriticalFile(repository string, identity fileIdentity) (_ *os.File, resultErr error) {
	path := filepath.Join(repository, filepath.FromSlash(identity.relativePath))
	fileDescriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open source without following symlinks: %w", err)
	}
	file := os.NewFile(uintptr(fileDescriptor), path)
	if file == nil {
		_ = unix.Close(fileDescriptor)
		return nil, fmt.Errorf("construct source descriptor")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("source is not a regular file")
	}
	if info.Size() != identity.size || info.Mode().Perm() != identity.mode {
		return nil, fmt.Errorf("source identity mismatch")
	}
	sealedDescriptor, err := unix.MemfdCreate("gotth-bb-alpha3-"+filepath.Base(identity.relativePath), unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, fmt.Errorf("create sealed input: %w", err)
	}
	sealed := os.NewFile(uintptr(sealedDescriptor), "sealed:"+identity.relativePath)
	if sealed == nil {
		_ = unix.Close(sealedDescriptor)
		return nil, fmt.Errorf("construct sealed input descriptor")
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, sealed.Close())
		}
	}()
	hash := sha256.New()
	bytesRead, err := io.Copy(io.MultiWriter(hash, sealed), file)
	if err != nil {
		return nil, fmt.Errorf("capture source bytes: %w", err)
	}
	if bytesRead != identity.size || fmt.Sprintf("%x", hash.Sum(nil)) != identity.sha256 {
		return nil, fmt.Errorf("source identity mismatch")
	}
	if _, err := sealed.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind sealed input: %w", err)
	}
	const requiredSeals = unix.F_SEAL_SEAL | unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE
	if _, err := unix.FcntlInt(sealed.Fd(), unix.F_ADD_SEALS, requiredSeals); err != nil {
		return nil, fmt.Errorf("seal captured input: %w", err)
	}
	observedSeals, err := unix.FcntlInt(sealed.Fd(), unix.F_GET_SEALS, 0)
	if err != nil {
		return nil, fmt.Errorf("inspect captured input seals: %w", err)
	}
	if observedSeals&requiredSeals != requiredSeals {
		return nil, fmt.Errorf("captured input seals are incomplete")
	}
	return sealed, nil
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
