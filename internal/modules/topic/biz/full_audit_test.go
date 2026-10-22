package biz

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muidea/magicCommon/event"
	gitbiz "thoughtflow/internal/modules/git_sync/biz"
	"thoughtflow/internal/pkg/eventstream"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/scratchpad"
	"thoughtflow/internal/pkg/topicstore"
)

func TestTopicEventsCommitConcreteFilesAndTrackedDeletions(t *testing.T) {
	root := t.TempDir()
	ws := topicTestWorkspace(root)
	ctx := context.Background()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return string(out)
	}
	git("init")
	git("config", "user.name", "Audit Test")
	git("config", "user.email", "test@example.test")
	hub := event.NewHub(4)
	jobs := jobstore.New(ws.JobsPath)
	commits := gitbiz.NewService(ws, jobs, hub, nil, true, 20*time.Millisecond)
	stream := eventstream.New(100)
	hub.Subscribe(models.EventGitCommitRequested, commits)
	hub.Subscribe(models.EventGitCommitSucceeded, stream)
	hub.Subscribe(models.EventGitCommitFailed, stream)
	defer func() { hub.Terminate(ctx); commits.Close() }()
	service := NewService(ws, jobs, topicstore.New(root), hub, nil, nil, nil, nil)
	disabled := false
	topic, err := service.CreateTopic(ctx, models.TopicCreateRequest{Name: "Protected", AutoWeave: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	thought := serviceTestThought("20261022-100000-member", "Member")
	if err := markdown.WriteThought(root, thought, models.ThoughtContent{Original: "Unique member fact"}); err != nil {
		t.Fatal(err)
	}
	proposal, err := service.PreviewWeave(ctx, topic.ID, thought.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.store.SaveSessionCandidates(ctx, topic.ID, []models.TopicSessionCandidate{{SessionID: "private"}}); err != nil {
		t.Fatal(err)
	}
	approval := "topics/" + topic.Slug + "/approvals/" + proposal.ID + ".yaml"
	wait := func(predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			for _, ev := range stream.History() {
				if ev.EventType == models.EventGitCommitFailed {
					t.Fatalf("topic auto commit failed: %+v", ev.Payload)
				}
			}
			if predicate() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("topic commit did not arrive")
	}
	exists := func(path string) bool {
		return exec.Command("git", "-C", root, "cat-file", "-e", "HEAD:"+path).Run() == nil
	}
	wait(func() bool { return exists(approval) })
	if _, err := service.AcceptWeave(ctx, topic.ID, models.TopicWeaveAcceptRequest{ProposalID: proposal.ID}); err != nil {
		t.Fatal(err)
	}
	membership := "topics/" + topic.Slug + "/memberships/" + thought.ID + ".yaml"
	wait(func() bool { return exists(membership) })
	before, err := service.store.ReadDocument(ctx, topic.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"compose.draft_created", "compose.draft_saved", "compose.draft_deleted"} {
		service.Notify(event.NewEvent(kind, "compose", "#", event.NewHeader(), models.DomainEvent{EventType: kind}), nil)
	}
	after, _ := service.store.ReadDocument(ctx, topic.ID)
	if after != before {
		t.Fatal("Compose event overwrote accepted prose")
	}
	if err := markdown.DeleteThought(root, thought.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.RemoveDeletedThought(ctx, thought.ID); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !exists(membership) })
	if got := git("ls-tree", "-r", "--name-only", "HEAD"); strings.Contains(got, "candidates.yaml") || strings.Contains(got, ".scratchpad") {
		t.Fatalf("private runtime in Git: %s", got)
	}
	document, _ := service.store.ReadDocument(ctx, topic.ID)
	if !strings.Contains(document, "Unique member fact") || !strings.Contains(document, "Source deleted") {
		t.Fatal("deleted source erased authored content or lacks review notice")
	}
}

func TestHistoricalCandidatesRefreshAndUniqueLineage(t *testing.T) {
	root := t.TempDir()
	ws := topicTestWorkspace(root)
	pads := newStubScratchpadProvider()
	for _, sp := range []scratchpad.Scratchpad{
		{SessionID: "new", SessionContext: scratchpad.SessionContext{Goal: "vector research"}},
		{SessionID: "reopen", SourceThoughtID: "existing", SessionContext: scratchpad.SessionContext{Goal: "vector research"}},
	} {
		pads.put(sp)
	}
	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, nil, nil, pads)
	disabled := false
	topic, err := service.CreateTopic(context.Background(), models.TopicCreateRequest{Name: "Vector", AutoWeave: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	impacts, err := service.ListCandidates(context.Background(), topic.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(impacts) != 2 {
		t.Fatalf("historical candidates=%+v", impacts)
	}
	for _, impact := range impacts {
		if (impact.SessionID == "reopen") != (impact.Source == models.TopicCandidateSourceThoughtReopen) {
			t.Fatalf("wrong lineage: %+v", impact)
		}
	}
	_, err = service.UpdateTopic(context.Background(), topic.ID, models.TopicUpdateRequest{Name: "Other", Rules: models.TopicRule{Keywords: models.KeywordRule{Any: []string{"unrelated"}}}, AutoWeave: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	impacts, err = service.ListCandidates(context.Background(), topic.ID)
	if err != nil || len(impacts) != 0 {
		t.Fatalf("stale rule candidates: %+v %v", impacts, err)
	}
	if _, err = os.Stat(filepath.Join(root, "topics", topic.Slug, "index.md")); err != nil {
		t.Fatal(err)
	}
}
