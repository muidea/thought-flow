package capture

import (
	"context"
	"sync"

	cd "github.com/muidea/magicCommon/def"
	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/framework/plugin/module"
	"github.com/muidea/magicCommon/task"

	"thoughtflow/internal/modules/capture/biz"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/thoughtlock"
	"thoughtflow/internal/pkg/workspace"
)

const moduleID = "capture"

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

// NewScratchpadService is a thin re-export of biz.NewScratchpadService
// so the application module can wire the scratchpad layer without
// importing the biz package directly (which would tangle the
// dependency graph: biz depends on scratchpad, application depends
// on capture).
func NewScratchpadService(store biz.ScratchpadStore, options ...biz.ScratchpadServiceOption) *biz.ScratchpadService {
	return biz.NewScratchpadService(store, options...)
}

// WithCapture re-exports biz.WithCapture for the application layer.
func WithCapture(c biz.CaptureCommitter) biz.ScratchpadServiceOption {
	return biz.WithCapture(c)
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
	return 100
}

func (m *Module) Setup(ctx context.Context, eventHub event.Hub, backgroundRoutine task.BackgroundRoutine) *cd.Error {
	cfg := appconfig.Load()
	ws, err := workspace.Open(ctx, cfg)
	if err != nil {
		return cd.WrapError(cd.Unexpected, err, "open workspace")
	}
	store := jobstore.New(ws.JobsPath)
	m.service = biz.NewService(ws, store, eventHub, biz.WithDuplicatePolicy(cfg.Capture.DuplicatePolicy), biz.WithLocker(thoughtlock.Default()))
	setCurrent(m.service)
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
