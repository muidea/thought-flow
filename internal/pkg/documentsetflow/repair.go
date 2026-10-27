package documentsetflow

import (
	"context"
	"sort"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentstore"
)

// repair only revises explanations in the frozen plan. Scope/type/identity
// errors need a new shared design and cannot be 'fixed' by rewriting facts.
func (s *Service) repair(ctx context.Context, run *ds.Run, candidate *ds.Revision) error {
	for run.RepairRounds < run.Plan.Profile.Limits.MaxRepairRounds {
		issues := map[string][]ds.Issue{}
		for _, i := range candidate.Report.Issues {
			if i.Hard || i.Severity != "error" {
				continue
			}
			for _, l := range i.Locations {
				issues[l.Member] = append(issues[l.Member], i)
			}
		}
		if len(issues) == 0 {
			return nil
		}
		run.RepairRounds++
		run.RepairIssues = issues
		if e := s.save(run); e != nil {
			return e
		}
		artifacts := map[string]ds.Artifact{}
		for _, a := range candidate.Members {
			artifacts[a.Key] = a
		}
		// Changing an upstream revision requires revalidating its downstream
		// generation dependencies as well. Schedule in the same frozen DAG.
		changed := true
		for changed {
			changed = false
			for _, m := range run.Plan.Members {
				if len(issues[m.Key]) > 0 {
					continue
				}
				for _, dep := range m.DependsOn {
					if len(issues[dep]) > 0 {
						issues[m.Key] = issues[dep]
						changed = true
						break
					}
				}
			}
		}
		pending := map[string]bool{}
		for key := range issues {
			pending[key] = true
		}
		for len(pending) > 0 {
			progress := false
			for _, m := range run.Plan.Members {
				if !pending[m.Key] {
					continue
				}
				deps := map[string]string{}
				wait := false
				for _, k := range m.DependsOn {
					if pending[k] {
						wait = true
					}
					deps[k] = artifacts[k].RevisionID
				}
				if wait {
					continue
				}
				if e := ctx.Err(); e != nil {
					return e
				}
				a, e := s.generate(ctx, run, m, deps)
				if e != nil {
					return e
				}
				artifacts[m.Key] = a
				delete(pending, m.Key)
				progress = true
				candidate.Members = nil
				for _, a := range artifacts {
					candidate.Members = append(candidate.Members, a)
				}
				sort.Slice(candidate.Members, func(i, j int) bool { return candidate.Members[i].Key < candidate.Members[j].Key })
				run.Candidate = candidate
				if e = s.save(run); e != nil {
					return e
				}
			}
			if !progress {
				return ds.ErrInvalid
			}
		}
		candidate.Report = ds.Validate(*candidate)
		if candidate.Report.StructuralStatus != "passed" {
			return nil
		}
		if e := s.review(ctx, run, candidate); e != nil {
			return e
		}
		if candidate.Report.Status == "ready" {
			return nil
		}
	}
	return nil
}

// A planner cannot relabel an old quote as authorization to change or delete a
// confirmed fact. Changes need evidence from the newly added user messages;
// otherwise retain a blocking proposal for the next conversation turn.
func guardConfirmedFacts(next *ds.SharedDesign, previous ds.Plan, capture ds.CaptureSnapshot) {
	meaning := func(f ds.Fact) string {
		return documentstore.HashJSON(struct {
			Kind, Name, Content, Owner string
			Fields                     []ds.Field
		}{f.Kind, f.Name, f.Content, f.Owner, f.Fields})
	}
	for key, old := range previous.Shared.Conditions {
		value, ok := next.Conditions[key]
		if old.Status == "confirmed" && old.Value != nil && ok && value.Value != nil && *old.Value != *value.Value && value.Source.Message < previous.Capture.MessageCount {
			value.Status = "proposed"
			next.Conditions[key] = value
		}
	}
	for _, old := range previous.Shared.Facts {
		if old.Status != "confirmed" {
			continue
		}
		found := false
		for i, f := range next.Facts {
			if f.ID != old.ID {
				continue
			}
			found = true
			if meaning(f) != meaning(old) && f.Source.Message < previous.Capture.MessageCount {
				next.Facts[i].Status = "proposed"
				next.Questions = append(next.Questions, ds.Question{ID: "changed-" + old.ID, Message: "Confirmed fact changed without new user evidence: " + old.Name, Blocking: true, Members: []string{old.Owner, f.Owner}})
			}
			break
		}
		if !found {
			authorized := false
			for _, change := range next.ScopeChanges {
				if change.FactID == old.ID && change.Action == "remove" && change.Status == "confirmed" && change.Source.Message >= previous.Capture.MessageCount && ds.UserEvidence(capture, change.Source) && change.Reason != "" && change.Coverage != "" {
					for _, key := range change.Affected {
						if key == old.Owner {
							authorized = true
						}
					}
				}
			}
			if authorized {
				continue
			}
			old.Status = "unresolved"
			next.Facts = append(next.Facts, old)
			next.Questions = append(next.Questions, ds.Question{ID: "removed-" + old.ID, Message: "Confirm removal or replacement coverage for " + old.Name, Blocking: true, Members: []string{old.Owner}})
		}
	}
}
