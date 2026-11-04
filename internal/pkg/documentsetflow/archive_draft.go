package documentsetflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/documentprofile"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

// Draft membership is a private presentation of the selected profile, not a
// modification of the frozen plan. Unknown conditions never omit a role.
func archiveDraftMembers(p ds.Plan) ([]ds.PlanMember, error) {
	var out []ds.PlanMember
	for _, role := range p.Profile.Roles {
		condition := p.Shared.Conditions[role.When]
		if role.When != "" && condition.Status == "confirmed" && condition.Value != nil && !*condition.Value {
			continue
		}
		profile, e := documentprofile.Parse([]byte(role.ProfileSnapshot), documentprofile.Limits{})
		if e != nil {
			return nil, e
		}
		keys := []string{role.Key}
		titles := []string{profile.Name}
		if role.Cardinality == "per_confirmed_module" {
			keys = nil
			titles = nil
			for _, f := range p.Shared.Facts {
				if f.Kind == "module" {
					keys = append(keys, role.Key+":"+f.ID)
					titles = append(titles, f.Name)
				}
			}
			if len(keys) == 0 {
				keys = []string{role.Key + ":draft-scope"}
				titles = []string{"模块职责设计（待确认）"}
			}
		}
		for i, key := range keys {
			out = append(out, ds.PlanMember{Key: key, Role: role.Key, Title: titles[i], Filename: strings.ReplaceAll(key, ":", "-") + ".md", ProfileRef: role.ProfileRef, ProfileSnapshot: role.ProfileSnapshot})
		}
	}
	for _, m := range out {
		if m.Key == "archive-source" || m.Filename == "capture-original.md" {
			return nil, ds.ErrInvalid
		}
	}
	if len(out) == 0 || len(out)+1 > p.Profile.Limits.MaxMembers {
		return nil, ds.ErrBudget
	}
	ordered := make([]ds.PlanMember, 0, len(out))
	done := map[string]bool{}
	for len(ordered) < len(out) {
		progress := false
		for _, m := range out {
			if done[m.Key] {
				continue
			}
			waiting := false
			for _, role := range p.Profile.Roles {
				if role.Key == m.Role {
					for _, dependency := range role.DependsOn {
						for _, other := range out {
							if other.Role == dependency && !done[other.Key] {
								waiting = true
							}
						}
					}
				}
			}
			if !waiting {
				ordered = append(ordered, m)
				done[m.Key] = true
				progress = true
			}
		}
		if !progress {
			return nil, ds.ErrInvalid
		}
	}
	return ordered, nil
}

