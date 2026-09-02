package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"git.dannyhunn.com/agents/gotth-bb/internal/releaseartifact"
)

const packageTestCommit = "0123456789abcdef0123456789abcdef01234567"

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestRunBuildsAndReportsReleaseArtifact(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	output := filepath.Join(t.TempDir(), "release")
	var report bytes.Buffer
	err := run(context.Background(), packageArguments(output), &report, func() (string, error) {
		return repository, nil
	}, packageRunner())
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(report.String(), "release artifact=") || !strings.Contains(report.String(), " sha256=") || !strings.Contains(report.String(), " checksums=") {
		t.Fatalf("run() output = %q", report.String())
	}
	if _, err := os.Stat(filepath.Join(output, "SHA256SUMS")); err != nil {
		t.Fatalf("Stat(SHA256SUMS) error = %v", err)
	}
}

func TestRunRejectsInvalidCommandBoundaries(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	output := filepath.Join(t.TempDir(), "release")
	validArguments := packageArguments(output)
	tests := []struct {
		name   string
		ctx    context.Context
		args   []string
		output io.Writer
		cwd    func() (string, error)
		runner releaseartifact.Runner
		want   string
	}{
		{name: "nil context", args: validArguments, output: io.Discard, cwd: func() (string, error) { return repository, nil }, runner: packageRunner(), want: "context is required"},
		{name: "nil arguments", ctx: context.Background(), output: io.Discard, cwd: func() (string, error) { return repository, nil }, runner: packageRunner(), want: "arguments are required"},
		{name: "nil output", ctx: context.Background(), args: validArguments, cwd: func() (string, error) { return repository, nil }, runner: packageRunner(), want: "output is required"},
		{name: "nil cwd", ctx: context.Background(), args: validArguments, output: io.Discard, runner: packageRunner(), want: "working-directory resolver is required"},
		{name: "nil runner", ctx: context.Background(), args: validArguments, output: io.Discard, cwd: func() (string, error) { return repository, nil }, want: "runner is required"},
		{name: "missing", ctx: context.Background(), args: validArguments[:len(validArguments)-2], output: io.Discard, cwd: func() (string, error) { return repository, nil }, runner: packageRunner(), want: "expected exactly"},
		{name: "empty", ctx: context.Background(), args: []string{"--version", "", "--commit", packageTestCommit, "--goos", runtime.GOOS, "--goarch", runtime.GOARCH, "--output", output}, output: io.Discard, cwd: func() (string, error) { return repository, nil }, runner: packageRunner(), want: "expected exactly"},
		{name: "duplicate", ctx: context.Background(), args: append(validArguments, "--version", "1.0.0-alpha.1"), output: io.Discard, cwd: func() (string, error) { return repository, nil }, runner: packageRunner(), want: "expected exactly"},
		{name: "positional", ctx: context.Background(), args: append(validArguments, "extra"), output: io.Discard, cwd: func() (string, error) { return repository, nil }, runner: packageRunner(), want: "expected exactly"},
		{name: "unknown", ctx: context.Background(), args: append(validArguments, "--unknown", "value"), output: io.Discard, cwd: func() (string, error) { return repository, nil }, runner: packageRunner(), want: "expected exactly"},
		{name: "cwd failure", ctx: context.Background(), args: validArguments, output: io.Discard, cwd: func() (string, error) { return "", errors.New("cwd failed") }, runner: packageRunner(), want: "resolve package working directory"},
		{name: "build failure", ctx: context.Background(), args: append([]string{"--version", "invalid"}, validArguments[2:]...), output: io.Discard, cwd: func() (string, error) { return repository, nil }, runner: packageRunner(), want: "release identity is invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := run(test.ctx, test.args, test.output, test.cwd, test.runner)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestRunReportsCommittedOutputFailure(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	output := filepath.Join(t.TempDir(), "release")
	err := run(context.Background(), packageArguments(output), failingWriter{}, func() (string, error) {
		return repository, nil
	}, packageRunner())
	if err == nil || !strings.Contains(err.Error(), "artifact committed but result output failed") {
		t.Fatalf("run() error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(output, "SHA256SUMS")); statErr != nil {
		t.Fatalf("committed output stat error = %v", statErr)
	}
}

func TestExecuteUsesExactDirectoryEnvironmentAndCancellation(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	output, err := execute(context.Background(), directory, []string{"PACKAGE_VALUE=exact"}, "sh", "-c", "printf '%s:%s' \"$PWD\" \"$PACKAGE_VALUE\"")
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	if got, want := string(output), directory+":exact"; got != want {
		t.Fatalf("execute() = %q, want %q", got, want)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := execute(canceled, directory, nil, "sh", "-c", "exit 0"); err == nil {
		t.Fatal("execute(canceled) returned nil error")
	}
}

func packageArguments(output string) []string {
	return []string{
		"--version", "1.0.0-alpha.1",
		"--commit", packageTestCommit,
		"--goos", runtime.GOOS,
		"--goarch", runtime.GOARCH,
		"--output", output,
	}
}

func packageRunner() releaseartifact.Runner {
	return func(ctx context.Context, _ string, _ []string, name string, args ...string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch {
		case name == "git" && len(args) > 0 && args[0] == "rev-parse":
			return []byte(packageTestCommit + "\n"), nil
		case name == "git" && len(args) > 0 && args[0] == "status":
			return nil, nil
		case name == "git" && len(args) > 0 && args[0] == "show":
			return []byte("GRANT UPDATE (singleton)\nON TABLE public.governance_state\nTO :\"runtime_role\";\n"), nil
		case name == "go" && len(args) > 0 && args[0] == "env":
			return []byte("go1.26.6-test\n"), nil
		case name == "go" && len(args) > 4 && args[0] == "list" && args[3] == "-f":
			return []byte("1.26.6\n"), nil
		case name == "go" && len(args) > 0 && args[0] == "list":
			return []byte("git.dannyhunn.com/agents/gotth-bb\n"), nil
		case name == "go" && len(args) > 0 && args[0] == "build":
			outputIndex := -1
			for index, argument := range args {
				if argument == "-o" {
					outputIndex = index
					break
				}
			}
			if outputIndex < 0 || outputIndex+1 >= len(args) {
				return nil, errors.New("missing output")
			}
			return nil, os.WriteFile(args[outputIndex+1], []byte("binary\n"), 0o755)
		case filepath.Base(name) == "gotth-bb-migrate" || filepath.Base(name) == "gotth-bb-operator":
			return []byte("gotth-bb version=1.0.0-alpha.1 commit=" + packageTestCommit + "\n"), nil
		default:
			return nil, errors.New("unexpected command")
		}
	}
}
