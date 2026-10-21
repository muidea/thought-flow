package models

import "time"

// Document records are immutable. Thought is only a compatibility projection.
type DocumentLink struct {
	Generation uint64 `json:"generation"`
	DocumentID string `json:"document_id"`
	RevisionID string `json:"revision_id"`
}
type DocumentContract struct {
	SchemaVersion int                   `json:"schema_version"`
	ProfileRef    DocumentProfileRef    `json:"profile_ref"`
	Title         string                `json:"title"`
	Purpose       string                `json:"purpose"`
	Audience      string                `json:"audience"`
	Language      string                `json:"language"`
	Parameters    map[string]string     `json:"parameters"`
	Requirements  []DocumentRequirement `json:"requirements,omitempty"`
	Constraints   map[string]string     `json:"constraints,omitempty"`
	Terms         map[string]string     `json:"terms,omitempty"`
	ReviewPolicy  DocumentReviewPolicy  `json:"review_policy"`
	ContextHash   string                `json:"context_hash,omitempty"`
}
type DocumentRequirement struct {
	ID          string   `json:"id"`
	DecisionIDs []string `json:"decision_ids"`
	TestIDs     []string `json:"test_ids"`
	Blocking    bool     `json:"blocking"`
}
type DocumentReviewPolicy struct {
	Version          string `json:"version"`
	SemanticRequired bool   `json:"semantic_required"`
	AllowManual      bool   `json:"allow_manual"`
}
type SourceManifest struct {
	SchemaVersion int              `json:"schema_version"`
	Sources       []DocumentSource `json:"sources"`
}
type DocumentSource struct {
	ResourceID         string    `json:"resource_id,omitempty"`
	ID                 string    `json:"source_id"`
	Type               string    `json:"type"`
	Version            string    `json:"source_version"`
	Hash               string    `json:"source_hash"`
	RetrievedAt        time.Time `json:"retrieved_at"`
	Locator            string    `json:"locator"`
	VerificationStatus string    `json:"verification_status"`
	// Evidence is a locator/hash into private runtime storage, never an excerpt.
	EvidenceHash string `json:"evidence_hash,omitempty"`
}
type DocumentRevision struct {
	DocumentID           string    `json:"document_id"`
	RevisionID           string    `json:"revision_id"`
	ParentRevisionID     string    `json:"parent_revision_id,omitempty"`
	BodyHash             string    `json:"body_hash"`
	ContractHash         string    `json:"contract_hash"`
	ProfileHash          string    `json:"profile_hash"`
	SourceManifestHash   string    `json:"source_manifest_hash"`
	NormalizationVersion string    `json:"normalization_version"`
	CreatedAt            time.Time `json:"created_at"`
	Origin               string    `json:"origin"`
	RepairCount          int       `json:"repair_count"`
}
type DocumentHead struct {
	Generation          uint64 `json:"generation"`
	WorkingRevisionID   string `json:"working_revision_id"`
	FinalizedRevisionID string `json:"finalized_revision_id,omitempty"`
	FinalizationID      string `json:"finalization_id,omitempty"`
	PendingProjection   bool   `json:"pending_projection"`
}
type DocumentIssue struct {
	ID                string `json:"id"`
	Code              string `json:"code"`
	Severity          string `json:"severity"`
	Group             string `json:"group"`
	Section           string `json:"section,omitempty"`
	Line              int    `json:"line,omitempty"`
	Evidence          string `json:"evidence,omitempty"`
	Basis             string `json:"basis"`
	Suggestion        string `json:"suggestion"`
	HumanConfirmation bool   `json:"human_confirmation"`
}
type DocumentReviewRun struct {
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	Model         string `json:"model,omitempty"`
	PromptVersion string `json:"prompt_version,omitempty"`
	Message       string `json:"message,omitempty"`
}
type ValidationReport struct {
	ReviewMode       string              `json:"review_mode,omitempty"`
	OperationID      string              `json:"operation_id,omitempty"`
	ReportID         string              `json:"report_id"`
	DocumentID       string              `json:"document_id"`
	RevisionID       string              `json:"revision_id"`
	ValidationKey    string              `json:"validation_key"`
	ValidatorVersion string              `json:"validator_version"`
	Status           string              `json:"status"`
	Checks           []string            `json:"checks"`
	Issues           []DocumentIssue     `json:"issues"`
	ReviewRuns       []DocumentReviewRun `json:"review_runs"`
	CreatedAt        time.Time           `json:"created_at"`
}
type DocumentConfirmation struct {
	IssueID string `json:"issue_id"`
	Reason  string `json:"reason"`
}
type FinalizationRecord struct {
	PreviousFinalizationID string                 `json:"previous_finalization_id,omitempty"`
	FinalizationID         string                 `json:"finalization_id"`
	OperationID            string                 `json:"operation_id"`
	RequestHash            string                 `json:"request_hash"`
	RevisionID             string                 `json:"revision_id"`
	ReportID               string                 `json:"report_id"`
	ValidationKey          string                 `json:"validation_key"`
	ConfirmedIssues        []DocumentConfirmation `json:"confirmed_issues"`
	FinalizedAt            time.Time              `json:"finalized_at"`
	DeliveryLevel          string                 `json:"delivery_level"`
	ReviewMethod           string                 `json:"review_method"`
}
type DocumentSnapshot struct {
	DocumentID      string              `json:"document_id"`
	Head            DocumentHead        `json:"head"`
	Revision        DocumentRevision    `json:"revision"`
	Contract        DocumentContract    `json:"contract"`
	Sources         SourceManifest      `json:"source_manifest"`
	Body            string              `json:"body"`
	Report          *ValidationReport   `json:"report,omitempty"`
	Freshness       string              `json:"freshness"`
	Finalization    *FinalizationRecord `json:"finalization,omitempty"`
	SourceFreshness string              `json:"source_freshness"`
}
