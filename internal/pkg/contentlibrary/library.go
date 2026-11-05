// Package contentlibrary projects saved content for reading without granting
// access to unpublished candidates or changing the authoritative stores.
package contentlibrary

import (
	"context"
	"os"
	"sort"
	"strings"
	"thoughtflow/internal/pkg/contentreference"
	"time"

	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetflow"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/pkg/topicstore"
)

type Selection = contentreference.Selection
type Member struct {
	Key       string                   `json:"key"`
	ThoughtID string                   `json:"thought_id"`
	Title     string                   `json:"title"`
	Role      string                   `json:"role"`
	Filename  string                   `json:"filename,omitempty"`
	Body      string                   `json:"body,omitempty"`
	Modified  bool                     `json:"modified"`
	Available bool                     `json:"available"`
	Resources []models.ResourceBinding `json:"resources,omitempty"`
}
type Version struct {
	Selection
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	Count     int       `json:"count"`
}
type Item struct {
	CaptureSessionID string                  `json:"capture_session_id,omitempty"`
	SourceRecords    []models.DocumentSource `json:"source_records,omitempty"`
	SourceCount      int                     `json:"source_count"`
	ID               string                  `json:"id"`
	Kind             string                  `json:"kind"`
	Title            string                  `json:"title"`
	Description      string                  `json:"description"`
	State            string                  `json:"state"`
	UpdatedAt        time.Time               `json:"updated_at"`
	Selection        Selection               `json:"selection"`
	Versions         []Version               `json:"versions,omitempty"`
	Members          []Member                `json:"members,omitempty"`
	Sources          []Member                `json:"sources,omitempty"`
	Thought          *models.Thought         `json:"thought,omitempty"`
	SetID            string                  `json:"set_id,omitempty"`
	MemberKey        string                  `json:"member_key,omitempty"`
	NewVersion       bool                    `json:"new_version"`
	TaskState        string                  `json:"task_state,omitempty"`
	TaskRunID        string                  `json:"task_run_id,omitempty"`
	Validation       string                  `json:"validation"`
	Questions        []string                `json:"questions,omitempty"`
	Unavailable      bool                    `json:"unavailable"`
}
type Detail struct {
	Item
	Head         *ds.Head         `json:"head,omitempty"`
	Report       *ds.Report       `json:"report,omitempty"`
	Finalization *ds.Finalization `json:"finalization,omitempty"`
}
type Query struct {
	Layout, Kind, Search, Sort, State string
	Page, Size                        int
	Overview                          bool
}
type Result struct {
	Items            []Item      `json:"items"`
	Total            int         `json:"total"`
	Page             int         `json:"page"`
	Size             int         `json:"page_size"`
	Counts           Counts      `json:"counts"`
	Attention        []Attention `json:"attention,omitempty"`
	AttentionTotal   int         `json:"attention_total,omitempty"`
	ArchivedSessions []string    `json:"archived_sessions,omitempty"`
}
type Counts struct {
	Content   int `json:"content"`
	Sets      int `json:"sets"`
	Notes     int `json:"notes"`
	Documents int `json:"documents"`
	Drafts    int `json:"drafts"`
}
type Store struct {
	Root       string
	cachedRuns *[]ds.Run
}

