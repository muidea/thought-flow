package thoughtflow

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	cd "github.com/muidea/magicCommon/def"
	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/framework/plugin/module"
	"github.com/muidea/magicCommon/task"
	engine "github.com/muidea/magicEngine/http"

	"thoughtflow/internal/modules/application/thoughtflow/service"
	"thoughtflow/internal/modules/capture"
	capturebiz "thoughtflow/internal/modules/capture/biz"
	"thoughtflow/internal/modules/git_sync"
	"thoughtflow/internal/modules/refiner"
	"thoughtflow/internal/modules/search"
	"thoughtflow/internal/modules/topic"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/eventstream"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/scratchpad"
	"thoughtflow/internal/pkg/workspace"
)

const moduleID = "application.thoughtflow"

func init() {
	module.Register(New())
}

func New() *Module {
	return &Module{}
}

type Module struct {
	server      gracefulHTTPServer
	serverDone  chan error
	stream      *eventstream.Stream
	httpService *service.Service
}

type gracefulHTTPServer interface {
	ListenAndServe() error
	Shutdown(ctx context.Context) error
}

var (
	defaultMu     sync.RWMutex
	defaultStream *eventstream.Stream
)

func CurrentStream() *eventstream.Stream {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultStream
}

func setCurrentStream(stream *eventstream.Stream) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultStream = stream
}

func (m *Module) ID() string {
	return moduleID
}

func (m *Module) Weight() int {
	return 1000
}

func (m *Module) Setup(ctx context.Context, eventHub event.Hub, backgroundRoutine task.BackgroundRoutine) *cd.Error {
	cfg := appconfig.Load()
	ws, err := workspace.Open(ctx, cfg)
	if err != nil {
		return cd.WrapError(cd.Unexpected, err, "open workspace")
	}
	captureService := capture.Current()
	if captureService == nil {
		return cd.NewError(cd.Unexpected, "capture service is not ready")
	}
	refinerService := refiner.Current()
	if refinerService == nil {
		return cd.NewError(cd.Unexpected, "refiner service is not ready")
	}
	searchService := search.Current()
	if searchService == nil {
		return cd.NewError(cd.Unexpected, "search service is not ready")
	}
	topicService := topic.Current()
	if topicService == nil {
		return cd.NewError(cd.Unexpected, "topic service is not ready")
	}
	gitService := git_sync.Current()
	if gitService == nil {
		return cd.NewError(cd.Unexpected, "git-sync service is not ready")
	}

	m.stream = eventstream.New(200)
	setCurrentStream(m.stream)
	for _, eventID := range []string{
		"thought.captured",
		"thought.refine_started",
		"thought.refined",
		"thought.refine_failed",
		"thought.expanded",
		"search.index_updated",
		"search.index_failed",
		"search.reindex_started",
		"search.reindex_finished",
		"topic.created",
		"topic.matched",
		"topic.updated",
		"topic.rebuild_started",
		"topic.rebuild_failed",
		"git.commit_succeeded",
		"git.commit_failed",
		"job.updated",
	} {
		eventHub.Subscribe(eventID, m.stream)
	}

	jobs := jobstore.New(ws.JobsPath)
	scratchpadStore := scratchpad.New(ws.ScratchpadPath)
	scratchpadSvc := capture.NewScratchpadService(scratchpadStore,
		capture.WithCapture(captureService),
		capturebiz.WithEventHub(eventHub),
	)
	topic.InjectScratchpadProvider(scratchpadStore)
	registry := engine.NewRouteRegistry()
	m.httpService = service.New(registry, captureService, scratchpadSvc, refinerService, searchService, topicService, scratchpadStore, gitService, jobs, eventHub, backgroundRoutine, m.stream, ws, cfg)
	m.httpService.RegisterRoutes()
	m.server, err = newGracefulHTTPServer(cfg.Server, registry)
	if err != nil {
		return cd.WrapError(cd.Unexpected, err, "create http server")
	}
	m.serverDone = make(chan error, 1)
	return nil
}

func newGracefulHTTPServer(cfg appconfig.ServerConfig, registry engine.RouteRegistry) (gracefulHTTPServer, error) {
	engineServer := engine.NewHTTPServer()
	engineServer.Bind(registry)
	handler, ok := engineServer.(http.Handler)
	if !ok {
		return nil, errors.New("magicEngine http server does not implement http.Handler")
	}
	return &http.Server{
		Addr:              net.JoinHostPort(cfg.Host, cfg.Port),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}, nil
}

func (m *Module) Run(ctx context.Context) *cd.Error {
	_ = ctx
	if m.server == nil {
		return cd.NewError(cd.Unexpected, "http server is not ready")
	}
	if m.serverDone == nil {
		m.serverDone = make(chan error, 1)
	}
	go func() {
		err := m.server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		if err != nil {
			slog.Error("thoughtflow http server stopped with error", "error", err)
		}
		m.serverDone <- err
	}()
	return nil
}

func (m *Module) Teardown(ctx context.Context) {
	if m.httpService != nil {
		m.httpService.Close()
	}
	if m.server != nil {
		shutdownCtx := ctx
		cancel := func() {}
		if _, ok := shutdownCtx.Deadline(); !ok {
			shutdownCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
		}
		if err := m.server.Shutdown(shutdownCtx); err != nil {
			slog.Error("thoughtflow http server shutdown failed", "error", err)
		}
		cancel()
	}
	m.httpService = nil
	setCurrentStream(nil)
}
