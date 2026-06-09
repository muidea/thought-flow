package thoughtflow

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	engine "github.com/muidea/magicEngine/http"

	"thoughtflow/internal/pkg/appconfig"
)

func TestNewGracefulHTTPServerWrapsMagicEngineHandler(t *testing.T) {
	registry := engine.NewRouteRegistry()
	server, err := newGracefulHTTPServer(appconfig.ServerConfig{Host: "127.0.0.1", Port: "18080"}, registry)
	if err != nil {
		t.Fatalf("newGracefulHTTPServer() error = %v", err)
	}
	httpServer, ok := server.(*http.Server)
	if !ok {
		t.Fatalf("server type = %T, want *http.Server", server)
	}
	if httpServer.Addr != "127.0.0.1:18080" {
		t.Fatalf("addr = %q", httpServer.Addr)
	}
	if httpServer.Handler == nil {
		t.Fatal("expected magicEngine handler")
	}
}

func TestModuleRunAndTeardownStopsHTTPServer(t *testing.T) {
	fake := newFakeGracefulHTTPServer()
	module := &Module{
		server:     fake,
		serverDone: make(chan error, 1),
	}
	if err := module.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	select {
	case <-fake.started:
	case <-time.After(time.Second):
		t.Fatal("server did not start")
	}

	module.Teardown(context.Background())

	select {
	case err := <-module.serverDone:
		if err != nil {
			t.Fatalf("server done error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
	if fake.shutdownCount() != 1 {
		t.Fatalf("shutdown count = %d, want 1", fake.shutdownCount())
	}
}

type fakeGracefulHTTPServer struct {
	started  chan struct{}
	stopped  chan struct{}
	mu       sync.Mutex
	shutdown int
}

func newFakeGracefulHTTPServer() *fakeGracefulHTTPServer {
	return &fakeGracefulHTTPServer{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

func (s *fakeGracefulHTTPServer) ListenAndServe() error {
	close(s.started)
	<-s.stopped
	return http.ErrServerClosed
}

func (s *fakeGracefulHTTPServer) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdown++
	select {
	case <-s.stopped:
	default:
		close(s.stopped)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (s *fakeGracefulHTTPServer) shutdownCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shutdown
}

func TestModuleRunReportsUnexpectedHTTPServerError(t *testing.T) {
	expected := errors.New("listen failed")
	module := &Module{
		server:     &failingGracefulHTTPServer{err: expected},
		serverDone: make(chan error, 1),
	}
	if err := module.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	select {
	case err := <-module.serverDone:
		if !errors.Is(err, expected) {
			t.Fatalf("server done error = %v, want %v", err, expected)
		}
	case <-time.After(time.Second):
		t.Fatal("server error was not reported")
	}
}

type failingGracefulHTTPServer struct {
	err error
}

func (s *failingGracefulHTTPServer) ListenAndServe() error {
	return s.err
}

func (s *failingGracefulHTTPServer) Shutdown(ctx context.Context) error {
	_ = ctx
	return nil
}
