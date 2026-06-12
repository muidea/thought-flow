package expander

import (
	"context"
	"sync"
	"time"

	cd "github.com/muidea/magicCommon/def"
	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/framework/plugin/module"
	"github.com/muidea/magicCommon/task"

	"thoughtflow/internal/modules/expander/biz"
	searchmodule "thoughtflow/internal/modules/search"
	topicmodule "thoughtflow/internal/modules/topic"
	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/thoughtlock"
	"thoughtflow/internal/pkg/webfetch"
	"thoughtflow/internal/pkg/workspace"
)

const moduleID = "expander"

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

// Weight sits between refiner (150) and topic (400) so the expander
// subscribes to `thought.refined` after refiner has finished writing
// the refinement and before topic.rebuild can re-evaluate the
// (now-expanded) thought.
func (m *Module) Weight() int {
	return 250
}

func (m *Module) Setup(ctx context.Context, eventHub event.Hub, backgroundRoutine task.BackgroundRoutine) *cd.Error {
	cfg := appconfig.Load()
	ws, err := workspace.Open(ctx, cfg)
	if err != nil {
		return cd.WrapError(cd.Unexpected, err, "open workspace")
	}
	m.service = biz.NewService(ws, jobstore.New(ws.JobsPath), eventHub, backgroundRoutine, ai.NewExpandProvider(cfg.LLM), webfetch.New(30*time.Second), thoughtlock.Default())
	m.service.SetSearcherLookup(func() biz.Searcher {
		svc := searchmodule.Current()
		if svc == nil {
			return nil
		}
		return svc
	})
	m.service.SetTopicSuggesterLookup(func() biz.TopicSuggester {
		svc := topicmodule.Current()
		if svc == nil {
			return nil
		}
		return svc
	})
	setCurrent(m.service)
	eventHub.Subscribe(models.EventThoughtRefined, m.service)
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
