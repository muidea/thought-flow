package refiner

import (
	"context"
	"sync"
	"time"

	cd "github.com/muidea/magicCommon/def"
	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/framework/plugin/module"
	"github.com/muidea/magicCommon/task"

	"thoughtflow/internal/modules/refiner/biz"
	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/synthesisstore"
	"thoughtflow/internal/pkg/webfetch"
	"thoughtflow/internal/pkg/workspace"
)

const moduleID = "refiner"

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
	return 150
}

func (m *Module) Setup(ctx context.Context, eventHub event.Hub, backgroundRoutine task.BackgroundRoutine) *cd.Error {
	cfg := appconfig.Load()
	ws, err := workspace.Open(ctx, cfg)
	if err != nil {
		return cd.WrapError(cd.Unexpected, err, "open workspace")
	}
	m.service = biz.NewService(ws, jobstore.New(ws.JobsPath), eventHub, backgroundRoutine, ai.NewRefineProvider(cfg.LLM, cfg.Embedding), webfetch.New(30*time.Second))
	m.service.ConfigureSynthesis(ai.NewSynthesisProvider(cfg.LLM), synthesisstore.New(ws.RootPath))
	setCurrent(m.service)
	eventHub.Subscribe("thought.captured", m.service)
	return nil
}

func (m *Module) Run(ctx context.Context) *cd.Error {
	_ = ctx
	return nil
}

func (m *Module) Teardown(ctx context.Context) {
	_ = ctx
	setCurrent(nil)
}
