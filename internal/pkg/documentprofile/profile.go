package documentprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"thoughtflow/internal/pkg/models"
)

var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`)
var fieldKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var tokenPattern = regexp.MustCompile(`\{\{([^{}]+)\}\}`)

type AutoMatchPolicy struct {
	Enabled          bool     `yaml:"enabled" json:"enabled"`
	Priority         int      `yaml:"priority" json:"priority"`
	UseWhen          []string `yaml:"use_when" json:"use_when"`
	PositiveExamples []string `yaml:"positive_examples" json:"positive_examples"`
	NegativeExamples []string `yaml:"negative_examples" json:"negative_examples"`
}

type ProfileInput struct {
	Key      string `yaml:"key" json:"key"`
	Label    string `yaml:"label" json:"label"`
	Required bool   `yaml:"required" json:"required"`
	Default  string `yaml:"default" json:"default,omitempty"`
}

type ValidationPolicy struct {
	AllowUnknownSections    bool `yaml:"allow_unknown_sections" json:"allow_unknown_sections"`
	RequireNonEmptySections bool `yaml:"require_non_empty_sections" json:"require_non_empty_sections"`
	MinimumBodyChars        int  `yaml:"minimum_body_chars" json:"minimum_body_chars"`
	MaximumBodyChars        int  `yaml:"maximum_body_chars" json:"maximum_body_chars"`
	HeadingLevel            int  `yaml:"heading_level" json:"heading_level"`
}

type GenerationPolicy struct {
	AdditionalInstructions string `yaml:"additional_instructions" json:"additional_instructions,omitempty"`
}

type formatFrontMatter struct {
	ID          string           `yaml:"id"`
	Version     int              `yaml:"version"`
	Name        string           `yaml:"name"`
	Family      string           `yaml:"family"`
	Description string           `yaml:"description"`
	Enabled     bool             `yaml:"enabled"`
	AutoMatch   AutoMatchPolicy  `yaml:"auto_match"`
	Inputs      []ProfileInput   `yaml:"inputs"`
	Validation  ValidationPolicy `yaml:"validation"`
	Generation  GenerationPolicy `yaml:"generation"`
}

type SectionSpec struct {
	Key      string `json:"key"`
	Required bool   `json:"required"`
}

type DocumentProfile struct {
	Ref         models.DocumentProfileRef `json:"ref"`
	Name        string                    `json:"name"`
	Description string                    `json:"description"`
	Enabled     bool                      `json:"enabled"`
	AutoMatch   AutoMatchPolicy           `json:"auto_match"`
	Inputs      []ProfileInput            `json:"inputs"`
	Validation  ValidationPolicy          `json:"validation"`
	Generation  GenerationPolicy          `json:"generation"`
	Sections    []SectionSpec             `json:"sections"`
	Template    string                    `json:"-"`
}

type DocumentProfileDescriptor struct {
	ID               string   `json:"id"`
	Version          int      `json:"version"`
	Family           string   `json:"family"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	UseWhen          []string `json:"use_when,omitempty"`
	PositiveExamples []string `json:"positive_examples,omitempty"`
	NegativeExamples []string `json:"negative_examples,omitempty"`
	Priority         int      `json:"priority"`
}

type FormatValidationResult struct {
	Valid   bool                     `json:"valid"`
	Profile *DocumentProfile         `json:"profile,omitempty"`
	Issues  []models.ValidationIssue `json:"issues,omitempty"`
}

type Limits struct {
	MaxFormatBytes int
	MaxSections    int
}

func Parse(raw []byte, limits Limits) (DocumentProfile, error) {
	if limits.MaxFormatBytes > 0 && len(raw) > limits.MaxFormatBytes {
		return DocumentProfile{}, fmt.Errorf("document format exceeds %d bytes", limits.MaxFormatBytes)
	}
	frontMatter, template, err := splitFormat(raw)
	if err != nil {
		return DocumentProfile{}, err
	}
	meta := formatFrontMatter{}
	if err := yaml.Unmarshal([]byte(frontMatter), &meta); err != nil {
		return DocumentProfile{}, fmt.Errorf("parse document format front matter: %w", err)
	}
	profile := DocumentProfile{
		Ref: models.DocumentProfileRef{
			Family:      strings.TrimSpace(meta.Family),
			ProfileID:   strings.TrimSpace(meta.ID),
			Version:     meta.Version,
			ContentHash: contentHash(raw),
		},
		Name:        strings.TrimSpace(meta.Name),
		Description: strings.TrimSpace(meta.Description),
		Enabled:     meta.Enabled,
		AutoMatch:   normalizeAutoMatch(meta.AutoMatch),
		Inputs:      normalizeInputs(meta.Inputs),
		Validation:  normalizeValidation(meta.Validation),
		Generation:  meta.Generation,
		Template:    strings.TrimSpace(template) + "\n",
	}
	profile.Sections, err = parseSections(profile.Template)
	if err != nil {
		return DocumentProfile{}, err
	}
	if err := validateProfile(profile, limits); err != nil {
		return DocumentProfile{}, err
	}
	return profile, nil
}

func Validate(raw []byte, limits Limits) FormatValidationResult {
	profile, err := Parse(raw, limits)
	if err != nil {
		return FormatValidationResult{Issues: []models.ValidationIssue{{Code: "thoughtflow.profile.invalid", Severity: models.ValidationSeverityError, Message: err.Error()}}}
	}
	return FormatValidationResult{Valid: true, Profile: &profile}
}

