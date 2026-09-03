package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/gotthboard/gotth-bb/internal/releaseartifact"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Getwd, execute); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "gotth-bb-package: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer, workingDirectory func() (string, error), runner releaseartifact.Runner) error {
	if ctx == nil {
		return fmt.Errorf("package command context is required")
	}
	if args == nil {
		return fmt.Errorf("package command arguments are required")
	}
	if output == nil {
		return fmt.Errorf("package command output is required")
	}
	if workingDirectory == nil {
		return fmt.Errorf("package working-directory resolver is required")
	}
	if runner == nil {
		return fmt.Errorf("package command runner is required")
	}
	flags := flag.NewFlagSet("package", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	values := make(map[string]string, 5)
	seen := make(map[string]bool, 5)
	for _, name := range []string{"version", "commit", "goos", "goarch", "output"} {
		flagName := name
		flags.Func(flagName, "required exact value", func(value string) error {
			if seen[flagName] || value == "" {
				return fmt.Errorf("invalid %s", flagName)
			}
			seen[flagName], values[flagName] = true, value
			return nil
		})
	}
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || len(seen) != 5 {
		return fmt.Errorf("expected exactly --version, --commit, --goos, --goarch, and --output")
	}
	repository, err := workingDirectory()
	if err != nil {
		return fmt.Errorf("resolve package working directory: %w", err)
	}
	repository, err = filepath.Abs(repository)
	if err != nil {
		return fmt.Errorf("resolve absolute repository directory: %w", err)
	}
	outputDirectory, err := filepath.Abs(values["output"])
	if err != nil {
		return fmt.Errorf("resolve absolute output directory: %w", err)
	}
	result, err := releaseartifact.Build(ctx, releaseartifact.Config{
		RepositoryDirectory: repository,
		OutputDirectory:     outputDirectory,
		Version:             values["version"],
		Commit:              values["commit"],
		GOOS:                values["goos"],
		GOARCH:              values["goarch"],
	}, runner)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "release artifact=%s sha256=%s checksums=%s\n", result.ArchivePath, result.SHA256, result.ChecksumPath); err != nil {
		return fmt.Errorf("release artifact committed but result output failed: %w", err)
	}
	return nil
}

func execute(ctx context.Context, directory string, environment []string, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	if environment != nil {
		command.Env = environment
	}
	return command.Output()
}
