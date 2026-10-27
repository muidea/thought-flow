// Package documentset defines the frozen, provider-independent document set contract.
package documentset

import (
	"errors"
	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/models"
	"time"
)

var (
	ErrCaptureMissing = errors.New("capture session unavailable")
	ErrSourceMissing  = errors.New("document set source unavailable")
	ErrInvalid        = errors.New("invalid document set contract")
	ErrConflict       = errors.New("document set generation or operation conflict")
	ErrPrecondition   = errors.New("document set precondition required")
	ErrRules          = errors.New("document set validation blocked")
	ErrBudget         = errors.New("document set budget exceeded")
	ErrUnavailable    = errors.New("document set generation unavailable")
)

const SchemaVersion = 1
const ValidatorVersion = "document-set-v1"

type Limits struct {
	MaxMembers      int `json:"max_members" yaml:"max_members"`
	MaxRepairRounds int `json:"max_repair_rounds" yaml:"max_repair_rounds"`
	MaxParallel     int `json:"max_parallel_generations" yaml:"max_parallel_generations"`
	MaxCalls        int `json:"max_calls" yaml:"max_calls"`
	MaxInputTokens  int `json:"max_input_tokens" yaml:"max_input_tokens"`
	MaxOutputTokens int `json:"max_output_tokens" yaml:"max_output_tokens"`
	MaxContextBytes int `json:"max_context_bytes" yaml:"max_context_bytes"`
	MaxInputBytes   int `json:"max_input_bytes" yaml:"max_input_bytes"`
	MaxOutputBytes  int `json:"max_output_bytes" yaml:"max_output_bytes"`
	WallSeconds     int `json:"wall_seconds" yaml:"wall_seconds"`
}
type Role struct {
	Key             string                    `json:"key" yaml:"key"`
	Cardinality     string                    `json:"cardinality" yaml:"cardinality"`
	Required        *bool                     `json:"required" yaml:"required"`
	When            string                    `json:"when,omitempty" yaml:"when,omitempty"`
	ProfileRef      models.DocumentProfileRef `json:"profile_ref" yaml:"profile_ref"`
	ProfileSnapshot string                    `json:"profile_snapshot" yaml:"profile_snapshot"`
	DependsOn       []string                  `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
}
type Profile struct {
	SchemaVersion int                            `json:"schema_version" yaml:"schema_version"`
	Kind          string                         `json:"kind" yaml:"kind"`
	ID            string                         `json:"profile_id" yaml:"profile_id"`
	Version       int                            `json:"version" yaml:"version"`
	Hash          string                         `json:"content_hash" yaml:"content_hash"`
	Name          string                         `json:"name" yaml:"name"`
	Enabled       bool                           `json:"enabled" yaml:"enabled"`
	Inputs        []documentprofile.ProfileInput `json:"inputs" yaml:"inputs"`
	Roles         []Role                         `json:"roles" yaml:"roles"`
	Checks        []string                       `json:"set_checks" yaml:"set_checks"`
	Limits        Limits                         `json:"limits" yaml:"limits"`
}
type Source struct {
	Message int    `json:"message"`
	Quote   string `json:"quote"`
}
type Fact struct {
	ID      string  `json:"id"`
	Kind    string  `json:"kind"` // requirement, term, module, interface, entity, decision, constraint, verification
	Name    string  `json:"name"`
	Content string  `json:"content"`
	Status  string  `json:"status"` // confirmed, proposed, unresolved
	Source  Source  `json:"source"`
	Hash    string  `json:"hash"`
	Reason  string  `json:"reason"`
	Owner   string  `json:"owner,omitempty"` // stable member key; sole structured definition
	Fields  []Field `json:"fields,omitempty"`
}
type Field struct {
	ID             string   `json:"id"`
	Type           string   `json:"type"`
	Nullable       bool     `json:"nullable"`
	Classification string   `json:"classification"` // persisted, computed, external
	EntityID       string   `json:"entity_id,omitempty"`
	EntityField    string   `json:"entity_field,omitempty"`
	Permission     string   `json:"permission,omitempty"`
	Errors         []string `json:"errors,omitempty"`
}
type Relation struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	From string `json:"from"`
	To   string `json:"to"`
}
type Question struct {
	ID       string   `json:"id"`
	Message  string   `json:"message"`
	Blocking bool     `json:"blocking"`
	Members  []string `json:"members,omitempty"`
}
type Condition struct {
	Value  *bool  `json:"value"`
	Status string `json:"status"`
	Source Source `json:"source"`
	Reason string `json:"reason"`
}
type ScopeChange struct {
	FactID   string   `json:"fact_id"`
	Action   string   `json:"action"`
	Status   string   `json:"status"`
	Source   Source   `json:"source"`
	Reason   string   `json:"reason"`
	Affected []string `json:"affected_members"`
	Coverage string   `json:"replacement_coverage"`
}
type SharedDesign struct {
	ScopeChanges []ScopeChange        `json:"scope_changes,omitempty"`
	RevisionID   string               `json:"revision_id"`
	Hash         string               `json:"hash"`
	Facts        []Fact               `json:"facts"`
	Relations    []Relation           `json:"relations"`
	Questions    []Question           `json:"open_questions"`
	Conditions   map[string]Condition `json:"conditions"`
	Inputs       map[string]string    `json:"inputs"`
}
type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}
type CaptureSnapshot struct {
	SessionID      string                `json:"session_id"`
	Generation     uint64                `json:"generation"`
	Messages       []Message             `json:"messages"`
	MessageCount   int                   `json:"message_count"`
	ConfirmedFacts []string              `json:"confirmed_facts"`
	Sources        models.SourceManifest `json:"sources"`
	ScopeHash      string                `json:"scope_hash"`
	ContextHash    string                `json:"context_hash"`
}
type PlanMember struct {
	Key             string                    `json:"key"`
	Role            string                    `json:"role"`
	Title           string                    `json:"title"`
	Filename        string                    `json:"filename"`
	DocumentID      string                    `json:"document_id"`
	Required        bool                      `json:"required"`
	Covers          []string                  `json:"covers"`
	Excludes        []string                  `json:"excludes"`
	DependsOn       []string                  `json:"depends_on"`
	ProfileRef      models.DocumentProfileRef `json:"profile_ref"`
	ProfileSnapshot string                    `json:"profile_snapshot"`
}
type Plan struct {
	ID                string            `json:"plan_id"`
	PlanningCall      *Call             `json:"planning_call,omitempty"`
	Hash              string            `json:"plan_hash"`
	SetID             string            `json:"set_id"`
	Profile           Profile           `json:"profile"`
	Capture           CaptureSnapshot   `json:"capture"`
	Shared            SharedDesign      `json:"shared_design"`
	Members           []PlanMember      `json:"members"`
	Omissions         map[string]string `json:"omissions"`
	BaseGeneration    uint64            `json:"base_generation"`
	CatalogGeneration uint64            `json:"catalog_generation"`
	LocationBaselines map[string]string `json:"location_baselines"`
	Baselines         map[string]string `json:"baselines"`
	CreatedAt         time.Time         `json:"created_at"`
}
type Artifact struct {
	Key             string                  `json:"member_key"`
	DocumentID      string                  `json:"document_id"`
	RevisionID      string                  `json:"revision_id"`
	BodyHash        string                  `json:"body_hash"`
	Body            string                  `json:"body"`
	Contract        models.DocumentContract `json:"contract"`
	ProfileSnapshot string                  `json:"profile_snapshot"`
	Sources         models.SourceManifest   `json:"sources"`
	ResourceHash    string                  `json:"resource_bindings_hash"`
	Resources       models.ResourceBindings `json:"resource_bindings"`
	Dependencies    map[string]string       `json:"dependencies"`
	SingleReport    models.ValidationReport `json:"single_report"`
	Original        string                  `json:"original,omitempty"`
	OriginalHash    string                  `json:"original_hash,omitempty"`
	ReusedFrom      string                  `json:"reused_from,omitempty"`
}
type Location struct {
	Member   string `json:"member_key"`
	Section  string `json:"section"`
	Evidence string `json:"evidence"`
}
type Issue struct {
	ID          string     `json:"id"`
	Code        string     `json:"code"`
	Severity    string     `json:"severity"`
	Hard        bool       `json:"hard"`
	Confirmable bool       `json:"confirmable"`
	Message     string     `json:"message"`
	Locations   []Location `json:"locations"`
}
type ReviewItem struct {
	ID        string   `json:"id"`
	Members   []string `json:"members"`
	Facts     []string `json:"facts"`
	Relations []string `json:"relations"`
}
type ReviewEvidence struct {
	CheckID   string     `json:"check_id"`
	Verdict   string     `json:"verdict"` // passed, risk, blocked
	Locations []Location `json:"locations"`
	Reason    string     `json:"reason"`
}
type ReviewResponse struct {
	Evidence []ReviewEvidence `json:"evidence"`
	Issues   []Issue          `json:"issues"`
}
type ReviewDecision struct {
	CheckID   string     `json:"check_id"`
	IssueID   string     `json:"issue_id,omitempty"`
	Reason    string     `json:"reason"`
	Locations []Location `json:"locations"`
}
type Report struct {
	CandidateHash    string           `json:"candidate_manifest_hash"`
	Hash             string           `json:"hash"`
	ValidatorVersion string           `json:"validator_version"`
	StructuralStatus string           `json:"structural_status"`
	SemanticStatus   string           `json:"semantic_status"`
	Status           string           `json:"status"`
	Checklist        []ReviewItem     `json:"checklist"`
	Evidence         []ReviewEvidence `json:"evidence"`
	Issues           []Issue          `json:"issues"`
	ReviewedBy       string           `json:"reviewed_by,omitempty"`
	ReviewDecisions  []ReviewDecision `json:"review_decisions,omitempty"`
}
type Revision struct {
	SchemaVersion int        `json:"schema_version"`
	ID            string     `json:"set_revision_id"`
	Hash          string     `json:"set_revision_hash"`
	Parent        string     `json:"parent_revision_id,omitempty"`
	OperationID   string     `json:"operation_id"`
	Plan          Plan       `json:"plan"`
	Members       []Artifact `json:"members"`
	Report        Report     `json:"validation"`
	CreatedAt     time.Time  `json:"created_at"`
}
type Finalization struct {
	ID            string    `json:"id"`
	RevisionID    string    `json:"set_revision_id"`
	RevisionHash  string    `json:"set_revision_hash"`
	ReportHash    string    `json:"report_hash"`
	DeliveryLevel string    `json:"delivery_level"`
	ApprovedBy    string    `json:"approved_by"`
	At            time.Time `json:"at"`
	OperationID   string    `json:"operation_id"`
}
type Head struct {
	SchemaVersion    int            `json:"schema_version"`
	Generation       uint64         `json:"generation"`
	Working          string         `json:"working_set_revision"`
	ProjectedWorking string         `json:"projected_working_revision,omitempty"`
	Finalized        string         `json:"finalized_set_revision"`
	Finalizations    []Finalization `json:"finalization_history,omitempty"`
	Finalization     *Finalization  `json:"finalization,omitempty"`
	OperationID      string         `json:"operation_id"`
}
type MemberProgress struct {
	Key   string `json:"member_key"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}
