package biz

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"thoughtflow/internal/pkg/documentvalidation"

	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/scratchpad"
)

var ErrCaptureParameters = errors.New("invalid capture parameters")

func reconcileParameters(old scratchpad.SessionContext, next map[string]string) map[string]string {
	values := mergeStringMaps(old.DocumentParameters, next)
	for key, p := range old.ParameterValues {
		if p.Confirmed {
			values[key] = p.Value
		}
	}
	return values
}

func contextSourceLinks(old scratchpad.SessionContext, next []string) []string {
	if old.SourceLinksConfirmed {
		return append([]string(nil), old.SourceLinks...)
	}
	return mergeContextStrings(old.SourceLinks, next)
}

func archiveSources(links []string) *models.SourceManifest {
	sources := &models.SourceManifest{SchemaVersion: 1, Sources: []models.DocumentSource{}}
	for i, link := range links {
		sources.Sources = append(sources.Sources, models.DocumentSource{ID: fmt.Sprintf("S%d", i+1), Type: "external", Locator: link, VerificationStatus: "unverified"})
	}
	return sources
}

func mergeIssues(old []models.CaptureIssue, next []models.CaptureIssue, legacy []string) []models.CaptureIssue {
	out := append([]models.CaptureIssue(nil), old...)
	for _, text := range legacy {
		kind := "contradiction"
		lower := strings.ToLower(text)
		if strings.Contains(lower, "response_format") || strings.Contains(lower, "conversion_unsupported") {
			kind = "diagnostic"
		}
		next = append(next, models.CaptureIssue{Kind: kind, Message: text})
	}
	for _, item := range next {
		item.Message = strings.TrimSpace(item.Message)
		if item.Message == "" {
			continue
		}
		switch item.Kind {
		case "limitation", "diagnostic":
			item.Blocking = false
		default:
			item.Kind = "contradiction"
			item.Blocking = true
		}
		item.ID = models.ContentHash(item.Kind + ":" + item.Message)[7:23]
		item.Status = "open" // Only the user resolution endpoint can resolve an item.
		found := false
		for _, prev := range out {
			if prev.ID == item.ID {
				found = true
				break
			}
		}
		if !found {
			out = append(out, item)
		}
	}
	return out
}

func activeConflicts(issues []models.CaptureIssue) []string {
	var out []string
	for _, i := range issues {
		if i.Blocking && i.Status != "resolved" {
			out = append(out, i.Message)
		}
	}
	return out
}

func (s *ScratchpadService) normalizeProfileContext(ctx *scratchpad.SessionContext) {
	if s.profiles == nil {
		return
	}
	p, err := s.resolveArchiveProfile(scratchpad.Scratchpad{SessionContext: *ctx}, nil)
	if err != nil {
		ctx.ArchiveReadiness = "needs_input"
		return
	}
	ctx.FrozenProfile = p.Snapshot
	ctx.DocumentParameters = profileParameters(p, ctx.DocumentParameters)
	if ctx.ParameterValues == nil {
		ctx.ParameterValues = map[string]scratchpad.ParameterValue{}
	}
	ctx.MissingProfileInputs = nil
	for _, input := range p.Inputs {
		value := ctx.DocumentParameters[input.Key]
		pv, ok := ctx.ParameterValues[input.Key]
		if (!ok || !pv.Confirmed) && value != "" {
			pv = scratchpad.ParameterValue{Value: value, Source: "extracted"}
			if value == input.Default {
				pv.Source = "default"
				pv.Confirmed = true
			}
			ctx.ParameterValues[input.Key] = pv
		}
		if input.Required && (value == "" || !pv.Confirmed) {
			ctx.MissingProfileInputs = append(ctx.MissingProfileInputs, input.Key)
		}
	}
	ctx.Issues = mergeIssues(ctx.Issues, nil, ctx.Conflicts)
	ctx.Conflicts = activeConflicts(ctx.Issues)
	ctx.ArchiveReadiness = "ready"
	if len(ctx.MissingProfileInputs) > 0 {
		ctx.ArchiveReadiness = "needs_input"
	} else if len(ctx.Conflicts) > 0 {
		ctx.ArchiveReadiness = "blocked"
	} else if p.Ref.Family == models.DocumentFamilyResearch && len(ctx.SourceLinks) == 0 {
		ctx.ArchiveReadiness = "needs_sources"
	}
}