func (s *Service) executeArchiveDraft(ctx context.Context, run *ds.Run) {
	fail := func(e error) { s.fail(run, e) }
	if s.opts.Generator == nil {
		fail(ds.ErrUnavailable)
		return
	}
	var capture ds.CaptureSnapshot
	if e := objectlocation.ReadJSON(s.Store.Root, documentsetstore.Private(run.Plan.SetID)+"/captures/"+run.Plan.ID+".json", &capture); e != nil {
		fail(e)
		return
	}
	if capture.ContextHash != run.Plan.Capture.ContextHash || capture.Generation != run.Plan.Capture.Generation {
		fail(ds.ErrConflict)
		return
	}
	current, readErr := s.opts.Snapshot(run.Plan.Capture.SessionID)
	if readErr != nil {
		fail(readErr)
		return
	}
	if documentstore.HashJSON(current) != documentstore.HashJSON(capture) {
		fail(ds.ErrConflict)
		return
	}
	members, e := archiveDraftMembers(run.Plan)
	if e != nil {
		fail(e)
		return
	}
	sourceHash := documentstore.HashJSON(capture)
	if run.Draft == nil {
		run.Draft = &ds.ArchiveDraftSet{SourceHash: sourceHash, Members: []ds.ArchiveDraftMember{}}
	}
	if run.Draft.SourceHash != sourceHash {
		fail(ds.ErrConflict)
		return
	}
	run.State = "generating"
	if e = s.save(run); e != nil {
		return
	}
	s.emit("document_set.progress", *run, "")
	for _, member := range members {
		if e = ctx.Err(); e != nil {
			fail(e)
			return
		}
		found := false
		for _, saved := range run.Draft.Members {
			if saved.Key == member.Key {
				if e = s.Store.SaveArchiveDraftMember(run.Plan.SetID, run.ID, saved); e != nil {
					fail(e)
					return
				}
				found = true
				break
			}
		}
		if found {
			continue
		}
		profile, err := documentprofile.Parse([]byte(member.ProfileSnapshot), documentprofile.Limits{})
		if err != nil {
			fail(err)
			return
		}
		material := map[string]any{"mode": "private_unapproved_design_draft", "instruction": "Extract and organize ALL existing user requirements and assistant design proposals from the frozen conversation into this role. Preserve concrete modules, interfaces, data, lifecycle and acceptance already discussed. Label assistant-originated or newly derived choices as proposals, unresolved choices as undecided. Do not replace existing designs with requests to reconfirm them. Do not claim confirmation, completed implementation, passed tests, authorization or readiness. Preserve remaining questions in the draft. Software delivery and document archive mechanics are distinct.", "capture": capture, "shared_design": run.Plan.Shared, "member": member, "upstream_drafts": run.Draft.Members}
		raw, _ := json.Marshal(material)
		request := ai.DocumentGenerationRequest{UnapprovedDraft: true, Purpose: run.Plan.Profile.Purpose, Profile: profile, Parameters: run.Plan.Shared.Inputs, Context: ai.DocumentSourceContext{Title: member.Title, Summary: run.Plan.Shared.Inputs["goal"], Body: string(raw)}}
		draft, err := boundedCall(ctx, s, run, "archive-draft", member.Key, request, func(ctx context.Context) (models.DocumentDraft, error) {
			return s.opts.Generator.GenerateDocument(ctx, request)
		})
		if err != nil {
			fail(err)
			return
		}
		if e = ctx.Err(); e != nil {
			fail(e)
			return
		}
		draft.Title = member.Title
		rendered := documentprofile.Render(profile, draft, request.Parameters)
		if rendered.Validation.Status == models.ArchiveValidationInvalid {
			fail(fmt.Errorf("%w: archive draft rendering: %v", ds.ErrRules, rendered.Validation.Issues))
			return
		}
		body := documentstore.Normalize("> 设计草稿：从完整对话自动整理，尚未定稿。用户需求与模型建议保留各自来源；本文不代表实施或交付批准。\n\n" + rendered.Content)
		saved := ds.ArchiveDraftMember{Key: member.Key, Filename: member.Filename, Title: member.Title, Body: body, Hash: documentstore.Hash([]byte(body))}
		if e = s.Store.SaveArchiveDraftMember(run.Plan.SetID, run.ID, saved); e != nil {
			fail(e)
			return
		}
		run.Draft.Members = append(run.Draft.Members, saved)
		if e = s.save(run); e != nil {
			return
		}
		s.emit("document_set.progress", *run, member.Key)
	}
	source := archiveOriginalMember(capture)
	if e = s.Store.SaveArchiveDraftMember(run.Plan.SetID, run.ID, source); e != nil {
		fail(e)
		return
	}
	hasSource := false
	for _, m := range run.Draft.Members {
		if m.Key == source.Key {
			hasSource = true
		}
	}
	if !hasSource {
		run.Draft.Members = append(run.Draft.Members, source)
	}
	// A concurrent turn, source change or new published set cannot be presented
	// as the successful archive of the source snapshot from which this run began.
	if e = s.check(run.Plan); e != nil {
		fail(e)
		return
	}
	if e = s.projectArchiveThoughts(ctx, run); e != nil {
		fail(e)
	}
}

func archiveOriginalMember(capture ds.CaptureSnapshot) ds.ArchiveDraftMember {
	var original strings.Builder
	original.WriteString("# 原始对话（完整保留，包含用户输入及模型建议）\n\n> 本文件保留原始素材，不表示其中建议或操作已获批准。\n\n")
	for i, message := range capture.Messages {
		fmt.Fprintf(&original, "## Message %d · %s\n\n%s\n\n", i, message.Role, message.Text)
	}
	return ds.ArchiveDraftMember{Key: "archive-source", Filename: "capture-original.md", Title: "原始对话（完整保留）", Body: original.String(), Hash: documentstore.Hash([]byte(original.String()))}
}
