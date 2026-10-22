package documentstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"thoughtflow/internal/pkg/models"
)

func data(body string) RevisionData {
	return RevisionData{Body: body, Contract: models.DocumentContract{SchemaVersion: 1}, Sources: models.SourceManifest{SchemaVersion: 1}}
}
func publish(t *testing.T, s *Store, id string, g uint64, d RevisionData) models.DocumentHead {
	t.Helper()
	unlock := s.Lock(id)
	defer unlock()
	h, e := s.Publish(id, g, d)
	if e != nil {
		t.Fatal(e)
	}
	return h
}
func TestNormalizationPreservesMarkdownSemantics(t *testing.T) {
	original := "    indented\r\nline  \r\n\r\n\r\n```\r\n  code  \r\n```\r\n\r\n"
	expected := "    indented\nline  \n\n\n```\n  code  \n```\n"
	if actual := Normalize(original); actual != expected || Normalize(actual) != actual {
		t.Fatalf("normalization changed semantic bytes: %q", actual)
	}
	if HashJSON(map[string]string{"b": "2", "a": "1"}) != HashJSON(map[string]string{"a": "1", "b": "2"}) {
		t.Fatal("unstable map hash")
	}
}
func TestImmutableRevisionsAndConcurrentCAS(t *testing.T) {
	s := New(t.TempDir())
	h := publish(t, s, "doc", 0, data("first  \n"))
	var wg sync.WaitGroup
	success, conflicts := 0, 0
	var mu sync.Mutex
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := s.Lock("doc")
			defer unlock()
			_, e := s.Publish("doc", h.Generation, data("next"))
			mu.Lock()
			defer mu.Unlock()
			if e == nil {
				success++
			} else if errors.Is(e, ErrConflict) {
				conflicts++
			} else {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if success != 1 || conflicts != 1 {
		t.Fatalf("CAS outcomes: %d %d", success, conflicts)
	}
	d, e := s.Revision("doc", h.WorkingRevisionID)
	if e != nil || d.Body != "first  \n" {
		t.Fatalf("old revision mutated: %+v %v", d, e)
	}
	if e = s.PutRecord("doc", "reports", "r1", map[string]string{"state": "first"}); e != nil {
		t.Fatal(e)
	}
	if e = s.PutRecord("doc", "reports", "r1", map[string]string{"state": "second"}); !os.IsExist(e) {
		t.Fatalf("record overwritten: %v", e)
	}
}
func TestCrashBeforeHEADDoesNotExposeCompleteOrphan(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	initial := publish(t, s, "doc", 0, data("first"))
	s.Fault = func(point string) error { return errors.New("injected crash at " + point) }
	unlock := s.Lock("doc")
	_, e := s.Publish("doc", initial.Generation, data("orphan"))
	unlock()
	if e == nil {
		t.Fatal("fault not reached")
	}
	restarted := New(root)
	head, e := restarted.Head("doc")
	if e != nil || head != initial {
		t.Fatalf("orphan became visible: %+v %v", head, e)
	}
	entries, e := os.ReadDir(filepath.Join(s.Root, "doc", "revisions"))
	if e != nil || len(entries) != 2 {
		t.Fatalf("expected complete unpublished revision, entries=%d err=%v", len(entries), e)
	}
}
func TestHashesAndSymlinksAreChecked(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	head := publish(t, s, "doc", 0, data("original"))
	path := filepath.Join(s.Root, "doc", "revisions", head.WorkingRevisionID, "body.md")
	if e := os.WriteFile(path, []byte("tampered"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Revision("doc", head.WorkingRevisionID); !errors.Is(e, ErrInvalid) {
		t.Fatalf("tampering accepted: %v", e)
	}
	for _, id := range []string{"../outside", "x/y", "..", strings.Repeat("a", 161)} {
		if _, e := s.Head(id); e == nil {
			t.Fatalf("invalid ID %q accepted", id)
		}
	}
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(s.Root, "linked")); e != nil {
		t.Fatal(e)
	}
	unlock := s.Lock("linked")
	_, e := s.Publish("linked", 0, data("private"))
	unlock()
	if !errors.Is(e, os.ErrPermission) {
		t.Fatalf("symlink accepted: %v", e)
	}
}

func TestStoreAllowsSymlinkAboveWorkspace(t *testing.T) {
	base := t.TempDir()
	realParent := filepath.Join(base, "real-parent")
	if err := os.Mkdir(realParent, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "parent-alias")
	if err := os.Symlink(realParent, alias); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(alias, "workspace")
	s := New(root)
	head := publish(t, s, "doc", 0, data("through parent alias"))
	restarted := New(root)
	if actual, err := restarted.Head("doc"); err != nil || actual != head {
		t.Fatalf("HEAD through workspace parent alias: %+v, %v", actual, err)
	}
	if actual, err := restarted.Revision("doc", head.WorkingRevisionID); err != nil || actual.Body != "through parent alias\n" {
		t.Fatalf("revision through workspace parent alias: %+v, %v", actual, err)
	}
}

func TestStoreRejectsSymlinksAtAndInsideWorkspace(t *testing.T) {
	for _, relative := range []string{".", "documents", "documents/doc", "documents/doc/head.json"} {
		t.Run(relative, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "workspace")
			link := filepath.Join(root, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			if relative == "documents/doc/head.json" {
				outside = filepath.Join(outside, "head.json")
				if err := os.WriteFile(outside, []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			s := New(root)
			if _, err := s.Head("doc"); !errors.Is(err, os.ErrPermission) {
				t.Fatalf("linked path %s accepted: %v", relative, err)
			}
			unlock := s.Lock("doc")
			defer unlock()
			if _, err := s.Publish("doc", 0, data("must not escape")); !errors.Is(err, os.ErrPermission) {
				t.Fatalf("write through linked path %s accepted: %v", relative, err)
			}
		})
	}
}
