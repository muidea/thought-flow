package documentsetflow

import (
	"context"
	"testing"

	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/topicstore"
)

func TestPlanCreatesReadableTopicWithoutChangingSetIdentity(t *testing.T) {
	snapshot, shared := fixture()
	shared.Inputs["project_name"] = "订单平台设计"
	s := New(t.TempDir(), Options{Enabled: true, Snapshot: func(string) (ds.CaptureSnapshot, error) { return snapshot, nil }})
	t.Cleanup(s.Close)
	p := ds.Builtins()[0]
	zero, one := uint64(0), uint64(1)
	req := PlanRequest{OperationID: "title-one", Name: p.Name, ExpectedGeneration: &zero, CaptureGeneration: &one, ProfileID: p.ID, ProfileVersion: p.Version, ProfileHash: p.Hash, Shared: &shared}
	ctx := context.Background()
	first, err := s.Plan(ctx, snapshot.SessionID, req)
	if err != nil {
		t.Fatal(err)
	}
	topic, err := topicstore.New(s.Store.Root).Get(ctx, first.SetID)
	if err != nil || topic.Name != "订单平台设计" || topic.ID != first.SetID || !ds.OpaqueSetTitle(topic.ID) {
		t.Fatal(topic, err)
	}
	replay, err := s.Plan(ctx, snapshot.SessionID, req)
	if err != nil || replay.Hash != first.Hash || replay.SetID != first.SetID {
		t.Fatal("plan replay drifted", replay, err)
	}
	req.OperationID = "title-two"
	second, err := s.Plan(ctx, snapshot.SessionID, req)
	if err != nil || second.SetID == first.SetID {
		t.Fatal(second, err)
	}
	topic, err = topicstore.New(s.Store.Root).Get(ctx, second.SetID)
	if err != nil || topic.Name != "订单平台设计" {
		t.Fatal(topic, err)
	}
	_, err = topicstore.New(s.Store.Root).Settings(ctx, first.SetID, models.TopicSettingsRequest{OperationID: "rename-title", ExpectedGeneration: 1, Name: "人工维护的专题名"})
	if err != nil {
		t.Fatal(err)
	}
	req.OperationID, req.SetID, req.Name = "title-three", first.SetID, "新模型标题"
	if _, err = s.Plan(ctx, snapshot.SessionID, req); err != nil {
		t.Fatal(err)
	}
	topic, err = topicstore.New(s.Store.Root).Get(ctx, first.SetID)
	if err != nil || topic.Name != "人工维护的专题名" {
		t.Fatal("subsequent plan overwrote user name", topic, err)
	}
}
