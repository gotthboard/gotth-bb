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
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

const shutdownTimeout = 15 * time.Second

type databasePool interface {
	Close()
}

type poolFactory func(context.Context, *pgxpool.Config) (databasePool, error)

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
	if err := run(ctx, os.LookupEnv, os.Stderr, func(poolContext context.Context, poolConfig *pgxpool.Config) (databasePool, error) {
		return store.OpenPool(poolContext, poolConfig)
	}, net.Listen); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "gotth-bb: %v\n", err)
		os.Exit(1)
	}
}

// run loads immutable configuration, constructs the HTTP boundary, binds the
// validated numeric listener, and serves until cancellation or failure.
//
// Complexity: for n configuration bytes, configured pool capacity c, initial
// database latency r, served request work q, and shutdown work d, delegated
// time is O(n+N(c)+r+q+d), Omega(1), with no tighter Theta bound established
// because database, network, request, and shutdown costs vary independently.
// Auxiliary space is O(n+A(c)+H(q)), Omega(1), with no tighter Theta bound
// established; N and A are pgx construction costs and H is net/http's concurrent
// request state. Local validation, ownership transfers, and wiring are time
// and auxiliary-space O(1), Omega(1), and tight Theta(1).
func run(ctx context.Context, lookup config.LookupEnv, logOutput io.Writer, openPool poolFactory, listen func(string, string) (net.Listener, error)) error {
	if ctx == nil {
		return fmt.Errorf("service context is required")
	}
	if logOutput == nil {
		return fmt.Errorf("service log output is required")
	}
	if openPool == nil {
		return fmt.Errorf("PostgreSQL pool factory is required")
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
	poolConfig, err := configured.DatabasePoolConfig()
	if err != nil {
		return fmt.Errorf("configure PostgreSQL pool: %w", err)
	}
	pool, err := openPool(ctx, poolConfig)
	if err != nil {
		if pool != nil {
			pool.Close()
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return fmt.Errorf("open PostgreSQL pool: %w", contextErr)
		}
		return fmt.Errorf("open PostgreSQL pool failed")
	}
	if pool == nil {
		return fmt.Errorf("open PostgreSQL pool returned no pool")
	}
	defer pool.Close()
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
