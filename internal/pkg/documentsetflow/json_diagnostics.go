package documentsetflow

import (
	"errors"
	"fmt"
	"strings"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/objectlocation"
)

// Failed completions are evidence only. They never become a plan, public
// artifact or replay command. The existing private namespace protects them
// from Git, package export and resource discovery/reads.
func (s *Service) persistJSONFailure(set, operation, stage string, failure error) (error, string) {
	diagnostic, ok := ai.JSONResponseErrorDiagnostic(failure)
	if !ok {
		return failure, ""
	}
	if !documentstore.ValidID(set) || !documentstore.ValidID(operation) || (stage != "planning" && stage != "review" && stage != "generation") {
		return errors.Join(failure, errors.New("invalid private JSON diagnostic identity")), ""
	}
	key := strings.TrimPrefix(documentstore.HashJSON([]string{diagnostic.RequestID, diagnostic.ResponseHash}), "sha256:")
	path := documentsetstore.Private(set) + "/diagnostics/" + stage + "/" + operation + "-" + key + ".json"
	record := struct {
		OperationID string                    `json:"operation_id"`
		Stage       string                    `json:"stage"`
		Response    ai.JSONResponseDiagnostic `json:"response"`
	}{operation, stage, diagnostic}
	if err := objectlocation.WriteJSON(s.Store.Root, path, record, true); err != nil {
		return errors.Join(failure, fmt.Errorf("persist private JSON response diagnostic: %w", err)), ""
	}
	return failure, path
}
