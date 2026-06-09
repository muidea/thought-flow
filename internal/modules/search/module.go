package search

import (
	"context"
	"path/filepath"
	"sync"

	cd "github.com/muidea/magicCommon/def"
	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/framework/plugin/module"
	"github.com/muidea/magicCommon/task"

	"thoughtflow/internal/modules/search/biz"
	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/searchdb"
	"thoughtflow/internal/pkg/workspace"
)

const moduleID = "search"

func init() {
	module.Register(New())
}

func New() *Module {
	return &Module{}
}

type Module struct {
	service *biz.Service
	store   *searchdb.Store
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
	return 300
}

func (m *Module) Setup(ctx context.Context, eventHub event.Hub, backgroundRoutine task.BackgroundRoutine) *cd.Error {
	cfg := appconfig.Load()
	ws, err := workspace.Open(ctx, cfg)
	if err != nil {
		return cd.WrapError(cd.Unexpected, err, "open workspace")
	}
	dbPath := cfg.Search.DuckDBPath
	if !filepath.IsAbs(dbPath) {
		dbPath = filepath.Join(ws.RootPath, dbPath)
	}
	store, err := searchdb.Open(ctx, dbPath)
	if err != nil {
		return cd.WrapError(cd.Unexpected, err, "open duckdb")
	}
	m.store = store
	m.service = biz.NewService(ws, jobstore.New(ws.JobsPath), store, eventHub, backgroundRoutine, ai.NewEmbeddingProvider(cfg.AI), dbPath)
	setCurrent(m.service)
	eventHub.Subscribe("thought.captured", m.service)
	eventHub.Subscribe("thought.refined", m.service)
	eventHub.Subscribe("topic.updated", m.service)
	return nil
}

func (m *Module) Run(ctx context.Context) *cd.Error {
	_ = ctx
	return nil
}

func (m *Module) Teardown(ctx context.Context) {
	_ = ctx
	setCurrent(nil)
	if m.store != nil {
		_ = m.store.Close()
	}
}