// PatchParameters is a field-level CAS API. A model result can suggest values,
// but cannot override these explicitly confirmed user decisions.
func (s *ScratchpadService) PatchParameters(id string, generation uint64, values map[string]string, resolutions map[string]string, sources ...[]string) (scratchpad.Scratchpad, error) {
	return s.store.Update(id, func(sp *scratchpad.Scratchpad) error {
		if sp.SessionGeneration != generation {
			return scratchpad.ErrConflict
		}
		p, err := s.resolveArchiveProfile(*sp, nil)
		if err != nil {
			return err
		}
		allowed := map[string]bool{}
		for _, in := range p.Inputs {
			allowed[in.Key] = true
		}
		if sp.SessionContext.DocumentParameters == nil {
			sp.SessionContext.DocumentParameters = map[string]string{}
		}
		if sp.SessionContext.ParameterValues == nil {
			sp.SessionContext.ParameterValues = map[string]scratchpad.ParameterValue{}
		}
		for key, value := range values {
			if !allowed[key] {
				return fmt.Errorf("%w: unknown profile input: %s", ErrCaptureParameters, key)
			}
			value = strings.TrimSpace(value)
			sp.SessionContext.DocumentParameters[key] = value
			sp.SessionContext.ParameterValues[key] = scratchpad.ParameterValue{Value: value, Source: "user", Confirmed: true}
		}
		if len(sources) > 0 {
			for _, link := range sources[0] {
				u, e := url.Parse(link)
				if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || documentvalidation.SensitiveLocator(link) {
					return fmt.Errorf("%w: unsafe source link", ErrCaptureParameters)
				}
			}
			sp.SessionContext.SourceLinks = trimNonEmpty(sources[0])
			sp.SessionContext.SourceLinksConfirmed = true
		}
		sp.SessionContext.Issues = mergeIssues(sp.SessionContext.Issues, nil, sp.SessionContext.Conflicts)
		for key, reason := range resolutions {
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("%w: resolution reason is required", ErrCaptureParameters)
			}
			found := false
			for i := range sp.SessionContext.Issues {
				if sp.SessionContext.Issues[i].ID == key {
					sp.SessionContext.Issues[i].Status = "resolved"
					sp.SessionContext.Issues[i].Resolution = reason
					found = true
				}
			}
			if !found {
				return fmt.Errorf("%w: unknown issue: %s", ErrCaptureParameters, key)
			}
		}
		sp.SessionContext.Conflicts = activeConflicts(sp.SessionContext.Issues)
		s.normalizeProfileContext(&sp.SessionContext)
		sp.ArchivePreview = nil
		sp.Operation = nil
		// Editing inputs is not a fresh authorization to auto-archive a new generation.
		sp.ArchiveIntent = scratchpad.ArchiveIntentMenu
		sp.SessionContext.ArchiveIntent = scratchpad.ArchiveIntentMenu
		return nil
	})
}

func archivePreflight(p documentprofile.DocumentProfile, sp scratchpad.Scratchpad, parameters map[string]string) []models.ValidationIssue {
	var issues []models.ValidationIssue
	for _, input := range p.Inputs {
		if input.Required && (parameters[input.Key] == "" || (p.SchemaVersion == 2 && !sp.SessionContext.ParameterValues[input.Key].Confirmed && input.Default == "")) {
			issues = append(issues, models.ValidationIssue{Code: "input_required", Severity: "error", Section: input.Key, Message: "Confirmed input is required: " + input.Label})
		}
	}
	if p.SchemaVersion == 2 && p.Ref.Family == models.DocumentFamilyResearch && len(sp.SessionContext.SourceLinks) == 0 {
		issues = append(issues, models.ValidationIssue{Code: "research_evidence_missing", Severity: "error", Section: "sources", Message: "Research requires real source links; save an unverified draft or provide sources."})
	}
	return issues
}
