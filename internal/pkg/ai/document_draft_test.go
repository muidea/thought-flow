package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/documentprofile"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/testutil/llmfixture"
)

func TestDocumentGenerationUsesExactProfileSchemaAndWholeResponse(t *testing.T) {
	profile, err := documentprofile.Parse([]byte(ds.Builtins()[2].Roles[0].ProfileSnapshot), documentprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"title":"Agent design","summary":"Unapproved extracted design","sections":{"scope":{"content":"Preserve the user goal and existing proposal."},"behavior":{"content":"Decompose requirements, execute and verify."},"design":{"content":"Use workspace profiles and execution records."},"failures":{"content":"Checkpoint recovery remains a proposal."},"verification":{"content":"Check requirement and overall goal evidence; no tests have run."}},"references":[],"assumptions":["Persistence details remain unconfirmed."]}`
	for _, tc := range []struct {
		name, content, reason, field string
	}{
		{"valid", valid, "", ""},
		{"metadata-inside-sections", `{"title":"Agent design","sections":{"assumptions":{"content":"PRIVATE-CANARY"},"references":{"content":"no sources"}}}`, "invalid_document_sections", "sections"},
		{"section-string", strings.Replace(valid, `{"content":"Preserve the user goal and existing proposal."}`, `"PRIVATE-CANARY"`, 1), "invalid_field_type", "sections"},
		{"content-object", strings.Replace(valid, `"Preserve the user goal and existing proposal."`, `{"PRIVATE-CANARY":"not a string"}`, 1), "invalid_field_type", "sections.content"},
		{"nested-draft", `{"document":` + valid + `}`, "unknown_field", ""},
		{"unknown-root-field", strings.TrimSuffix(valid, "}") + `,"PRIVATE-CANARY":{"sections":{}}}`, "unknown_field", ""},
		{"empty-required", strings.Replace(valid, "Preserve the user goal and existing proposal.", "  ", 1), "missing_required_field", "sections.scope.content"},
		{"missing-required", `{"title":"Agent design","sections":{}}`, "missing_required_field", "sections.scope.content"},
		{"null", `null`, "", ""},
		{"trailing-object", valid + `{}`, "", ""},
		{"markup-with-inner-json", `<unrecognized>` + valid + `</unrecognized>`, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var request struct {
					Input []struct{ Content string }
					Text  json.RawMessage
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Input) != 2 || r.URL.Path != "/v1/responses" {
					t.Error("invalid Responses request", err)
					w.WriteHeader(400)
					return
				}
				if string(request.Text) != `{"format":{"type":"json_object"}}` {
					t.Error("native json_object requirement changed")
				}
				input := strings.SplitN(strings.TrimPrefix(request.Input[1].Content, "Profile JSON:\n"), "\n\nParameters JSON:", 2)[0]
				var payload struct {
					Schema map[string]any `json:"response_schema"`
				}
				if err := json.Unmarshal([]byte(input), &payload); err != nil || payload.Schema == nil {
					t.Error("missing generation response schema", err)
				} else {
					assertDocumentDraftSchema(t, payload.Schema, profile)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", "draft-contract-request")
				json.NewEncoder(w).Encode(llmfixture.Response(tc.content))
			}))
			defer server.Close()
			p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: time.Second}, appconfig.EmbeddingConfig{})
			draft, err := p.GenerateDocument(WithRequiredJSON(context.Background()), DocumentGenerationRequest{UnapprovedDraft: true, Purpose: ds.EngineeringPurpose, Profile: profile})
			if tc.name == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				rendered := documentprofile.Render(profile, draft, nil)
				if rendered.Validation.Status != models.ArchiveValidationValid || !strings.Contains(rendered.Content, "workspace profiles") || !strings.Contains(rendered.Content, "Persistence details remain unconfirmed.") {
					t.Fatal("valid full document was changed or rejected", rendered.Validation)
				}
			} else {
				var failure ProviderError
				if !errors.As(err, &failure) || failure.RequestID != "draft-contract-request" || CanAutomaticallyRetry(err) || strings.Contains(err.Error(), "PRIVATE-CANARY") {
					t.Fatal("invalid outer document accepted, leaked or made retryable", err)
				}
				if tc.reason != "" && (failure.Reason != tc.reason || failure.Field != tc.field) {
					t.Fatal("lost trusted protocol diagnostic", failure.Reason, failure.Field, err)
				}
				diagnostic, ok := JSONResponseErrorDiagnostic(err)
				if !ok || diagnostic.Content != tc.content {
					t.Fatal("failed response evidence lost", ok)
				}
			}
			if calls != 1 {
				t.Fatal("invalid structure caused an automatic retry", calls)
			}
		})
	}
}

func assertDocumentDraftSchema(t *testing.T, schema map[string]any, profile documentprofile.DocumentProfile) {
	t.Helper()
	defs := schema["$defs"].(map[string]any)
	properties := defs["DocumentDraft"].(map[string]any)["properties"].(map[string]any)
	sections := properties["sections"].(map[string]any)
	keys := sections["properties"].(map[string]any)
	var required []string
	for _, key := range sections["required"].([]any) {
		required = append(required, key.(string))
	}
	if len(keys) != len(profile.Sections) || !reflect.DeepEqual(required, profile.RequiredSectionKeys()) || sections["additionalProperties"] != false {
		t.Fatal("schema and frozen profile differ", sections)
	}
	for _, section := range profile.Sections {
		if keys[section.Key].(map[string]any)["$ref"] != "#/$defs/DocumentSection" {
			t.Fatal("missing content DTO", section.Key)
		}
	}
	content := defs["DocumentSection"].(map[string]any)["properties"].(map[string]any)["content"].(map[string]any)
	if content["type"] != "string" || properties["assumptions"].(map[string]any)["type"] != "array" || properties["references"].(map[string]any)["type"] != "array" {
		t.Fatal("section content or root metadata has the wrong wire type")
	}
}

func TestDocumentDraftAllowsExplicitProfileSectionNames(t *testing.T) {
	profile := documentprofile.DocumentProfile{Sections: []documentprofile.SectionSpec{{Key: "scope", Required: true}, {Key: "assumptions", Required: true}, {Key: "references"}}}
	draft, err := decodeDocumentDraft(`{"sections":{"scope":{"content":"Scope"},"assumptions":{"content":"Explicitly declared section"},"references":{"content":"Profile content"}}}`, profile)
	if err != nil || validateDocumentDraftSections(draft, profile) != nil {
		t.Fatal("declared names were banned", err)
	}
	profile.Validation.AllowUnknownSections = true
	if _, err := decodeDocumentDraft(`{"sections":{"extension":{"content":"Explicitly allowed extension"}}}`, profile); err != nil {
		t.Fatal(err)
	}
}
