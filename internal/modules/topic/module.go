package topic

import (
	"context"
	"sync"

	cd "github.com/muidea/magicCommon/def"
	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/framework/plugin/module"
	"github.com/muidea/magicCommon/task"

	searchmodule "thoughtflow/internal/modules/search"
	"thoughtflow/internal/modules/topic/biz"
	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/topicstore"
	"thoughtflow/internal/pkg/workspace"
)

const moduleID = "topic"

func init() {
	module.Register(New())
}

func New() *Module {
	return &Module{}
}

type Module struct {
	service *biz.Service
}

var (
	defaultMu      sync.RWMutex
	defaultService *biz.Service
)

func Current() *biz.Service {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultService
}

func setCurrent(service *biz.Service) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultService = service
}

func (m *Module) ID() string {
	return moduleID
}

func (m *Module) Weight() int {
	return 400
}

func (m *Module) Setup(ctx context.Context, eventHub event.Hub, backgroundRoutine task.BackgroundRoutine) *cd.Error {
	cfg := appconfig.Load()
	ws, err := workspace.Open(ctx, cfg)
	if err != nil {
		return cd.WrapError(cd.Unexpected, err, "open workspace")
	}
	m.service = biz.NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(ws.RootPath, topicstore.WithWeaveProvider(ai.NewWeaveProvider(cfg.LLM))), eventHub, backgroundRoutine, ai.NewEmbeddingProvider(cfg.Embedding), searchmodule.Current(), nil)
	setCurrent(m.service)
	eventHub.Subscribe("thought.refined", m.service)
	eventHub.Subscribe("search.index_updated", m.service)
	eventHub.Subscribe(models.EventScratchpadContextUpdated, m.service)
	eventHub.Subscribe(models.EventScratchpadCommitted, m.service)
	eventHub.Subscribe("compose.draft_created", m.service)
	eventHub.Subscribe("compose.draft_saved", m.service)
	return nil
}

// InjectScratchpadProvider wires the scratchpad store into the topic
// service after the capture module has created it. The application
// layer is the only place that knows the full wiring order, so the
// topic module leaves the provider slot nil at Setup() and lets the
// application fill it in. A nil provider disables session-candidate
// matching but does not break the existing thought-matching flow.
func InjectScratchpadProvider(provider biz.ScratchpadProvider) {
	svc := Current()
	if svc == nil {
		return
	}
	svc.SetScratchpadProvider(provider)
}

func InjectComposeDraftProvider(provider biz.ComposeDraftProvider) {
	svc := Current()
	if svc == nil {
		return
	}
	svc.SetComposeDraftProvider(provider)
}

func (m *Module) Run(ctx context.Context) *cd.Error {
	_ = ctx
	return nil
}

func (m *Module) Teardown(ctx context.Context) {
	_ = ctx
	setCurrent(nil)
}
