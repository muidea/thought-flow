package models

import "time"

const (
	ThoughtTypeText = "text"
	ThoughtTypeURL  = "url"

	ThoughtSourceManual    = "manual"
	ThoughtSourceAPI       = "api"
	ThoughtSourceSynthesis = "synthesis"

	CaptureStatusCaptured        = "captured"
	CaptureStatusDuplicateWarned = "duplicate_warned"
	CaptureStatusFailed          = "capture_failed"

	RefineStatusPending  = "pending"
	RefineStatusRunning  = "running"
	RefineStatusRefined  = "refined"
	RefineStatusFailed   = "failed"
	RefineStatusDisabled = "disabled"

	IndexStatusPending   = "pending"
	IndexStatusIndexed   = "indexed"
	IndexStatusFailed    = "failed"
	TopicStatusUnmatched = "unmatched"
	TopicStatusMatched   = "matched"
	TopicStatusUpdated   = "updated"
	TopicStatusFailed    = "failed"

	JobStatusQueued    = "queued"
	JobStatusRunning   = "running"
	JobStatusSucceeded = "succeeded"
	JobStatusFailed    = "failed"

	JobTypeGitCommit  = "git_commit"
	JobTypeRefine     = "refine"
	JobTypeIndex      = "index"
	JobTypeReindex    = "reindex"
	JobTypeTopicMatch = "topic_match"
	JobTypeTopicWeave = "topic_weave"

	ResourceTypeThought   = "thought"
	ResourceTypeWorkspace = "workspace"
	ResourceTypeTopic     = "topic"

	EventThoughtCaptured       = "thought.captured"
	EventThoughtRefineStarted  = "thought.refine_started"
	EventThoughtRefined        = "thought.refined"
	EventThoughtRefineFailed   = "thought.refine_failed"
	EventSearchIndexUpdated    = "search.index_updated"
	EventSearchIndexFailed     = "search.index_failed"
	EventSearchReindexStarted  = "search.reindex_started"
	EventSearchReindexFinished = "search.reindex_finished"
	EventTopicCreated          = "topic.created"
	EventTopicMatched          = "topic.matched"
	EventTopicUpdated          = "topic.updated"
	EventTopicRebuildStarted   = "topic.rebuild_started"
	EventTopicRebuildFailed    = "topic.rebuild_failed"
	EventGitCommitRequested    = "git.commit_requested"
	EventGitCommitSucceeded    = "git.commit_succeeded"
	EventGitCommitFailed       = "git.commit_failed"
	EventJobUpdated            = "job.updated"
)

type Workspace struct {
	ID              string    `json:"id"`
	RootPath        string    `json:"root_path"`
	ThoughtsPath    string    `json:"thoughts_path"`
	TopicsPath      string    `json:"topics_path"`
	AttachmentsPath string    `json:"attachments_path"`
	RuntimePath     string    `json:"runtime_path"`
	JobsPath        string    `json:"jobs_path"`
	GitEnabled      bool      `json:"git_enabled"`
	CreatedAt       time.Time `json:"created_at"`
}

