package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gotthboard/gotth-bb/internal/authentikcontrol"
	"github.com/gotthboard/gotth-bb/internal/authentikgateway"
	"github.com/gotthboard/gotth-bb/internal/buildinfo"
	"github.com/gotthboard/gotth-bb/internal/config"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		identity, err := buildinfo.Current()
		if err == nil {
			_, err = fmt.Fprintf(os.Stdout, "gotth-bb version=%s commit=%s\n", identity.Version, identity.Commit)
		}
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "gotth-bb-authentik-gateway: release identity unavailable")
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 1 {
		_, _ = fmt.Fprintln(os.Stderr, "gotth-bb-authentik-gateway: accepts only version or no arguments")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.LookupEnv); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "gotth-bb-authentik-gateway: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, lookup config.LookupEnv) error {
	if ctx == nil || lookup == nil {
		return fmt.Errorf("gateway startup dependencies are required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("gateway startup canceled")
	}
	require := func(name string) (string, error) {
		value, ok := lookup(name)
		if !ok || value == "" {
			return "", fmt.Errorf("%s is required", name)
		}
		return value, nil
	}
	issuer, err := require("OIDC_ISSUER_URL")
	if err != nil {
		return err
	}
	tokenRaw, err := require("AUTHENTIK_CONTROL_TOKEN_FILE")
	if err != nil {
		return err
	}
	tokenPath, err := config.ParseAuthentikControlFile("AUTHENTIK_CONTROL_TOKEN_FILE", tokenRaw)
	if err != nil {
		return err
	}
	objectsRaw, err := require("AUTHENTIK_CONTROL_OBJECTS_FILE")
	if err != nil {
		return err
	}
	objectsPath, err := config.ParseAuthentikControlFile("AUTHENTIK_CONTROL_OBJECTS_FILE", objectsRaw)
	if err != nil {
		return err
	}
	socketRaw, err := require("AUTHENTIK_CONTROL_SOCKET")
	if err != nil {
		return err
	}
	socketPath, err := config.ParseAuthentikControlSocket(socketRaw)
	if err != nil {
		return err
	}
	if tokenPath == objectsPath {
		return fmt.Errorf("gateway token and objects files must differ")
	}
	secret, objects, err := authentikcontrol.Load(tokenPath, objectsPath, issuer)
	if err != nil {
		return fmt.Errorf("load gateway configuration")
	}
	client, err := authentikcontrol.New(issuer, secret, objects)
	if err != nil {
		return fmt.Errorf("construct Authentik client")
	}
	defer client.Close()
	handler, err := authentikgateway.NewHandler(client, objects, time.Now)
	if err != nil {
		return err
	}
	listener, err := authentikgateway.Listen(socketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       3 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       5 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve gateway")
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			_ = server.Close()
			return fmt.Errorf("shut down gateway")
		}
		return nil
	}
}