type Call struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Member          string `json:"member_key,omitempty"`
	State           string `json:"state"` // running, completed, failed, uncertain
	InputTokens     int    `json:"input_tokens_upper_bound"`
	OutputTokens    int    `json:"output_tokens_upper_bound"`
	TokensEstimated bool   `json:"tokens_estimated"`
	InputBytes      int    `json:"input_bytes"`
	OutputBytes     int    `json:"output_bytes"`
	Error           string `json:"error,omitempty"`
}
type Run struct {
	StateVersion      uint64             `json:"state_version"`
	ID                string             `json:"run_id"`
	OperationID       string             `json:"operation_id"`
	RequestHash       string             `json:"request_hash"`
	Plan              Plan               `json:"plan"`
	State             string             `json:"state"`
	Members           []MemberProgress   `json:"members"`
	Calls             []Call             `json:"calls"`
	Candidate         *Revision          `json:"candidate,omitempty"`
	Error             string             `json:"error,omitempty"`
	ModelHash         string             `json:"model_hash"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
	RepairRounds      int                `json:"repair_rounds"`
	RepairIssues      map[string][]Issue `json:"repair_issues,omitempty"`
	Reconciliations   []Reconciliation   `json:"reconciliations,omitempty"`
	MarkdownPreviewID string             `json:"markdown_preview_id,omitempty"`
	CancelRequested   bool               `json:"cancel_requested"`
}

// Reconciliation retains all three sides privately; publication cannot discard a manual edit.
type Reconciliation struct {
	Member    string `json:"member_key"`
	Base      string `json:"base"`
	Generated string `json:"generated"`
	Manual    string `json:"manual"`
	Merged    string `json:"merged,omitempty"`
	Conflict  bool   `json:"conflict"`
}
