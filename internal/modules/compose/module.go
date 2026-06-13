package compose

import (
	"context"
	"sync"

	cd "github.com/muidea/magicCommon/def"
	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/framework/plugin/module"
	"github.com/muidea/magicCommon/task"

	"thoughtflow/internal/modules/capture"
	"thoughtflow/internal/modules/compose/biz"
	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/composedraft"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/workspace"
)

const moduleID = "compose"

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

// Weight sits between the capture module (which we depend on for
// the save-as-thought sink) and the refiner module (whose
// SynthesisProvider we share). 170 keeps the registration order
// late enough that capture.Current() is populated by the time we
// wire our CaptureSink in Setup.
func (m *Module) Weight() int {
	return 170
}

func (m *Module) Setup(_ context.Context, eventHub event.Hub, _ task.BackgroundRoutine) *cd.Error {
	cfg := appconfig.Load()
	ws, err := workspace.Open(context.Background(), cfg)
	if err != nil {
		return cd.WrapError(cd.Unexpected, err, "open workspace")
	}
	capture := capture.Current()
	if capture == nil {
		return cd.NewError(cd.Unexpected, "compose module requires capture module to be initialized first")
	}
	m.service = biz.NewService(
		ws,
		composedraft.New(ws.RootPath),
		jobstore.New(ws.JobsPath),
		eventHub,
		ai.NewSynthesisProvider(cfg.LLM),
		capture,
	)
	m.service.SetModel(cfg.LLM.ChatModel)
	setCurrent(m.service)
	return nil
}

func (m *Module) Run(_ context.Context) *cd.Error {
	return nil
}

func (m *Module) Teardown(_ context.Context) {
	setCurrent(nil)
}