type Thought struct {
	ID             string     `json:"id"`
	Type           string     `json:"type"`
	Source         string     `json:"source"`
	UserTitle      string     `json:"user_title,omitempty"`
	ExtractedTitle string     `json:"extracted_title,omitempty"`
	DisplayTitle   string     `json:"display_title,omitempty"`
	URL            string     `json:"url,omitempty"`
	Path           string     `json:"path"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	ContentHash    string     `json:"content_hash"`
	UserTags       []string   `json:"user_tags,omitempty"`
	AITags         []string   `json:"ai_tags,omitempty"`
	TopicIDs       []string   `json:"topic_ids,omitempty"`
	Summary        string     `json:"summary,omitempty"`
	KeyPoints      []string   `json:"key_points,omitempty"`
	Errors         []ErrorRef `json:"errors,omitempty"`
	CaptureStatus  string     `json:"capture_status"`
	RefineStatus   string     `json:"refine_status"`
	IndexStatus    string     `json:"index_status"`
	TopicStatus    string     `json:"topic_status"`
}

type ThoughtContent struct {
	Original         string `json:"original"`
	ExtractedContent string `json:"extracted_content,omitempty"`
	AINotes          string `json:"ai_notes,omitempty"`
	Links            string `json:"links,omitempty"`
}

type ThoughtSnapshot struct {
	Thought Thought        `json:"thought"`
	Content ThoughtContent `json:"content,omitempty"`
}

type ThoughtRefinement struct {
	ThoughtID            string           `json:"thought_id"`
	Status               string           `json:"status"`
	ExtractedTitle       string           `json:"extracted_title,omitempty"`
	ExtractedContentHash string           `json:"extracted_content_hash,omitempty"`
	Summary              string           `json:"summary,omitempty"`
	KeyPoints            []string         `json:"key_points,omitempty"`
	AITags               []string         `json:"ai_tags,omitempty"`
	Model                string           `json:"model,omitempty"`
	InputHash            string           `json:"input_hash,omitempty"`
	GeneratedAt          time.Time        `json:"generated_at"`
	Error                *ErrorRef        `json:"error,omitempty"`
	Embedding            *EmbeddingRecord `json:"embedding,omitempty"`
}

type EmbeddingRecord struct {
	ThoughtID   string    `json:"thought_id"`
	Model       string    `json:"model"`
	Dimension   int       `json:"dimension"`
	Vector      []float64 `json:"vector"`
	ContentHash string    `json:"content_hash"`
	CreatedAt   time.Time `json:"created_at"`
}

type CaptureCommand struct {
	Type       string   `json:"type"`
	Content    string   `json:"content"`
	URL        string   `json:"url"`
	Title      string   `json:"title"`
	Tags       []string `json:"tags"`
	TopicHints []string `json:"topic_hints"`
	Source     string   `json:"source"`
}

type CaptureResult struct {
	Thought Thought `json:"thought"`
	Jobs    []Job   `json:"jobs"`
}

type Job struct {
	ID           string     `json:"id"`
	Type         string     `json:"type"`
	ResourceType string     `json:"resource_type"`
	ResourceID   string     `json:"resource_id"`
	Status       string     `json:"status"`
	Attempt      int        `json:"attempt"`
	MaxAttempts  int        `json:"max_attempts"`
	Progress     float64    `json:"progress"`
	Message      string     `json:"message,omitempty"`
	Error        *ErrorRef  `json:"error,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

type ErrorRef struct {
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
	OccurredAt time.Time      `json:"occurred_at"`
	Retryable  bool           `json:"retryable"`
}

type DomainEvent struct {
	EventID        string    `json:"event_id"`
	EventType      string    `json:"event_type"`
	SourceUnit     string    `json:"source_unit"`
	OccurredAt     time.Time `json:"occurred_at"`
	TraceID        string    `json:"trace_id,omitempty"`
	WorkspaceID    string    `json:"workspace_id"`
	ResourceType   string    `json:"resource_type"`
	ResourceID     string    `json:"resource_id"`
	PayloadVersion int       `json:"payload_version"`
	Payload        any       `json:"payload,omitempty"`
}

type GitCommitRequestedPayload struct {
	Paths       []string `json:"paths"`
	Reason      string   `json:"reason"`
	ResourceIDs []string `json:"resource_ids"`
}

type GitCommitRecord struct {
	CommitHash  string    `json:"commit_hash,omitempty"`
	Message     string    `json:"message"`
	Paths       []string  `json:"paths"`
	ResourceIDs []string  `json:"resource_ids"`
	CommittedAt time.Time `json:"committed_at"`
	Error       *ErrorRef `json:"error,omitempty"`
	JobID       string    `json:"job_id,omitempty"`
}

type SystemStatus struct {
	Status     string                  `json:"status"`
	Ready      bool                    `json:"ready"`
	Workspace  WorkspaceRuntimeStatus  `json:"workspace"`
	DuckDB     DuckDBRuntimeStatus     `json:"duckdb"`
	AI         AIRuntimeStatus         `json:"ai"`
	Git        GitRuntimeStatus        `json:"git"`
	Background BackgroundRuntimeStatus `json:"background"`
	Events     EventsRuntimeStatus     `json:"events"`
}

type WorkspaceRuntimeStatus struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	RootPath        string `json:"root_path"`
	ThoughtsPath    string `json:"thoughts_path"`
	TopicsPath      string `json:"topics_path"`
	AttachmentsPath string `json:"attachments_path"`
	RuntimePath     string `json:"runtime_path"`
	JobsPath        string `json:"jobs_path"`
	GitEnabled      bool   `json:"git_enabled"`
	Writable        bool   `json:"writable"`
	Error           string `json:"error,omitempty"`
}

