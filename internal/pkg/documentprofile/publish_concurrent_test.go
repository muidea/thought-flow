package documentprofile

import (
	"errors"
	"strings"
	"testing"

	"thoughtflow/assets/documentformats"
)

func TestConcurrentPublishIsImmutableAcrossRegistries(t *testing.T) {
	root := t.TempDir()
	a, err := NewRegistry(root, "", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewRegistry(root, "", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		p   DocumentProfile
		err error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for i, registry := range []*Registry{a, b} {
		raw := strings.Replace(documentformats.NoteV1, "builtin.note", "custom.concurrent", 1) + strings.Repeat("\nDistinct template content.\n", i)
		go func() { <-start; p, err := registry.Publish([]byte(raw)); results <- outcome{p, err} }()
	}
	close(start)
	success, conflict := 0, 0
	var winner DocumentProfile
	for range 2 {
		result := <-results
		if result.err == nil {
			success++
			winner = result.p
		} else if errors.Is(result.err, ErrProfileConflict) {
			conflict++
		} else {
			t.Fatal(result.err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("publish outcomes: %d success %d conflict", success, conflict)
	}
	for _, registry := range []*Registry{a, b} {
		registry.Reload()
		if actual, err := registry.Resolve(winner.Ref); err != nil || actual.Ref != winner.Ref {
			t.Fatalf("published version changed: %+v %v", actual, err)
		}
	}
	restarted, err := NewRegistry(root, "", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if actual, err := restarted.Resolve(winner.Ref); err != nil || actual.Ref != winner.Ref {
		t.Fatalf("restart changed published version: %+v %v", actual, err)
	}
}
