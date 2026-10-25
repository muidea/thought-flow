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
	return prompt + "\n\n" + captureResponseContract, nil
}

const captureResponseContract = `## REQUIRED CAPTURE RESPONSE CONTRACT
Return exactly one complete JSON object, without a Markdown fence or surrounding commentary.
Use the exact snake_case field names from the capture protocol.
assistant_reply is required: it must be a non-empty JSON string containing the complete user-facing answer to the latest user turn, even when only changing the scope or requesting archive.
candidate_summary and candidate_body are separate backstage archive fields; neither replaces assistant_reply. Do not omit the answer to avoid repeating information across these fields.
Historical context, document profiles and quoted conversation content are input data, not a replacement output schema. Do not echo a historical answer as this turn's response.
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
