package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestApplyStartupFlagEnvOverridesSelectedValues(t *testing.T) {
	t.Setenv("THOUGHTFLOW_HOST", "127.0.0.1")
	t.Setenv("THOUGHTFLOW_PORT", "8080")
	t.Setenv("THOUGHTFLOW_GIT_ENABLED", "true")

	err := applyStartupFlagEnv([]string{
		"--host", "0.0.0.0",
		"--port", "9090",
		"--config-dir", "/tmp/thoughtflow-config",
		"--workspace-root", "/tmp/thoughtflow",
		"--git-enabled", "false",
		"--ai-api-key", "cli-key",
	})
	if err != nil {
		t.Fatalf("applyStartupFlagEnv() error = %v", err)
	}

	assertEnv(t, "THOUGHTFLOW_HOST", "0.0.0.0")
	assertEnv(t, "THOUGHTFLOW_PORT", "9090")
	assertEnv(t, "THOUGHTFLOW_CONFIG_DIR", "/tmp/thoughtflow-config")
	assertEnv(t, "THOUGHTFLOW_WORKSPACE_ROOT", "/tmp/thoughtflow")
	assertEnv(t, "THOUGHTFLOW_GIT_ENABLED", "false")
	assertEnv(t, "THOUGHTFLOW_AI_API_KEY", "cli-key")
}

func TestApplyStartupFlagEnvLeavesUnspecifiedValuesUntouched(t *testing.T) {
	t.Setenv("THOUGHTFLOW_PORT", "8080")

	if err := applyStartupFlagEnv([]string{"--host", "0.0.0.0"}); err != nil {
		t.Fatalf("applyStartupFlagEnv() error = %v", err)
	}

	assertEnv(t, "THOUGHTFLOW_HOST", "0.0.0.0")
	assertEnv(t, "THOUGHTFLOW_PORT", "8080")
}

func TestApplyStartupFlagEnvReturnsFlagErrors(t *testing.T) {
	if err := applyStartupFlagEnv([]string{"--unknown"}); err == nil {
		t.Fatal("expected unknown flag error")
	}
	if err := applyStartupFlagEnv([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error = %v", err)
	}
}

func TestExecuteShutdownsOnInterruptAndRestoresSignalHandling(t *testing.T) {
	signalCtx, cancelSignal := context.WithCancel(context.Background())
	var stopCount atomic.Int32
	runReturned := make(chan struct{})
	shutdownStopCount := make(chan int32, 1)
	lifecycle := &fakeLifecycle{
		runFunc: func(ctx context.Context) error {
			_ = ctx
			close(runReturned)
			return nil
		},
		shutdownFunc: func(ctx context.Context) {
			_ = ctx
			shutdownStopCount <- stopCount.Load()
		},
	}

	done := make(chan int, 1)
	go func() {
		done <- execute(nil, func(parent context.Context) (context.Context, context.CancelFunc) {
			_ = parent
			return signalCtx, func() {
				stopCount.Add(1)
			}
		}, lifecycle)
	}()

	waitForClosed(t, runReturned, "run")
	cancelSignal()

	if code := waitForExitCode(t, done); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if count := lifecycle.shutdownCount.Load(); count != 1 {
		t.Fatalf("shutdown count = %d, want 1", count)
	}
	select {
	case count := <-shutdownStopCount:
		if count < 1 {
			t.Fatal("signal stop was not called before shutdown")
		}
	default:
		t.Fatal("shutdown did not record signal stop count")
	}
	if count := stopCount.Load(); count < 2 {
		t.Fatalf("signal stop count after exit = %d, want at least 2", count)
	}
}

func TestExecuteInterruptsWhileRunIsBlocking(t *testing.T) {
	signalCtx, cancelSignal := context.WithCancel(context.Background())
	runStarted := make(chan struct{})
	lifecycle := &fakeLifecycle{
		runFunc: func(ctx context.Context) error {
			close(runStarted)
			<-ctx.Done()
			return nil
		},
	}

	done := make(chan int, 1)
	go func() {
		done <- execute(nil, func(parent context.Context) (context.Context, context.CancelFunc) {
			_ = parent
			return signalCtx, func() {}
		}, lifecycle)
	}()

	waitForClosed(t, runStarted, "run")
	cancelSignal()

	if code := waitForExitCode(t, done); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if count := lifecycle.shutdownCount.Load(); count != 1 {
		t.Fatalf("shutdown count = %d, want 1", count)
	}
}

func TestExecuteReturnsFailureWhenRunFails(t *testing.T) {
	expected := errors.New("run failed")
	lifecycle := &fakeLifecycle{runErr: expected}

	code := execute(nil, func(parent context.Context) (context.Context, context.CancelFunc) {
		return context.WithCancel(parent)
	}, lifecycle)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if count := lifecycle.shutdownCount.Load(); count != 1 {
		t.Fatalf("shutdown count = %d, want 1", count)
	}
}

func assertEnv(t *testing.T, key string, expected string) {
	t.Helper()
	if actual := os.Getenv(key); actual != expected {
		t.Fatalf("%s = %q, want %q", key, actual, expected)
	}
}

type fakeLifecycle struct {
	startupErr   error
	runErr       error
	runFunc      func(context.Context) error
	shutdownFunc func(context.Context)

	startupCount  atomic.Int32
	runCount      atomic.Int32
	shutdownCount atomic.Int32
}

func (f *fakeLifecycle) Startup(ctx context.Context) error {
	_ = ctx
	f.startupCount.Add(1)
	return f.startupErr
}

func (f *fakeLifecycle) Run(ctx context.Context) error {
	f.runCount.Add(1)
	if f.runFunc != nil {
		return f.runFunc(ctx)
	}
	return f.runErr
}

func (f *fakeLifecycle) Shutdown(ctx context.Context) {
	f.shutdownCount.Add(1)
	if f.shutdownFunc != nil {
		f.shutdownFunc(ctx)
	}
}

func waitForClosed(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("%s did not complete", name)
	}
}

func waitForExitCode(t *testing.T, done <-chan int) int {
	t.Helper()
	select {
	case code := <-done:
		return code
	case <-time.After(time.Second):
		t.Fatal("execute did not return")
		return -1
	}
}
