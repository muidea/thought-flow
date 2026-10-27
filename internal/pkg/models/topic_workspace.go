package models

import "time"

const (
	TopicModeAggregate  = "aggregate"
	TopicModeCollection = "collection"
)

type TopicSearchPolicy struct {
	TopicGlobalVisible         bool `json:"topic_global_visible" yaml:"topic_global_visible"`
	OwnedThoughtsGlobalDefault bool `json:"owned_thoughts_global_default" yaml:"owned_thoughts_global_default"`
}

type ObjectPlacement struct {
	ThoughtPath  string `json:"thought_path"`
	DocumentRoot string `json:"document_root"`
	EvidenceRoot string `json:"evidence_root,omitempty"`
}

type ObjectLocation struct {
	ManagedSetID        string          `json:"managed_set_id,omitempty"`
	ManagedMemberKey    string          `json:"managed_member_key,omitempty"`
	ImportOperationID   string          `json:"import_operation_id,omitempty"`
	SchemaVersion       int             `json:"schema_version"`
	ObjectID            string          `json:"object_id"`
	ObjectKind          string          `json:"object_kind"`
	Generation          uint64          `json:"generation"`
	OwnerTopicID        string          `json:"owner_topic_id,omitempty"`
	GlobalSearch        string          `json:"global_search"`
	Lifecycle           string          `json:"lifecycle"`
	Placement           ObjectPlacement `json:"placement"`
	ResourceBindingsRef *FileDigest     `json:"resource_bindings_ref,omitempty"`
	LastOperationID     string          `json:"last_operation_id"`
	DeletedAt           *time.Time      `json:"deleted_at,omitempty"`
}

type FileDigest struct {
	Path    string `json:"path"`
	Hash    string `json:"hash"`
	Size    int64  `json:"size"`
	Private bool   `json:"private,omitempty"`
}

type CatalogBinding struct {
	Kind           string `json:"kind"`
	ThoughtID      string `json:"thought_id,omitempty"`
	SnapshotID     string `json:"snapshot_id,omitempty"`
	VersionPolicy  string `json:"version_policy,omitempty"`
	RevisionID     string `json:"revision_id,omitempty"`
	FinalizationID string `json:"finalization_id,omitempty"`
}

