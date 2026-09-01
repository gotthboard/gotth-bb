package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

type lifecycleServer interface {
	Serve(net.Listener) error
	Shutdown(context.Context) error
	Close() error
}

// RunHTTPServer serves until cancellation or a serve failure, then performs a
// bounded graceful shutdown and force-closes connections if draining expires.
//
// Complexity: orchestration performs tight Theta(1) local work and uses tight
// Theta(1) auxiliary space. Serve and Shutdown costs and wait time are owned by
// net/http and the active connection/request population; the shutdown wait is
// bounded by shutdownTimeout before forced closure.
func RunHTTPServer(ctx context.Context, server lifecycleServer, listener net.Listener, shutdownTimeout time.Duration) error {
	if ctx == nil {
		return fmt.Errorf("HTTP server context is required")
	}
	if server == nil {
		return fmt.Errorf("HTTP server is required")
	}
	if listener == nil {
		return fmt.Errorf("HTTP listener is required")
	}
	if shutdownTimeout <= 0 {
		return fmt.Errorf("HTTP shutdown timeout must be positive")
	}
	if err := ctx.Err(); err != nil {
		if closeErr := listener.Close(); closeErr != nil {
			return errors.Join(fmt.Errorf("HTTP server start canceled: %w", err), fmt.Errorf("close canceled HTTP listener: %w", closeErr))
		}
		return fmt.Errorf("HTTP server start canceled: %w", err)
	}
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.Serve(listener)
	}()

	select {
	case err := <-serveErrors:
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
	}

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownContext); err != nil {
		shutdownErr := fmt.Errorf("shutdown HTTP server: %w", err)
		if closeErr := server.Close(); closeErr != nil {
			return errors.Join(shutdownErr, fmt.Errorf("force-close HTTP server: %w", closeErr))
		}
		return shutdownErr
	}
	if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP during shutdown: %w", err)
	}
	return nil
}