func New(root string) *Store { return &Store{Root: root} }
func (s *Store) runs() ([]ds.Run, error) {
	if s.cachedRuns != nil {
		return *s.cachedRuns, nil
	}
	return documentsetstore.New(s.Root).Runs()
}
func title(p ds.Plan) string {
	return ds.DisplayTitle(p.Shared, "", p.Profile.Name)
}
func description(p ds.Plan) string { return p.Shared.Inputs["goal"] }
func (s *Store) versions(id string) ([]Version, *ds.Head, error) {
	st := documentsetstore.New(s.Root)
	out := []Version{}
	h, e := st.Head(id)
	var head *ds.Head
	if e == nil {
		head = &h
		seen := map[string]bool{}
		for _, start := range append([]string{h.Working, h.Finalized}, retained(h)...) {
			for r := start; r != "" && !seen[r]; {
				seen[r] = true
				v, err := st.Read(id, r)
				if err != nil {
					return nil, nil, err
				}
				state := "working"
				for _, f := range h.Finalizations {
					if f.RevisionID == r {
						state = "finalized"
					}
				}
				out = append(out, Version{Selection: Selection{"revision", r}, State: state, CreatedAt: v.CreatedAt, Count: len(v.Members)})
				r = v.Parent
			}
		}
	} else if !os.IsNotExist(e) {
		return nil, nil, e
	}
	runs, e := s.runs()
	if e != nil {
		return nil, nil, e
	}
	for _, r := range runs {
		if r.Plan.SetID != id || !savedArchive(r) {
			continue
		}
		n := 0
		for _, m := range r.Draft.Members {
			if m.Key != "archive-source" {
				n++
			}
		}
		out = append(out, Version{Selection: Selection{"archive", r.ID}, State: "draft", CreatedAt: r.CreatedAt, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, head, nil
}
func retained(h ds.Head) []string {
	out := []string{}
	for _, f := range h.Finalizations {
		out = append(out, f.RevisionID)
	}
	return out
}
func savedArchive(r ds.Run) bool {
	return r.ArchiveDraft && r.Draft != nil && r.Draft.NotesReady && r.State == "draft_saved"
}
func (s *Store) Detail(ctx context.Context, id string, sel Selection) (Detail, error) {
	for attempts := 0; attempts < 3; attempts++ {
		epoch := objectlocation.For(s.Root).Epoch()
		result, err := s.detail(ctx, id, sel)
		if objectlocation.For(s.Root).Epoch() == epoch {
			return result, err
		}
		if ctx.Err() != nil {
			return Detail{}, ctx.Err()
		}
	}
	return Detail{}, ds.ErrConflict
}
func (s *Store) detail(ctx context.Context, id string, sel Selection) (Detail, error) {
	d := Detail{}
	runs, err := s.runs()
	if err != nil {
		return d, err
	}
	local := *s
	local.cachedRuns = &runs
	s = &local
	if !documentstore.ValidID(id) {
		return d, ds.ErrInvalid
	}
	versions, h, e := s.versions(id)
	if e != nil {
		return d, e
	}
	if len(versions) == 0 {
		return d, os.ErrNotExist
	}
	if sel.Kind == "" && sel.ID == "" {
		sel = versions[0].Selection
		if h != nil && h.Finalized != "" {
			sel = Selection{"revision", h.Finalized}
		}
	}
	var chosen *Version
	for i := range versions {
		if versions[i].Selection == sel {
			chosen = &versions[i]
			break
		}
	}
	if chosen == nil {
		return d, os.ErrNotExist
	}
	d.Item = Item{ID: id, Kind: "document_set", State: chosen.State, Selection: sel, Versions: versions, Members: []Member{}, Sources: []Member{}, UpdatedAt: chosen.CreatedAt, Questions: []string{}}
	d.Head = h
	var p ds.Plan
	if sel.Kind == "archive" {
		r, err := documentsetstore.New(s.Root).Run(sel.ID)
		if err != nil {
			return d, err
		}
		if !savedArchive(r) || r.Plan.SetID != id {
			return d, os.ErrNotExist
		}
		p = r.Plan
		if e = documentsetflow.VerifySavedArchive(s.Root, r); e != nil {
			return d, e
		}
		seen := map[string]bool{}
		for _, a := range r.Draft.Members {
			if e = ctx.Err(); e != nil {
				return d, e
			}
			if seen[a.Key] || a.Hash != documentstore.Hash([]byte(a.Body)) {
				return d, ds.ErrInvalid
			}
			seen[a.Key] = true
			path, err := objectlocation.SafePath(s.Root, documentsetstore.Private(id)+"/drafts/"+r.ID+"/"+a.Filename)
			if err != nil {
				return d, err
			}
			raw, err := objectlocation.ReadFileBounded(path, 32<<20)
			if err != nil {
				return d, err
			}
			if string(raw) != a.Body {
				return d, ds.ErrConflict
			}
			role := strings.SplitN(a.Key, ":", 2)[0]
			m := Member{Key: a.Key, ThoughtID: a.ThoughtID, Title: a.Title, Filename: a.Filename, Role: role, Body: a.Body}
			s.markCopy(&m, a.Hash)
			if a.Key == "archive-source" {
				d.Sources = append(d.Sources, m)
			} else {
				d.Members = append(d.Members, m)
			}
		}
		d.Validation = "not_reviewed"
	} else {
		r, err := documentsetstore.New(s.Root).Read(id, sel.ID)
		if err != nil {
			return d, err
		}
		p = r.Plan
		d.Report = &r.Report
		d.Validation = r.Report.Status
		for _, a := range r.Members {
			role := ""
			for _, pm := range p.Members {
				if pm.Key == a.Key {
					role = pm.Role
				}
			}
			m := Member{Key: a.Key, ThoughtID: a.DocumentID, Title: a.Contract.Title, Role: role, Body: a.Body, Resources: a.Resources.Bindings}
			s.markCopy(&m, documentstore.Hash([]byte(a.Body)))
			d.Members = append(d.Members, m)
		}
		if h != nil {
			for i := range h.Finalizations {
				if h.Finalizations[i].RevisionID == sel.ID {
					f := h.Finalizations[i]
					d.Finalization = &f
				}
			}
		}
	}
	d.SourceRecords = p.Capture.Sources.Sources
	d.SourceCount = len(d.Sources) + len(d.SourceRecords)
	d.Title = title(p)
	d.CaptureSessionID = p.Capture.SessionID
	d.Description = description(p)
	for _, q := range p.Shared.Questions {
		diagnostic := q.Kind == "" && (q.ID == "modules" || strings.HasPrefix(q.ID, "fact-") || strings.HasPrefix(q.ID, "input-") || strings.HasPrefix(q.ID, "condition-") || strings.HasPrefix(q.ID, "gap-"))
		if !diagnostic && q.Blocking && strings.TrimSpace(q.Resolution) == "" {
			d.Questions = append(d.Questions, q.Message)
		}
	}
	d.NewVersion = versions[0].Selection != sel
	latestTask := time.Time{}
	for _, r := range runs {
		if r.Plan.SetID == id && (r.UpdatedAt.After(latestTask) || (r.UpdatedAt.Equal(latestTask) && r.ID > d.TaskRunID)) {
			latestTask = r.UpdatedAt
			d.TaskRunID = r.ID
			d.TaskState = r.State
		}
	}
	if latestTask.After(d.UpdatedAt) {
		d.UpdatedAt = latestTask
	}
	return d, nil
}
func (s *Store) markCopy(m *Member, hash string) {
	_, c, e := markdown.ReadThought(s.Root, m.ThoughtID)
	m.Available = e == nil
	if e == nil {
		m.Modified = documentstore.Hash([]byte(c.AINotes)) != hash
	}
}
func (s *Store) all(ctx context.Context) ([]Item, error) {
	runs, err := s.runs()
	if err != nil {
		return nil, err
	}
	local := *s
	local.cachedRuns = &runs
	s = &local
	ids, e := markdown.ListThoughtIDs(s.Root)
	if e != nil {
		return nil, e
	}
	notes := []models.Thought{}
	setIDs := map[string]bool{}
	locations := map[string]models.ObjectLocation{}
	for _, id := range ids {
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		t, _, err := markdown.ReadThought(s.Root, id)
		if err != nil {
			return nil, err
		}
		notes = append(notes, t)
		if t.DesignDraft != nil {
			setIDs[t.DesignDraft.SetID] = true
		}
		if l, err := objectlocation.For(s.Root).Lookup(id); err == nil {
			locations[id] = l
			if l.ManagedSetID != "" {
				setIDs[l.ManagedSetID] = true
			}
		}
	}
	for _, r := range runs {
		if savedArchive(r) {
			setIDs[r.Plan.SetID] = true
		}
	}
	topics, e := topicstore.New(s.Root).List(ctx)
	if e != nil {
		return nil, e
	}
	for _, t := range topics {
		// Ordinary topics allow names such as Chinese titles as their IDs;
		// only the narrower document-set namespace can contain a SetHead.
		if !documentstore.ValidID(t.ID) {
			continue
		}
		if _, err := documentsetstore.New(s.Root).Head(t.ID); err == nil {
			setIDs[t.ID] = true
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	out := []Item{}
	grouped := map[string]bool{}
	for id := range setIDs {
		d, err := s.Detail(ctx, id, Selection{})
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, err
			}
			continue
		}
		i := d.Item
		for n := range i.Members {
			grouped[i.Members[n].ThoughtID] = true
			i.Members[n].Body = ""
		}
		for n := range i.Sources {
			grouped[i.Sources[n].ThoughtID] = true
			i.Sources[n].Body = ""
		}
		out = append(out, i)
	}
	// Every version's copies belong to the same set. Never let old attempts flood
	// the standalone list; an unavailable archive leaves its notes readable.
	for _, t := range notes {
		sid := ""
		if t.DesignDraft != nil {
			sid = t.DesignDraft.SetID
		}
		if sid == "" {
			sid = locations[t.ID].ManagedSetID
		}
		if sid != "" && setIDs[sid] {
			for _, i := range out {
				if i.ID == sid {
					grouped[t.ID] = true
					break
				}
			}
		}
		if grouped[t.ID] {
			continue
		}
		tcopy := t
		out = append(out, Item{ID: t.ID, Kind: "thought", Title: first(t.DisplayTitle, t.UserTitle, t.ExtractedTitle, t.ID), Description: t.Summary, State: "note", UpdatedAt: t.UpdatedAt, Thought: &tcopy})
	}
	return out, nil
}
func first(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
func (s *Store) List(ctx context.Context, q Query) (Result, error) {
	for attempts := 0; attempts < 3; attempts++ {
		epoch := objectlocation.For(s.Root).Epoch()
		result, err := s.list(ctx, q)
		if objectlocation.For(s.Root).Epoch() == epoch {
			return result, err
		}
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
	}
	return Result{}, ds.ErrConflict
}
func (s *Store) list(ctx context.Context, q Query) (Result, error) {
	r := Result{Items: []Item{}}
	if q.Page < 1 {
		q.Page = 1
	}
	if q.Size < 1 {
		q.Size = 24
	}
	if q.Size > 100 {
		q.Size = 100
	}
	if q.Kind != "" && q.Kind != "all" && q.Kind != "thought" && q.Kind != "document_set" {
		return r, ds.ErrInvalid
	}
	if q.Layout != "" && q.Layout != "grouped" && q.Layout != "flat" {
		return r, ds.ErrInvalid
	}
	if q.Sort != "" && q.Sort != "updated" && q.Sort != "title" {
		return r, ds.ErrInvalid
	}
	if q.State != "" && q.State != "draft" {
		return r, ds.ErrInvalid
	}
	if q.Overview {
		runs, err := s.runs()
		if err != nil {
			return r, err
		}
		local := *s
		local.cachedRuns = &runs
		s = &local
	}
	items, e := s.all(ctx)
	if e != nil {
		return r, e
	}
	r.Page = q.Page
	r.Size = q.Size
	if q.Overview {
		r.Attention, r.AttentionTotal = s.attention(items)
		for _, item := range items {
			if item.CaptureSessionID != "" {
				r.ArchivedSessions = append(r.ArchivedSessions, item.CaptureSessionID)
			}
		}
		sort.Strings(r.ArchivedSessions)
	}
	for _, i := range items {
		r.Counts.Content++
		if i.Kind == "document_set" {
			r.Counts.Sets++
			r.Counts.Documents += len(i.Members)
			if i.State == "draft" {
				r.Counts.Drafts++
			}
		} else {
			r.Counts.Notes++
			r.Counts.Documents++
		}
	}
	rows := []Item{}
	search := strings.ToLower(strings.TrimSpace(q.Search))
	match := func(i Item) bool {
		hay := i.Title + " " + i.Description
		for _, m := range i.Members {
			hay += " " + m.Title + " " + m.Role
		}
		if i.Thought != nil {
			hay += " " + strings.Join(append(append([]string{}, i.Thought.UserTags...), i.Thought.AITags...), " ")
		}
		return search == "" || strings.Contains(strings.ToLower(hay), search)
	}
	for _, i := range items {
		if q.State != "" && i.State != q.State {
			continue
		}
		if q.Kind != "" && q.Kind != "all" && q.Kind != i.Kind {
			continue
		}
		if q.Layout == "flat" && i.Kind == "document_set" {
			for _, m := range i.Members {
				row := Item{ID: i.ID + ":" + m.Key, Kind: "member", SetID: i.ID, MemberKey: m.Key, Title: m.Title, Description: i.Title, State: i.State, UpdatedAt: i.UpdatedAt, Selection: i.Selection, Members: []Member{m}}
				if match(row) {
					rows = append(rows, row)
				}
			}
		} else if match(i) {
			rows = append(rows, i)
		}
	}
	sort.Slice(rows, func(a, b int) bool {
		if q.Sort == "title" && rows[a].Title != rows[b].Title {
			return rows[a].Title < rows[b].Title
		}
		if rows[a].UpdatedAt.Equal(rows[b].UpdatedAt) {
			return rows[a].ID < rows[b].ID
		}
		return rows[a].UpdatedAt.After(rows[b].UpdatedAt)
	})
	r.Total = len(rows)
	start := (q.Page - 1) * q.Size
	if start < 0 {
		return r, ds.ErrInvalid
	}
	if start < r.Total {
		end := start + q.Size
		if end > r.Total {
			end = r.Total
		}
		r.Items = rows[start:end]
	}
	return r, nil
}
