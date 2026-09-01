package app

import (
	"bytes"
	"log"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/config"
)

func TestNewHTTPServerUsesBoundedTransportSettings(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	errorLog := log.New(&bytes.Buffer{}, "", 0)
	server, err := NewHTTPServer(config.Config{
		ListenAddr: netip.MustParseAddrPort("127.0.0.1:8080"),
	}, handler, errorLog)
	if err != nil {
		t.Fatalf("NewHTTPServer() returned error: %v", err)
	}
	if server.Addr != "127.0.0.1:8080" || server.Handler == nil {
		t.Fatalf("server address/handler = %q, %T", server.Addr, server.Handler)
	}
	if server.ReadHeaderTimeout != 5*time.Second || server.ReadTimeout != 30*time.Second || server.WriteTimeout != 30*time.Second || server.IdleTimeout != 60*time.Second {
		t.Fatalf("server timeouts = %s, %s, %s, %s", server.ReadHeaderTimeout, server.ReadTimeout, server.WriteTimeout, server.IdleTimeout)
	}
	if server.MaxHeaderBytes != 1<<20 || !server.DisableGeneralOptionsHandler || server.ErrorLog != errorLog {
		t.Fatalf("server controls = max headers %d, disable options %v, error log %p", server.MaxHeaderBytes, server.DisableGeneralOptionsHandler, server.ErrorLog)
	}
}

func TestNewHTTPServerRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	configured := config.Config{ListenAddr: netip.MustParseAddrPort("127.0.0.1:8080")}
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	errorLog := log.New(&bytes.Buffer{}, "", 0)
	if got, err := NewHTTPServer(configured, nil, errorLog); err == nil || got != nil {
		t.Fatalf("NewHTTPServer(config, nil, log) = %v, %v", got, err)
	}
	if got, err := NewHTTPServer(configured, handler, nil); err == nil || got != nil {
		t.Fatalf("NewHTTPServer(config, handler, nil) = %v, %v", got, err)
	}
}