type CatalogGroup struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
type CatalogEntry struct {
	ID       string         `json:"id"`
	GroupID  string         `json:"group_id"`
	Title    string         `json:"title"`
	Role     string         `json:"role"`
	Required bool           `json:"required"`
	Binding  CatalogBinding `json:"binding"`
}
type TopicCatalog struct {
	SchemaVersion         int            `json:"schema_version"`
	TopicID               string         `json:"topic_id"`
	Generation            uint64         `json:"generation"`
	RequiredDeliveryLevel string         `json:"required_delivery_level"`
	Groups                []CatalogGroup `json:"groups"`
	Entries               []CatalogEntry `json:"entries"`
	LastOperationID       string         `json:"last_operation_id"`
}
type CatalogWriteRequest struct {
	OperationID           string         `json:"operation_id"`
	ExpectedGeneration    uint64         `json:"expected_generation"`
	RequiredDeliveryLevel string         `json:"required_delivery_level"`
	Groups                []CatalogGroup `json:"groups"`
	Entries               []CatalogEntry `json:"entries"`
}
type CatalogEntryView struct {
	Entry                  CatalogEntry        `json:"entry"`
	BindingStatus          string              `json:"binding_status"`
	DeliveryStatus         string              `json:"delivery_status"`
	HasNewDraft            bool                `json:"has_new_draft"`
	WorkStatus             string              `json:"work_status"`
	VersionStatus          string              `json:"version_status"`
	OriginalTitle          string              `json:"original_title,omitempty"`
	Profile                *DocumentProfileRef `json:"profile,omitempty"`
	WorkingRevisionID      string              `json:"working_revision_id,omitempty"`
	SelectedRevisionID     string              `json:"selected_revision_id,omitempty"`
	SelectedFinalizationID string              `json:"selected_finalization_id,omitempty"`
	ValidationStatus       string              `json:"validation_status"`
	Availability           string              `json:"availability"`
	UpdatedAt              time.Time           `json:"updated_at,omitempty"`
	Location               *ObjectLocation     `json:"location,omitempty"`
	Error                  string              `json:"error,omitempty"`
}
type CatalogView struct {
	WorkspaceKey     string             `json:"workspace_key"`
	Catalog          TopicCatalog       `json:"catalog"`
	Topic            Topic              `json:"topic"`
	Entries          []CatalogEntryView `json:"entries"`
	Deleted          []ObjectLocation   `json:"deleted"`
	Uncatalogued     []ObjectLocation   `json:"uncatalogued"`
	RequiredCount    int                `json:"required_count"`
	DeliveredCount   int                `json:"delivered_count"`
	ProjectionStatus string             `json:"projection_status"`
	ProjectionHash   string             `json:"projection_hash"`
}
type TopicSettingsRequest struct {
	OperationID        string            `json:"operation_id"`
	ExpectedGeneration uint64            `json:"expected_generation"`
	Name               string            `json:"name"`
	Description        string            `json:"description"`
	SearchPolicy       TopicSearchPolicy `json:"search_policy"`
}
type SearchPolicyRequest struct {
	OperationID        string `json:"operation_id"`
	ExpectedGeneration uint64 `json:"expected_generation"`
	GlobalSearch       string `json:"global_search"`
}
type WorkspaceOperation struct {
	ID               string    `json:"id"`
	Kind             string    `json:"kind"`
	RequestHash      string    `json:"request_hash"`
	ObjectID         string    `json:"object_id,omitempty"`
	TopicID          string    `json:"topic_id,omitempty"`
	State            string    `json:"state"`
	BeforeGeneration uint64    `json:"before_generation"`
	AfterGeneration  uint64    `json:"after_generation"`
	TargetPath       string    `json:"target_path,omitempty"`
	CandidateHash    string    `json:"candidate_hash,omitempty"`
	EventType        string    `json:"event_type,omitempty"`
	Actor            string    `json:"actor"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Error            string    `json:"error,omitempty"`
}
type MigrationPlan struct {
	ID               string         `json:"id"`
	ObjectID         string         `json:"object_id"`
	TargetTopicID    string         `json:"target_topic_id"`
	Source           ObjectLocation `json:"source"`
	Target           ObjectLocation `json:"target"`
	Files            []FileDigest   `json:"files"`
	AvailableBytes   uint64         `json:"available_bytes"`
	TotalSize        int64          `json:"total_size"`
	BeforeGlobal     bool           `json:"before_global"`
	AfterGlobal      bool           `json:"after_global"`
	PlanHash         string         `json:"plan_hash"`
	CreatedAt        time.Time      `json:"created_at"`
	Conflicts        []string       `json:"conflicts"`
	Missing          []string       `json:"missing"`
	ResourceHandling string         `json:"resource_handling"`
}
type MigrationRequest struct {
	OperationID              string `json:"operation_id"`
	PreviewID                string `json:"preview_id"`
	ExpectedObjectGeneration uint64 `json:"expected_object_generation"`
	PlanHash                 string `json:"plan_hash"`
}
type TopicResource struct {
	ID        string   `json:"id"`
	TopicID   string   `json:"topic_id"`
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	MIME      string   `json:"mime"`
	Hash      string   `json:"hash"`
	Size      int64    `json:"size"`
	Shareable bool     `json:"shareable"`
	ObjectIDs []string `json:"object_ids"`
}
type ResourceBinding struct {
	ResourceID      string `json:"resource_id"`
	Hash            string `json:"hash"`
	Path            string `json:"path"`
	Private         bool   `json:"private"`
	OriginalBase    string `json:"original_base,omitempty"`
	OriginalLocator string `json:"original_locator,omitempty"`
}
type ResourceBindings struct {
	ObjectID string            `json:"object_id"`
	Bindings []ResourceBinding `json:"bindings"`
}
type OfflineResourceIssue struct {
	EntryID string `json:"entry_id,omitempty"`
	Locator string `json:"locator"`
	Reason  string `json:"reason"`
}
type TopicSnapshot struct {
	ResourceIssues  []OfflineResourceIssue `json:"resource_issues,omitempty"`
	DeliveryLevel   string                 `json:"delivery_level"`
	ID              string                 `json:"id"`
	SourceThoughtID string                 `json:"source_thought_id"`
	RevisionID      string                 `json:"revision_id"`
	FinalizationID  string                 `json:"finalization_id"`
	BodyHash        string                 `json:"body_hash"`
	Complete        string                 `json:"complete"`
	Private         bool                   `json:"private"`
	Resources       []ResourceBinding      `json:"resources"`
	CreatedAt       time.Time              `json:"created_at"`
}
