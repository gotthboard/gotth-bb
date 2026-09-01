package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/app"
	"git.dannyhunn.com/agents/gotth-bb/internal/config"
	"git.dannyhunn.com/agents/gotth-bb/internal/httpui"
)

const shutdownTimeout = 15 * time.Second

// main binds process signals to the tested service runner and reports only a
// bounded top-level error before returning a nonzero process status.
//
// Complexity: local work is tight Theta(1) time and auxiliary space; process
// lifetime and delegated service costs are owned by run.
func main() {
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	finished := make(chan struct{})
	go func() {
		select {
		case <-signals:
			signal.Stop(signals)
			cancel()
		case <-finished:
		}
	}()
	defer func() {
		close(finished)
		signal.Stop(signals)
		cancel()
	}()
	if err := run(ctx, os.LookupEnv, os.Stderr, net.Listen); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "gotth-bb: %v\n", err)
		os.Exit(1)
	}
}

// run loads immutable configuration, constructs the HTTP boundary, binds the
// validated numeric listener, and serves until cancellation or failure.
//
// Complexity: for configuration input size n, startup parsing is O(n) time and
// auxiliary space. Remaining local wiring is tight Theta(1); listener, request,
// logging, and shutdown costs are delegated to their documented components.
func run(ctx context.Context, lookup config.LookupEnv, logOutput io.Writer, listen func(string, string) (net.Listener, error)) error {
	if ctx == nil {
		return fmt.Errorf("service context is required")
	}
	if logOutput == nil {
		return fmt.Errorf("service log output is required")
	}
	if listen == nil {
		return fmt.Errorf("service listener factory is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("service startup canceled: %w", err)
	}
	configured, err := config.Load(lookup)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(logOutput, &slog.HandlerOptions{Level: configured.LogLevel}))
	handler, err := app.NewHTTPHandler(httpui.NewHandler(), logger, rand.Reader, time.Now)
	if err != nil {
		return fmt.Errorf("construct HTTP handler: %w", err)
	}
	server, err := app.NewHTTPServer(configured, handler, slog.NewLogLogger(logger.Handler(), slog.LevelError))
	if err != nil {
		return fmt.Errorf("construct HTTP server: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("service startup canceled: %w", err)
	}
	listener, err := listen("tcp", configured.ListenAddr.String())
	if err != nil {
		return fmt.Errorf("listen for HTTP: %w", err)
	}
	if err := ctx.Err(); err != nil {
		if closeErr := listener.Close(); closeErr != nil {
			return errors.Join(fmt.Errorf("service startup canceled: %w", err), fmt.Errorf("close canceled listener: %w", closeErr))
		}
		return fmt.Errorf("service startup canceled: %w", err)
	}
	if err := app.RunHTTPServer(ctx, server, listener, shutdownTimeout); err != nil {
		return err
	}
	logger.InfoContext(context.Background(), "service stopped")
	return nil
}
