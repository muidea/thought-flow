package biz

import (
	"context"
	"errors"
	"testing"
	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/models"
	"time"
)

type contextAwarePlan struct{}

func (contextAwarePlan) Expand(ctx context.Context, _ ai.ExpandRequest) (ai.ExpandResult, error) {
	select {
	case <-ctx.Done():
		return ai.ExpandResult{}, ctx.Err()
	case <-time.After(20 * time.Millisecond):
		return ai.ExpandResult{Plan: "independent success"}, nil
	}
}
func TestPipelineFailureDoesNotCancelSiblingOrEraseFailedStage(t *testing.T) {
	service, search, _, _, _, _ := newTestService(t)
	search.err = errors.New("unavailable")
	service.provider = contextAwarePlan{}
	thought := models.Thought{ID: "test", RelatedThoughtIDs: []string{"keep"}, ExpansionPlan: "old"}
	related, plan, _, _, err := service.runPipeline(context.Background(), thought, models.ThoughtContent{Original: "input"})
	if err == nil || plan != "independent success" || len(related) != 1 || related[0] != "keep" {
		t.Fatalf("partial result lost: %v %q %v", related, plan, err)
	}
}
