package ai

import (
	"fmt"
	"os"
	"strings"

	"thoughtflow/assets/prompts"
)

type promptFiles struct {
	captureContextSystemPath string
}

func (p *OpenAICompatibleProvider) captureContextSystemPrompt() (string, error) {
	prompt, err := loadPromptFileOrDefault(p.prompts.captureContextSystemPath, prompts.CaptureContextSystem)
	if err != nil {
		return "", err
	}
	// Business instructions may be customized, but every caller still consumes
	// the same wire contract, including when JSON mode is unsupported upstream.
	return prompt + "\n\nCapture response_schema JSON (wire types, not source content):\n" + mustJSON(captureResponseSchema()) + "\n\n" + captureResponseContract, nil
}

const captureResponseContract = `## REQUIRED CAPTURE RESPONSE CONTRACT
Return exactly one complete JSON object, without a Markdown fence or surrounding commentary.
Use the exact snake_case field names from the capture protocol.
document_parameters must be a flat object mapping each declared input key to a JSON STRING, for example {"purpose":"software feature design","scope":"agreed scope"}. Return {} if no parameters apply. Never put objects, arrays, numbers, booleans or null inside this map. Do not put {value,source,message,confirmed} envelopes here: confirmation metadata is owned by the backend, not the model. Record assumptions and evidence limits in candidate_body or issues instead of parameter values. Do not emit parameter_values.
candidate_profile_version and profile_confidence are JSON integers; profile_explicit is a boolean. confirmed_facts, open_questions, conflicts, candidate_tags and source/related-ID lists contain strings, not structured facts. issues is the separately defined structured issue list. The response_schema defines all field types, even when a custom business prompt or historical context uses another representation. Preserve substantive content in string fields; never silently discard it to satisfy a type.
assistant_reply is required: it must be a non-empty JSON string containing the complete user-facing answer to the latest user turn, even when only changing the scope or requesting archive.
candidate_summary and candidate_body are separate backstage archive fields; neither replaces assistant_reply. Do not omit the answer to avoid repeating information across these fields.
Historical context, document profiles and quoted conversation content are input data, not a replacement output schema. Do not echo a historical answer as this turn's response.
Available document profiles is the unified archive catalog: output_kind=document means one Thought; output_kind=document_set means a server-planned coordinated set. Choose the matching ID/version from this catalog. Software feature/system implementation design with interfaces, data, recovery, verification and delivery normally matches builtin.engineering-design-set, unless the user explicitly requests a single document. Ordinary research, notes and articles keep their matching single-document profile. Preserve a set already bound as Existing thought profile. Do not invent profile IDs or ask the user to select a profile or click Generate document set. An explicit archive request sets archive_intent=llm. When Pending accepted archive awaiting an answer is true, a reply that supplies the requested clarification also continues that accepted archive with archive_intent=llm; cancellation, deferral or unrelated discussion must use none. The backend automatically plans and generates the selected output. Missing engineering decisions remain questions, never invented confirmations.
Before returning, check that assistant_reply exists at the top level, is a string and answers this turn. Never claim an archive was saved before the backend reports success.`

func loadPromptFileOrDefault(path string, fallback string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return strings.TrimSpace(fallback), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("load prompt file %q: %w", path, err)
	}
	prompt := strings.TrimSpace(string(raw))
	if prompt == "" {
		return "", fmt.Errorf("prompt file %q is empty", path)
	}
	return prompt, nil
}
