package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

type fakeLifecycleServer struct {
	serve    func(net.Listener) error
	shutdown func(context.Context) error
	close    func() error
}

type closeErrorListener struct {
	net.Listener
}

func (closeErrorListener) Close() error {
	return errors.New("listener close failed")
}

func (server *fakeLifecycleServer) Serve(listener net.Listener) error {
	return server.serve(listener)
}

func (server *fakeLifecycleServer) Shutdown(ctx context.Context) error {
	return server.shutdown(ctx)
}

func (server *fakeLifecycleServer) Close() error {
	return server.close()
}

func TestRunHTTPServerShutsDownAfterCancellation(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() returned error: %v", err)
	}
	started := make(chan struct{})
	serveResult := make(chan error, 1)
	server := &fakeLifecycleServer{
		serve: func(net.Listener) error {
			close(started)
			return <-serveResult
		},
		shutdown: func(context.Context) error {
			serveResult <- http.ErrServerClosed
			return nil
		},
		close: func() error { return nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- RunHTTPServer(ctx, server, listener, time.Second)
	}()
	<-started
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("RunHTTPServer() returned error: %v", err)
	}
}

func TestRunHTTPServerDoesNotServeAfterCancellation(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() returned error: %v", err)
	}
	server := &fakeLifecycleServer{
		serve: func(net.Listener) error {
			t.Fatal("Serve ran after cancellation")
			return nil
		},
		shutdown: func(context.Context) error { return nil },
		close:    func() error { return nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = RunHTTPServer(ctx, server, listener, time.Second)
	if err == nil || !strings.Contains(err.Error(), "start canceled") {
		t.Fatalf("RunHTTPServer() error = %v", err)
	}
}

func TestRunHTTPServerReportsCanceledListenerCloseFailure(t *testing.T) {
	t.Parallel()

	underlying, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() returned error: %v", err)
	}
	defer underlying.Close()
	server := &fakeLifecycleServer{
		serve: func(net.Listener) error {
			t.Fatal("Serve ran after cancellation")
			return nil
		},
		shutdown: func(context.Context) error { return nil },
		close:    func() error { return nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = RunHTTPServer(ctx, server, closeErrorListener{Listener: underlying}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "start canceled") || !strings.Contains(err.Error(), "close canceled HTTP listener") {
		t.Fatalf("RunHTTPServer() error = %v", err)
	}
}

func TestRunHTTPServerReportsServeFailure(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() returned error: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("listener.Close() returned error: %v", err)
	}
	server := &http.Server{Handler: http.NotFoundHandler()}
	err = RunHTTPServer(context.Background(), server, listener, time.Second)
	if err == nil || !strings.Contains(err.Error(), "serve HTTP") {
		t.Fatalf("RunHTTPServer() error = %v", err)
	}
}

func TestRunHTTPServerClosesAfterShutdownDeadline(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() returned error: %v", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		response.WriteHeader(http.StatusNoContent)
	})}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- RunHTTPServer(ctx, server, listener, time.Millisecond)
	}()
	clientResult := make(chan error, 1)
	go func() {
		response, requestErr := http.Get("http://" + listener.Addr().String())
		if requestErr == nil {
			_ = response.Body.Close()
		}
		clientResult <- requestErr
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not reach handler")
	}
	cancel()
	select {
	case runErr := <-result:
		if runErr == nil || !strings.Contains(runErr.Error(), "shutdown HTTP server") {
			t.Fatalf("RunHTTPServer() error = %v", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("RunHTTPServer() did not enforce shutdown deadline")
	}
	close(release)
	select {
	case <-clientResult:
	case <-time.After(time.Second):
		t.Fatal("client request did not terminate")
	}
}

func TestRunHTTPServerReportsForceCloseFailure(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() returned error: %v", err)
	}
	defer listener.Close()
	serveResult := make(chan error, 1)
	started := make(chan struct{})
	server := &fakeLifecycleServer{
		serve: func(net.Listener) error {
			close(started)
			return <-serveResult
		},
		shutdown: func(context.Context) error {
			return context.DeadlineExceeded
		},
		close: func() error {
			serveResult <- http.ErrServerClosed
			return errors.New("close failed")
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- RunHTTPServer(ctx, server, listener, time.Second) }()
	<-started
	cancel()
	err = <-result
	if err == nil || !strings.Contains(err.Error(), "shutdown HTTP server") || !strings.Contains(err.Error(), "force-close HTTP server") {
		t.Fatalf("RunHTTPServer() error = %v", err)
	}
}

func TestRunHTTPServerReportsServeFailureDuringShutdown(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() returned error: %v", err)
	}
	defer listener.Close()
	serveResult := make(chan error, 1)
	started := make(chan struct{})
	server := &fakeLifecycleServer{
		serve: func(net.Listener) error {
			close(started)
			return <-serveResult
		},
		shutdown: func(context.Context) error {
			serveResult <- errors.New("serve failed while draining")
			return nil
		},
		close: func() error { return nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- RunHTTPServer(ctx, server, listener, time.Second) }()
	<-started
	cancel()
	err = <-result
	if err == nil || !strings.Contains(err.Error(), "serve HTTP during shutdown") {
		t.Fatalf("RunHTTPServer() error = %v", err)
	}
}

func TestRunHTTPServerRejectsInvalidDependencies(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() returned error: %v", err)
	}
	defer listener.Close()
	server := &http.Server{Handler: http.NotFoundHandler()}
	if err := RunHTTPServer(nil, server, listener, time.Second); err == nil {
		t.Fatal("RunHTTPServer(nil, server, listener, timeout) accepted nil context")
	}
	if err := RunHTTPServer(context.Background(), nil, listener, time.Second); err == nil {
		t.Fatal("RunHTTPServer(context, nil, listener, timeout) accepted nil server")
	}
	if err := RunHTTPServer(context.Background(), server, nil, time.Second); err == nil {
		t.Fatal("RunHTTPServer(context, server, nil, timeout) accepted nil listener")
	}
	if err := RunHTTPServer(context.Background(), server, listener, 0); err == nil {
		t.Fatal("RunHTTPServer(context, server, listener, 0) accepted invalid timeout")
	}
}
