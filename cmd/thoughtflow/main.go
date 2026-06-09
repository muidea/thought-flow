package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/muidea/magicCommon/framework/application"
	"github.com/muidea/magicCommon/framework/service"

	_ "thoughtflow/internal/modules/application/thoughtflow"
	_ "thoughtflow/internal/modules/capture"
	_ "thoughtflow/internal/modules/git_sync"
	_ "thoughtflow/internal/modules/refiner"
	_ "thoughtflow/internal/modules/search"
	_ "thoughtflow/internal/modules/topic"
)

func main() {
	if err := applyStartupFlagEnv(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		slog.Error("parse startup flags failed", "error", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := application.Options{
		ServiceName: "thoughtflow",
	}
	if err := application.StartupWithOptions(ctx, service.DefaultService(), opts); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
	if err := application.Run(ctx); err != nil {
		slog.Error("run failed", "error", err)
		application.Shutdown(context.Background())
		os.Exit(1)
	}

	<-ctx.Done()
	application.Shutdown(context.Background())
}

func applyStartupFlagEnv(args []string) error {
	flags := flag.NewFlagSet("thoughtflow", flag.ContinueOnError)
	flagToEnv := map[string]string{
		"host":                 "THOUGHTFLOW_HOST",
		"port":                 "THOUGHTFLOW_PORT",
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
