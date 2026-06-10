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

type configurableLifecycle interface {
	SetConfigDir(configDir string)
}

type applicationLifecycle struct {
	configDir string
}

func main() {
	os.Exit(execute(os.Args[1:], interruptSignalContext, &applicationLifecycle{}))
}

func execute(args []string, newSignalContext func(context.Context) (context.Context, context.CancelFunc), lifecycle lifecycleController) int {
	startupOpts, err := parseStartupFlags(args)
	if err != nil {
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
		lifecycle = &applicationLifecycle{}
	}
	if configurable, ok := lifecycle.(configurableLifecycle); ok {
		configurable.SetConfigDir(startupOpts.ConfigDir)
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

func (a *applicationLifecycle) Startup(ctx context.Context) error {
	configDir := a.configDir
	if configDir == "" {
		configDir = appconfig.ConfigDir()
	}
	cfg := appconfig.LoadWithConfigDir(configDir)
	if err := appconfig.ValidateDirectorySeparation(configDir, cfg); err != nil {
		return err
	}
	opts := application.Options{
		ServiceName: "thoughtflow",
		ConfigDir:   configDir,
	}
	if err := application.StartupWithOptions(ctx, service.DefaultService(), opts); err != nil {
		return err
	}
	return nil
}

func (a *applicationLifecycle) SetConfigDir(configDir string) {
	a.configDir = configDir
}

func (a *applicationLifecycle) Run(ctx context.Context) error {
	if err := application.Run(ctx); err != nil {
		return err
	}
	return nil
}

func (a *applicationLifecycle) Shutdown(ctx context.Context) {
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

type startupOptions struct {
	ConfigDir string
}

func parseStartupFlags(args []string) (startupOptions, error) {
	flags := flag.NewFlagSet("thoughtflow", flag.ContinueOnError)
	opts := startupOptions{}
	flags.StringVar(&opts.ConfigDir, "config-dir", appconfig.ConfigDir(), "config directory containing application.toml")
	if err := flags.Parse(args); err != nil {
		return startupOptions{}, err
	}
	return opts, nil
}
