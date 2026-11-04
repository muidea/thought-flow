package biz

import (
	"errors"
	"fmt"
	"strings"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

func (s *ScratchpadService) persistCaptureJSONFailure(session string, generation uint64, failure error) error {
	diagnostic, ok := ai.JSONResponseErrorDiagnostic(failure)
	if !ok || s.jsonDiagnosticRoot == "" {
		return failure
	}
	// Hash identities rather than using user-controlled session or gateway
	// header text as a filesystem component.
	sessionKey := strings.TrimPrefix(models.ContentHash(session), "sha256:")
	responseKey := strings.TrimPrefix(models.ContentHash(diagnostic.RequestID+"\n"+diagnostic.ResponseHash), "sha256:")
	path := fmt.Sprintf(".content-registry/private/capture/%s/diagnostics/%d-%s.json", sessionKey, generation, responseKey)
	record := struct {
		SessionID       string                    `json:"session_id"`
		InputGeneration uint64                    `json:"input_generation"`
		Stage           string                    `json:"stage"`
		Response        ai.JSONResponseDiagnostic `json:"response"`
	}{session, generation, "capture_context", diagnostic}
	if err := objectlocation.WriteJSON(s.jsonDiagnosticRoot, path, record, true); err != nil {
		return errors.Join(failure, fmt.Errorf("persist private capture JSON response diagnostic: %w", err))
	}
	return failure
}
