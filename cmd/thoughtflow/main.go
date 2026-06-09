package main

import (
	"context"
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
