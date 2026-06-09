package thoughtflow

import (
	"context"
	"sync"

	cd "github.com/muidea/magicCommon/def"
	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/framework/plugin/module"
	"github.com/muidea/magicCommon/task"
	engine "github.com/muidea/magicEngine/http"

	"thoughtflow/internal/modules/application/thoughtflow/service"
	"thoughtflow/internal/modules/capture"
	"thoughtflow/internal/modules/refiner"
	"thoughtflow/internal/modules/search"
	"thoughtflow/internal/modules/topic"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/eventstream"
	"thoughtflow/internal/pkg/jobstore"
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
	server engine.HTTPServer
	stream *eventstream.Stream
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

	m.stream = eventstream.New(200)
	setCurrentStream(m.stream)
	for _, eventID := range []string{
		"thought.captured",
		"thought.refine_started",
		"thought.refined",
		"thought.refine_failed",
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
	registry := engine.NewRouteRegistry()
	httpService := service.New(registry, captureService, refinerService, searchService, topicService, jobs, m.stream, ws)
	httpService.RegisterRoutes()
	m.server = engine.NewHTTPServer(engine.WithPort(cfg.Server.Port))
	m.server.Bind(registry)
	return nil
}

func (m *Module) Run(ctx context.Context) *cd.Error {
	_ = ctx
	if m.server == nil {
		return cd.NewError(cd.Unexpected, "http server is not ready")
	}
	go m.server.Run()
	return nil
}

func (m *Module) Teardown(ctx context.Context) {
	_ = ctx
	setCurrentStream(nil)
}
