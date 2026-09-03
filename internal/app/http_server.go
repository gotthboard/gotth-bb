package app

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gotthboard/gotth-bb/internal/config"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	maxHeaderBytes    = 1 << 20
)

// NewHTTPServer constructs the loopback application server with explicit
// transport limits and a caller-owned sanitized error logger.
//
// Complexity: time O(1), Omega(1), and tight Theta(1); auxiliary space O(1),
// Omega(1), and tight Theta(1).
func NewHTTPServer(configured config.Config, handler http.Handler, errorLog *log.Logger) (*http.Server, error) {
	if handler == nil {
		return nil, fmt.Errorf("HTTP server handler is required")
	}
	if errorLog == nil {
		return nil, fmt.Errorf("HTTP server error logger is required")
	}
	return &http.Server{
		Addr:                         configured.ListenAddr.String(),
		Handler:                      handler,
		DisableGeneralOptionsHandler: true,
		ReadTimeout:                  readTimeout,
		ReadHeaderTimeout:            readHeaderTimeout,
		WriteTimeout:                 writeTimeout,
		IdleTimeout:                  idleTimeout,
		MaxHeaderBytes:               maxHeaderBytes,
		ErrorLog:                     errorLog,
	}, nil
}