func (p DocumentProfile) Descriptor() DocumentProfileDescriptor {
	return DocumentProfileDescriptor{
		ID:               p.Ref.ProfileID,
		Version:          p.Ref.Version,
		Family:           p.Ref.Family,
		Name:             p.Name,
		Description:      p.Description,
		UseWhen:          append([]string(nil), p.AutoMatch.UseWhen...),
		PositiveExamples: append([]string(nil), p.AutoMatch.PositiveExamples...),
		NegativeExamples: append([]string(nil), p.AutoMatch.NegativeExamples...),
		Priority:         p.AutoMatch.Priority,
	}
}

func (p DocumentProfile) RequiredSectionKeys() []string {
	out := []string{}
	for _, section := range p.Sections {
		if section.Required {
			out = append(out, section.Key)
		}
	}
	return out
}

func splitFormat(raw []byte) (string, string, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return "", "", errors.New("document format must start with YAML front matter")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return "", "", errors.New("document format front matter is not closed")
	}
	return text[4 : 4+end], text[4+end+len("\n---\n"):], nil
}

func parseSections(template string) ([]SectionSpec, error) {
	sections := []SectionSpec{}
	seen := map[string]struct{}{}
	for _, match := range tokenPattern.FindAllStringSubmatch(template, -1) {
		token := strings.TrimSpace(match[1])
		if token == "title" || token == "summary" || token == "references" || strings.HasPrefix(token, "parameter:") {
			continue
		}
		if !strings.HasPrefix(token, "section:") {
			return nil, fmt.Errorf("unsupported template token %q", token)
		}
		parts := strings.Split(token, "|")
		key := strings.TrimPrefix(parts[0], "section:")
		if !fieldKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("invalid section key %q", key)
		}
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("section %q appears more than once", key)
		}
		seen[key] = struct{}{}
		required := len(parts) == 2 && parts[1] == "required"
		if len(parts) > 2 || len(parts) == 2 && parts[1] != "required" && parts[1] != "optional" {
			return nil, fmt.Errorf("invalid section token %q", token)
		}
		sections = append(sections, SectionSpec{Key: key, Required: required})
	}
	return sections, nil
}

func validateProfile(profile DocumentProfile, limits Limits) error {
	if !identifierPattern.MatchString(profile.Ref.ProfileID) {
		return fmt.Errorf("invalid profile id %q", profile.Ref.ProfileID)
	}
	if profile.Ref.Version <= 0 {
		return errors.New("profile version must be greater than zero")
	}
	if !validFamily(profile.Ref.Family) {
		return fmt.Errorf("invalid document family %q", profile.Ref.Family)
	}
	if profile.Name == "" || profile.Description == "" {
		return errors.New("profile name and description are required")
	}
	if len(profile.Sections) == 0 {
		return errors.New("profile must declare at least one section")
	}
	if limits.MaxSections > 0 && len(profile.Sections) > limits.MaxSections {
		return fmt.Errorf("profile exceeds %d sections", limits.MaxSections)
	}
	required := false
	for _, section := range profile.Sections {
		required = required || section.Required
	}
	if !required {
		return errors.New("profile must declare at least one required section")
	}
	seenInputs := map[string]struct{}{}
	for _, input := range profile.Inputs {
		if !fieldKeyPattern.MatchString(input.Key) {
			return fmt.Errorf("invalid input key %q", input.Key)
		}
		if _, exists := seenInputs[input.Key]; exists {
			return fmt.Errorf("input %q appears more than once", input.Key)
		}
		seenInputs[input.Key] = struct{}{}
	}
	if profile.AutoMatch.Enabled && len(profile.AutoMatch.UseWhen) == 0 {
		return errors.New("auto-match profiles require at least one use_when entry")
	}
	return nil
}

func normalizeAutoMatch(policy AutoMatchPolicy) AutoMatchPolicy {
	policy.UseWhen = normalizeStrings(policy.UseWhen)
	policy.PositiveExamples = normalizeStrings(policy.PositiveExamples)
	policy.NegativeExamples = normalizeStrings(policy.NegativeExamples)
	return policy
}

func normalizeInputs(inputs []ProfileInput) []ProfileInput {
	out := make([]ProfileInput, 0, len(inputs))
	for _, input := range inputs {
		input.Key = strings.TrimSpace(input.Key)
		input.Label = strings.TrimSpace(input.Label)
		input.Default = strings.TrimSpace(input.Default)
		out = append(out, input)
	}
	return out
}

func normalizeValidation(policy ValidationPolicy) ValidationPolicy {
	if policy.MaximumBodyChars <= 0 {
		policy.MaximumBodyChars = 50000
	}
	if policy.HeadingLevel <= 0 {
		policy.HeadingLevel = 2
	}
	return policy
}

func normalizeStrings(values []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func validFamily(family string) bool {
	switch family {
	case models.DocumentFamilyNote, models.DocumentFamilyResearch, models.DocumentFamilyDesign, models.DocumentFamilyArticle, models.DocumentFamilyRecord, models.DocumentFamilyOther:
		return true
	default:
		return false
	}
}

func contentHash(raw []byte) string {
	normalized := strings.TrimSpace(strings.ReplaceAll(string(raw), "\r\n", "\n")) + "\n"
	sum := sha256.Sum256([]byte(normalized))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
