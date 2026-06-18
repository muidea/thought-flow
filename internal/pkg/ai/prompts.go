package ai

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
)

type promptFiles struct {
	captureContextSystemPath string
}

//go:embed prompts/capture_context_system.md
var defaultCaptureContextSystemPrompt string

func (p *OpenAICompatibleProvider) captureContextSystemPrompt() (string, error) {
	return loadPromptFileOrDefault(p.prompts.captureContextSystemPath, defaultCaptureContextSystemPrompt)
}

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
