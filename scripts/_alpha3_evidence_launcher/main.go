//go:build linux && amd64

// alpha3-evidence-launcher is the static, environment-clearing entry point for
// the Linux/amd64 Alpha.3 PostgreSQL evidence runners. The Bash programs are
// deliberately not supported as direct entry points.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const launcherName = "alpha3-evidence-launcher"

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
	info, err := os.Lstat(inner)
	if err != nil {
		fatalf("inspect evidence runner: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		fatalf("evidence runner is not a regular file: %s", inner)
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
	}
	rootCommand := exec.Command("/usr/bin/git", "rev-parse", "--show-toplevel")
	rootCommand.Dir = repository
	rootCommand.Env = gitEnvironment
	rootOutput, err := rootCommand.Output()
	if err != nil || strings.TrimSpace(string(rootOutput)) != repository {
		fatalf("launcher path is not inside its exact Git worktree")
	}
	statusCommand := exec.Command("/usr/bin/git", "status", "--porcelain=v1", "--untracked-files=all")
	statusCommand.Dir = repository
	statusCommand.Env = gitEnvironment
	statusOutput, err := statusCommand.Output()
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

func fatalf(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "alpha3 evidence launcher: "+format+"\n", arguments...)
	os.Exit(2)
}
