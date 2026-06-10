package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/muidea/magicCommon/framework/application"
	"github.com/muidea/magicCommon/framework/service"

	"thoughtflow/internal/pkg/appconfig"

	_ "thoughtflow/internal/modules/application/thoughtflow"
	_ "thoughtflow/internal/modules/capture"
	_ "thoughtflow/internal/modules/git_sync"
	_ "thoughtflow/internal/modules/refiner"
	_ "thoughtflow/internal/modules/search"
	_ "thoughtflow/internal/modules/topic"
)

const gracefulShutdownTimeout = 10 * time.Second

type lifecycleController interface {
	Startup(ctx context.Context) error
	Run(ctx context.Context) error
	Shutdown(ctx context.Context)
}

type applicationLifecycle struct{}

func main() {
	os.Exit(execute(os.Args[1:], interruptSignalContext, applicationLifecycle{}))
}

func execute(args []string, newSignalContext func(context.Context) (context.Context, context.CancelFunc), lifecycle lifecycleController) int {
	if err := applyStartupFlagEnv(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		slog.Error("parse startup flags failed", "error", err)
		return 2
	}

	if newSignalContext == nil {
		newSignalContext = interruptSignalContext
	}
	if lifecycle == nil {
		lifecycle = applicationLifecycle{}
	}

	ctx, stop := newSignalContext(context.Background())
	defer stop()

	if err := lifecycle.Startup(ctx); err != nil {
		slog.Error("startup failed", "error", err)
		return 1
	}
	if ctx.Err() != nil {
		stop()
		slog.Info("shutdown requested", "reason", ctx.Err())
		shutdownLifecycle(lifecycle)
		return 0
	}

	runDone := make(chan error, 1)
	go func() {
		runDone <- lifecycle.Run(ctx)
	}()

	select {
	case err := <-runDone:
		if err != nil {
			slog.Error("run failed", "error", err)
			shutdownLifecycle(lifecycle)
			return 1
		}
	case <-ctx.Done():
		stop()
		slog.Info("shutdown requested", "reason", ctx.Err())
		shutdownLifecycle(lifecycle)
		return 0
	}

	<-ctx.Done()
	stop()
	slog.Info("shutdown requested", "reason", ctx.Err())
	shutdownLifecycle(lifecycle)
	return 0
}

func (applicationLifecycle) Startup(ctx context.Context) error {
	opts := application.Options{
		ServiceName: "thoughtflow",
		ConfigDir:   appconfig.ConfigDir(),
	}
	if err := application.StartupWithOptions(ctx, service.DefaultService(), opts); err != nil {
		return err
	}
	return nil
}

func (applicationLifecycle) Run(ctx context.Context) error {
	if err := application.Run(ctx); err != nil {
		return err
	}
	return nil
}

func (applicationLifecycle) Shutdown(ctx context.Context) {
	application.Shutdown(ctx)
}

func interruptSignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

func shutdownLifecycle(lifecycle lifecycleController) {
	ctx, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
	defer cancel()
	lifecycle.Shutdown(ctx)
}

func applyStartupFlagEnv(args []string) error {
	flags := flag.NewFlagSet("thoughtflow", flag.ContinueOnError)
	flagToEnv := map[string]string{
		"host":                 "THOUGHTFLOW_HOST",
		"port":                 "THOUGHTFLOW_PORT",
		"config-dir":           "THOUGHTFLOW_CONFIG_DIR",
		"workspace-root":       "THOUGHTFLOW_WORKSPACE_ROOT",
		"auto-init-git":        "THOUGHTFLOW_AUTO_INIT_GIT",
		"git-enabled":          "THOUGHTFLOW_GIT_ENABLED",
		"git-debounce-seconds": "THOUGHTFLOW_GIT_DEBOUNCE_SECONDS",
		"duckdb-path":          "THOUGHTFLOW_DUCKDB_PATH",
		"ai-base-url":          "THOUGHTFLOW_AI_BASE_URL",
		"ai-api-key":           "THOUGHTFLOW_AI_API_KEY",
		"ai-chat-model":        "THOUGHTFLOW_AI_CHAT_MODEL",
		"ai-embedding-model":   "THOUGHTFLOW_AI_EMBEDDING_MODEL",
		"ai-timeout-seconds":   "THOUGHTFLOW_AI_TIMEOUT_SECONDS",
	}
	values := map[string]*string{}
	for name := range flagToEnv {
		value := ""
		values[name] = &value
		flags.StringVar(&value, name, "", "override "+flagToEnv[name])
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	flags.Visit(func(item *flag.Flag) {
		if envKey, ok := flagToEnv[item.Name]; ok {
			_ = os.Setenv(envKey, *values[item.Name])
		}
	})
	return nil
}