type DuckDBRuntimeStatus struct {
	Status string `json:"status"`
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Error  string `json:"error,omitempty"`
}

type AIRuntimeStatus struct {
	Status         string `json:"status"`
	Configured     bool   `json:"configured"`
	BaseURL        string `json:"base_url"`
	ChatModel      string `json:"chat_model"`
	EmbeddingModel string `json:"embedding_model"`
}

type GitRuntimeStatus struct {
	Status             string `json:"status"`
	Enabled            bool   `json:"enabled"`
	Repository         bool   `json:"repository"`
	IdentityConfigured bool   `json:"identity_configured"`
	Dirty              bool   `json:"dirty"`
	Error              string `json:"error,omitempty"`
}

type BackgroundRuntimeStatus struct {
	Status   string `json:"status"`
	JobsPath string `json:"jobs_path"`
	Writable bool   `json:"writable"`
	Error    string `json:"error,omitempty"`
}

type EventsRuntimeStatus struct {
	Status      string `json:"status"`
	HistorySize int    `json:"history_size"`
	Limit       int    `json:"limit"`
	Subscribers int    `json:"subscribers"`
}

type SearchQuery struct {
	Query          string        `json:"q"`
	Mode           string        `json:"mode"`
	Sort           string        `json:"sort,omitempty"`
	TopicID        string        `json:"topic_id,omitempty"`
	Tags           []string      `json:"tags,omitempty"`
	Page           int           `json:"page"`
	PageSize       int           `json:"page_size"`
	Explain        bool          `json:"explain,omitempty"`
	Weights        SearchWeights `json:"weights,omitempty"`
	QueryVector    []float64     `json:"-"`
	EmbeddingModel string        `json:"-"`
}

type SearchWeights struct {
	Keyword  float64 `json:"keyword"`
	Semantic float64 `json:"semantic"`
	Recency  float64 `json:"recency"`
}

type SearchExplain struct {
	Mode           string        `json:"mode"`
	Sort           string        `json:"sort"`
	ScoreFormula   string        `json:"score_formula"`
	Weights        SearchWeights `json:"weights"`
	Components     SearchWeights `json:"components"`
	KeywordSource  string        `json:"keyword_source,omitempty"`
	SemanticSource string        `json:"semantic_source,omitempty"`
}

type SearchResult struct {
	ThoughtID     string         `json:"thought_id"`
	Title         string         `json:"title"`
	Snippet       string         `json:"snippet"`
	Score         float64        `json:"score"`
	KeywordScore  float64        `json:"keyword_score"`
	SemanticScore float64        `json:"semantic_score"`
	RecencyScore  float64        `json:"recency_score"`
	Path          string         `json:"path"`
	Topics        []string       `json:"topics,omitempty"`
	Tags          []string       `json:"tags,omitempty"`
	Explain       *SearchExplain `json:"explain,omitempty"`
}

type SearchResponse struct {
	Items    []SearchResult `json:"items"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	Total    int            `json:"total"`
}

type Topic struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Slug         string        `json:"slug"`
	Description  string        `json:"description,omitempty"`
	Rules        TopicRule     `json:"rules"`
	Outline      []OutlineNode `json:"outline,omitempty"`
	AutoWeave    bool          `json:"auto_weave"`
	MemberCount  int           `json:"member_count"`
	WordCount    int           `json:"word_count"`
	LastActiveAt *time.Time    `json:"last_active_at,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	Members      []string      `json:"members,omitempty"`
}

type TopicRule struct {
	Keywords      KeywordRule  `json:"keywords"`
	Tags          TagRule      `json:"tags"`
	Semantic      SemanticRule `json:"semantic"`
	ManualInclude []string     `json:"manual_include,omitempty"`
	ManualExclude []string     `json:"manual_exclude,omitempty"`
}

type KeywordRule struct {
	Any     []string `json:"any,omitempty"`
	All     []string `json:"all,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

type TagRule struct {
	Any []string `json:"any,omitempty"`
}

type SemanticRule struct {
	Enabled   bool    `json:"enabled"`
	Threshold float64 `json:"threshold"`
}

type OutlineNode struct {
	Title string `json:"title"`
}

type TopicMembership struct {
	TopicID   string    `json:"topic_id" yaml:"topic_id"`
	ThoughtID string    `json:"thought_id" yaml:"thought_id"`
	MatchType string    `json:"match_type" yaml:"match_type"`
	Score     float64   `json:"score" yaml:"score"`
	Reasons   []string  `json:"reasons" yaml:"reasons"`
	Status    string    `json:"status" yaml:"status"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt time.Time `json:"updated_at" yaml:"updated_at"`
}

type TopicWeaveRequest struct {
	Topic           Topic           `json:"topic"`
	CurrentDocument string          `json:"current_document"`
	Thought         Thought         `json:"thought"`
	Content         ThoughtContent  `json:"content"`
	Membership      TopicMembership `json:"membership"`
	SourceLink      string          `json:"source_link"`
}

type TopicWeaveResult struct {
	Document string `json:"document"`
	Model    string `json:"model"`
	Strategy string `json:"strategy"`
}

type TopicWeavePreviewRequest struct {
	ThoughtID string `json:"thought_id"`
}

type TopicWeaveAcceptRequest struct {
	ThoughtID string `json:"thought_id"`
	Document  string `json:"document"`
}

type TopicWeaveProposal struct {
	TopicID          string                  `json:"topic_id"`
	ThoughtID        string                  `json:"thought_id"`
	SourceLink       string                  `json:"source_link"`
	Membership       TopicMembership         `json:"membership"`
	BaseDocument     string                  `json:"base_document"`
	ProposedDocument string                  `json:"proposed_document"`
	Diff             []TopicDocumentDiffLine `json:"diff"`
	CreatedAt        time.Time               `json:"created_at"`
}

type TopicDocumentDiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

type TopicCreateRequest struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Rules       TopicRule     `json:"rules"`
	Outline     []OutlineNode `json:"outline"`
	AutoWeave   *bool         `json:"auto_weave,omitempty"`
}

type TopicUpdateRequest struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Rules       TopicRule     `json:"rules"`
	Outline     []OutlineNode `json:"outline"`
	AutoWeave   *bool         `json:"auto_weave,omitempty"`
}

type TopicDetail struct {
	Topic      Topic             `json:"topic"`
	Document   string            `json:"document"`
	Members    []TopicMembership `json:"members"`
	Activities []DomainEvent     `json:"activities,omitempty"`
}

type SynthesisRequest struct {
	ThoughtIDs []string `json:"thought_ids"`
	Goal       string   `json:"goal"`
	Format     string   `json:"format"`
}

type SynthesisSaveRequest struct {
	DraftID     string   `json:"draft_id,omitempty"`
	ThoughtIDs  []string `json:"thought_ids"`
	Goal        string   `json:"goal"`
	Format      string   `json:"format"`
	Title       string   `json:"title,omitempty"`
	Content     string   `json:"content"`
	SourceLinks []string `json:"source_links,omitempty"`
}

type SynthesisDraft struct {
	ID          string    `json:"id"`
	ThoughtIDs  []string  `json:"thought_ids"`
	Goal        string    `json:"goal"`
	Format      string    `json:"format"`
	Content     string    `json:"content"`
	SourceLinks []string  `json:"source_links"`
	Model       string    `json:"model"`
	CreatedAt   time.Time `json:"created_at"`
}

type SynthesisSaveResult struct {
	Thought     Thought  `json:"thought"`
	Jobs        []Job    `json:"jobs,omitempty"`
	SourceLinks []string `json:"source_links,omitempty"`
}

type APIResponse struct {
	RequestID string `json:"request_id"`
	Data      any    `json:"data"`
	Error     any    `json:"error"`
}

func NewErrorRef(code, message string, retryable bool) ErrorRef {
	return ErrorRef{
		Code:       code,
		Message:    message,
		OccurredAt: time.Now().UTC(),
		Retryable:  retryable,
	}
}
