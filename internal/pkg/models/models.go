package models

import "time"

const (
	ThoughtTypeText = "text"
	ThoughtTypeURL  = "url"

	ThoughtSourceManual  = "manual"
	ThoughtSourceAPI     = "api"
	ThoughtSourceCompose = "compose"

	ComposeSourceTypeThought        = "thought"
	ComposeSourceTypeSearchResult   = "search_result"
	ComposeSourceTypeTopicSection   = "topic_section"
	ComposeSourceTypeCaptureSession = "capture_session"

	ComposeFormatSummary = "summary"
	ComposeFormatOutline = "outline"
	ComposeFormatReport  = "report"

	ComposeStatusDraft = "draft"
	ComposeStatusSaved = "saved"

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
	JobStatusRetrying  = "retrying"
	JobStatusSucceeded = "succeeded"
	JobStatusFailed    = "failed"
	JobStatusCanceled  = "canceled"

	JobTypeGitCommit  = "git_commit"
	JobTypeRefine     = "refine"
	JobTypeIndex      = "index"
	JobTypeReindex    = "reindex"
	JobTypeTopicMatch = "topic_match"
	JobTypeTopicWeave = "topic_weave"
	JobTypeExpand     = "expand"

	ResourceTypeThought   = "thought"
	ResourceTypeWorkspace = "workspace"
	ResourceTypeTopic     = "topic"
	ResourceTypeSession   = "scratchpad_session"

	EventThoughtCaptured          = "thought.captured"
	EventThoughtRefineStarted     = "thought.refine_started"
	EventThoughtRefined           = "thought.refined"
	EventThoughtRefineFailed      = "thought.refine_failed"
	EventSearchIndexUpdated       = "search.index_updated"
	EventSearchIndexFailed        = "search.index_failed"
	EventSearchReindexStarted     = "search.reindex_started"
	EventSearchReindexFinished    = "search.reindex_finished"
	EventTopicCreated             = "topic.created"
	EventTopicMatched             = "topic.matched"
	EventTopicUpdated             = "topic.updated"
	EventTopicRefreshStarted      = "topic.refresh_started"
	EventTopicRefreshFailed       = "topic.refresh_failed"
	EventScratchpadContextUpdated = "scratchpad.context_updated"
	EventScratchpadCommitted      = "scratchpad.committed"
	EventGitCommitRequested       = "git.commit_requested"
	EventGitCommitSucceeded       = "git.commit_succeeded"
	EventGitCommitFailed          = "git.commit_failed"
	EventJobUpdated               = "job.updated"
	EventThoughtPatched           = "thought.patched"
	EventThoughtExpanded          = "thought.expanded"
)

type Workspace struct {
	ID              string    `json:"id"`
	RootPath        string    `json:"root_path"`
	ThoughtsPath    string    `json:"thoughts_path"`
	TopicsPath      string    `json:"topics_path"`
	AttachmentsPath string    `json:"attachments_path"`
	RuntimePath     string    `json:"runtime_path"`
	JobsPath        string    `json:"jobs_path"`
	ScratchpadPath  string    `json:"scratchpad_path"`
	GitEnabled      bool      `json:"git_enabled"`
	CreatedAt       time.Time `json:"created_at"`
}

type Thought struct {
	ID                string        `json:"id"`
	Type              string        `json:"type"`
	Source            string        `json:"source"`
	UserTitle         string        `json:"user_title,omitempty"`
	ExtractedTitle    string        `json:"extracted_title,omitempty"`
	DisplayTitle      string        `json:"display_title,omitempty"`
	URL               string        `json:"url,omitempty"`
	Path              string        `json:"path"`
	CreatedAt         time.Time     `json:"created_at"`
	UpdatedAt         time.Time     `json:"updated_at"`
	ContentHash       string        `json:"content_hash"`
	UserTags          []string      `json:"user_tags,omitempty"`
	AITags            []string      `json:"ai_tags,omitempty"`
	TopicIDs          []string      `json:"topic_ids,omitempty"`
	Summary           string        `json:"summary,omitempty"`
	KeyPoints         []string      `json:"key_points,omitempty"`
	Errors            []ErrorRef    `json:"errors,omitempty"`
	CaptureStatus     string        `json:"capture_status"`
	RefineStatus      string        `json:"refine_status"`
	IndexStatus       string        `json:"index_status"`
	TopicStatus       string        `json:"topic_status"`
	RelatedThoughtIDs []string      `json:"related_thought_ids,omitempty" yaml:"related_thought_ids,omitempty"`
	SuggestedTopicIDs []string      `json:"suggested_topic_ids,omitempty" yaml:"suggested_topic_ids,omitempty"`
	URLFollowups      []URLFollowup `json:"url_followups,omitempty" yaml:"url_followups,omitempty"`
	ExpansionPlan     string        `json:"expansion_plan,omitempty" yaml:"expansion_plan,omitempty"`
}

// URLFollowup is one related URL harvested from the main URL of a
// URL-type thought during post-refine expansion. The snippet is a short
// lead-in the fetcher pulled alongside the link; it can be empty when
// the source page did not provide a useful preview.
type URLFollowup struct {
	URL     string `json:"url" yaml:"url"`
	Title   string `json:"title" yaml:"title"`
	Snippet string `json:"snippet,omitempty" yaml:"snippet,omitempty"`
}

// TopicMatchSuggestion is one near-miss topic returned by
// TopicService.NearMissTopics. Score is the same cosine/keyword score
// that a hard match would have used, but below the topic's
// Rules.Semantic.Threshold; the UI surfaces it as "possibly related".
type TopicMatchSuggestion struct {
	TopicID   string  `json:"topic_id" yaml:"topic_id"`
	TopicName string  `json:"topic_name" yaml:"topic_name"`
	Score     float64 `json:"score" yaml:"score"`
}

type ThoughtContent struct {
	Original         string `json:"original"`
	ExtractedContent string `json:"extracted_content,omitempty"`
	AINotes          string `json:"ai_notes,omitempty"`
	Links            string `json:"links,omitempty"`
}

type ThoughtSnapshot struct {
	Thought    Thought           `json:"thought"`
	Content    ThoughtContent    `json:"content,omitempty"`
	Jobs       []Job             `json:"jobs,omitempty"`
	GitCommits []GitCommitRecord `json:"git_commits,omitempty"`
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

// ThoughtPatchRequest is the body of PATCH /api/thoughts/:id. Pointer
// fields distinguish "field absent" (leave the existing value alone) from
// "field present with empty value" (clear the value). PatchThought
// rejects unknown fields with a 400; see service.PatchThought.
type ThoughtPatchRequest struct {
	Title         *string   `json:"title,omitempty"`
	Tags          *[]string `json:"tags,omitempty"`
	AINotesAppend *string   `json:"ai_notes_append,omitempty"`
	TopicIDs      *[]string `json:"topic_ids,omitempty"`
}

type ThoughtSuggestion struct {
	ThoughtID string   `json:"thought_id"`
	Title     string   `json:"title"`
	Tags      []string `json:"tags"`
	Model     string   `json:"model"`
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

type GitChangeSet struct {
	ID            string    `json:"id"`
	Paths         []string  `json:"paths"`
	Reason        string    `json:"reason"`
	ResourceIDs   []string  `json:"resource_ids"`
	CreatedAt     time.Time `json:"created_at"`
	DebounceUntil time.Time `json:"debounce_until"`
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
	LLM        LLMRuntimeStatus        `json:"llm"`
	Embedding  EmbeddingRuntimeStatus  `json:"embedding"`
	Reader     ReaderRuntimeStatus     `json:"reader"`
	Git        GitRuntimeStatus        `json:"git"`
	Background BackgroundRuntimeStatus `json:"background"`
	Events     EventsRuntimeStatus     `json:"events"`
}

// ReaderRuntimeStatus is the runtime view of the third-party web
// reader integration. UI uses it (and the PrivacyReport sibling) to
// decide whether to render the "外部请求" hint next to URL-type
// capture buttons. Configured distinguishes "API key set" from
// "Enabled, falling back to the Jina public endpoint".
type ReaderRuntimeStatus struct {
	Status     string `json:"status"`
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`
	BaseURL    string `json:"base_url"`
}

// PrivacyReport is the lightweight view served by
// GET /api/system/privacy. Each ExternalSurface tells the UI
// whether the corresponding action will trigger an external HTTP
// call, what provider it would hit, and a human-readable hint the
// UI can render next to the action button. The hint text is built
// on the server so the i18n / wording lives in one place rather
// than being duplicated in every UI surface.
type PrivacyReport struct {
	GeneratedAt time.Time        `json:"generated_at"`
	LLM         ExternalSurface  `json:"llm"`
	Embedding   ExternalSurface  `json:"embedding"`
	Reader      ExternalSurface  `json:"reader"`
	Actions     []ExternalAction `json:"actions"`
}

type ExternalSurface struct {
	Kind       string `json:"kind"`
	Configured bool   `json:"configured"`
	Enabled    bool   `json:"enabled"`
	Provider   string `json:"provider"`
	BaseURL    string `json:"base_url"`
	Hint       string `json:"hint"`
}

// ExternalAction pairs an API action with the surfaces it triggers.
// The UI fetches this list once and uses it to badge the matching
// buttons. The Action field is the HTTP method + path; the
// Surfaces field is the list of Kind values the action will hit.
type ExternalAction struct {
	Action   string   `json:"action"`
	Method   string   `json:"method"`
	Path     string   `json:"path"`
	Surfaces []string `json:"surfaces"`
	Hint     string   `json:"hint"`
}

type SystemMetrics struct {
	GeneratedAt           time.Time             `json:"generated_at"`
	Values                map[string]float64    `json:"values"`
	RefineDurationSeconds DurationMetric        `json:"refine_duration_seconds"`
	BackgroundJobs        BackgroundJobsMetric  `json:"background_jobs"`
	ThoughtIndexLag       ThoughtIndexLagMetric `json:"thought_index_lag"`
}

type DurationMetric struct {
	Count   int     `json:"count"`
	Sum     float64 `json:"sum"`
	Average float64 `json:"average"`
	Latest  float64 `json:"latest"`
}

type BackgroundJobsMetric struct {
	Total    int            `json:"total"`
	ByStatus map[string]int `json:"by_status"`
	ByType   map[string]int `json:"by_type"`
}

type ThoughtIndexLagMetric struct {
	Seconds         float64 `json:"seconds"`
	PendingThoughts int     `json:"pending_thoughts"`
	FailedThoughts  int     `json:"failed_thoughts"`
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
	ScratchpadPath  string `json:"scratchpad_path"`
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

type LLMRuntimeStatus struct {
	Status     string `json:"status"`
	Configured bool   `json:"configured"`
	BaseURL    string `json:"base_url"`
	ChatModel  string `json:"chat_model"`
}

type EmbeddingRuntimeStatus struct {
	Status     string `json:"status"`
	Configured bool   `json:"configured"`
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
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
	Status         string `json:"status"`
	JobsPath       string `json:"jobs_path"`
	Writable       bool   `json:"writable"`
	AcceptingTasks bool   `json:"accepting_tasks"`
	Error          string `json:"error,omitempty"`
}

type EventsRuntimeStatus struct {
	Status      string `json:"status"`
	Publishable bool   `json:"publishable"`
	HistorySize int    `json:"history_size"`
	Limit       int    `json:"limit"`
	Subscribers int    `json:"subscribers"`
}

type SearchQuery struct {
	Query              string        `json:"q"`
	Mode               string        `json:"mode"`
	Sort               string        `json:"sort,omitempty"`
	TopicID            string        `json:"topic_id,omitempty"`
	Tags               []string      `json:"tags,omitempty"`
	From               time.Time     `json:"from,omitempty"`
	To                 time.Time     `json:"to,omitempty"`
	Page               int           `json:"page"`
	PageSize           int           `json:"page_size"`
	Limit              int           `json:"limit,omitempty"`
	IncludeCandidates  bool          `json:"include_candidates,omitempty"`
	Explain            bool          `json:"explain,omitempty"`
	Weights            SearchWeights `json:"weights,omitempty"`
	QueryVector        []float64     `json:"-"`
	EmbeddingModel     string        `json:"-"`
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

// SearchResultView is the Web-facing search response. Internal score
// components (KeywordScore/SemanticScore/RecencyScore) and the
// DuckDB debug fields are intentionally omitted: the single
// SearchResultSummary.Score is the only numeric signal exposed to
// the UI. SearchResultView.Candidates is only populated when the
// caller asked for it via SearchQuery.IncludeCandidates; the field
// stays out of the JSON when no candidates were produced so clients
// do not have to special-case empty slices.
type SearchResultView struct {
	Results    []SearchResultSummary  `json:"results"`
	Candidates []SearchResultCandidate `json:"candidates,omitempty"`
	Page       int                    `json:"page"`
	PageSize   int                    `json:"page_size"`
	Total      int                    `json:"total"`
}

// SearchResultSummary is the per-thought projection embedded in
// SearchResultView. Path is repo-relative (no absolute filesystem
// location) so the Web never accidentally renders a host-local
// path that the user has no way to dereference.
type SearchResultSummary struct {
	ThoughtID string   `json:"thought_id"`
	Title     string   `json:"title"`
	Snippet   string   `json:"snippet"`
	Score     float64  `json:"score"`
	Path      string   `json:"path"`
	Topics    []string `json:"topics,omitempty"`
	Tags      []string `json:"tags,omitempty"`
}

// SearchResultCandidate is a topic-level candidate surfaced
// alongside the per-thought results when SearchQuery.IncludeCandidates
// is set. MatchType mirrors the values used by TopicMembership
// (tag_hint | keyword | semantic).
type SearchResultCandidate struct {
	TopicID      string  `json:"topic_id"`
	TopicName    string  `json:"topic_name"`
	Slug         string  `json:"slug"`
	MatchType    string  `json:"match_type"`
	Score        float64 `json:"score"`
	MatchedCount int     `json:"matched_count"`
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
	ProposalID string `json:"proposal_id,omitempty"`
	ThoughtID  string `json:"thought_id"`
	Document   string `json:"document"`
}

type TopicWeaveProposal struct {
	ID               string                  `json:"id" yaml:"id"`
	TopicID          string                  `json:"topic_id"`
	ThoughtID        string                  `json:"thought_id"`
	Status           string                  `json:"status" yaml:"status"`
	SourceLink       string                  `json:"source_link"`
	Membership       TopicMembership         `json:"membership"`
	BaseDocument     string                  `json:"base_document"`
	ProposedDocument string                  `json:"proposed_document"`
	AcceptedDocument string                  `json:"accepted_document,omitempty" yaml:"accepted_document,omitempty"`
	Diff             []TopicDocumentDiffLine `json:"diff"`
	Patch            TopicDocumentPatch      `json:"patch" yaml:"patch"`
	CreatedAt        time.Time               `json:"created_at"`
	UpdatedAt        time.Time               `json:"updated_at" yaml:"updated_at"`
	AcceptedAt       *time.Time              `json:"accepted_at,omitempty" yaml:"accepted_at,omitempty"`
}

type TopicDocumentDiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

type TopicDocumentPatch struct {
	BaseHash     string                   `json:"base_hash" yaml:"base_hash"`
	ProposedHash string                   `json:"proposed_hash" yaml:"proposed_hash"`
	Hunks        []TopicDocumentPatchHunk `json:"hunks" yaml:"hunks"`
}

type TopicDocumentPatchHunk struct {
	BaseStart     int                     `json:"base_start" yaml:"base_start"`
	BaseCount     int                     `json:"base_count" yaml:"base_count"`
	ProposedStart int                     `json:"proposed_start" yaml:"proposed_start"`
	ProposedCount int                     `json:"proposed_count" yaml:"proposed_count"`
	Lines         []TopicDocumentDiffLine `json:"lines" yaml:"lines"`
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
	Topic             Topic                   `json:"topic"`
	Document          string                  `json:"document"`
	Members           []TopicMembership       `json:"members"`
	SessionCandidates []TopicSessionCandidate `json:"session_candidates,omitempty"`
	Activities        []DomainEvent           `json:"activities,omitempty"`
}

// TopicSessionCandidate is a read-only projection of an unarchived
// scratchpad session that matched a topic via the LLM-maintained
// SessionContext.SuggestedTopicIDs tag hint, the keyword rules, or
// a semantic cosine score. Candidates are NEVER written to the
// topic's main document; the user must explicitly archive the
// scratchpad (strategy=new) before the candidate becomes a formal
// TopicMembership. MatchType is one of "tag_hint" | "keyword" |
// "semantic"; Status is "candidate" | "near_miss" | "conflict".
type TopicSessionCandidate struct {
	SessionID string    `json:"session_id" yaml:"session_id"`
	TopicID   string    `json:"topic_id" yaml:"topic_id"`
	Title     string    `json:"title" yaml:"title"`
	MatchType string    `json:"match_type" yaml:"match_type"`
	Score     float64   `json:"score" yaml:"score"`
	Reasons   []string  `json:"reasons" yaml:"reasons"`
	Status    string    `json:"status" yaml:"status"`
	UpdatedAt time.Time `json:"updated_at" yaml:"updated_at"`
}

// TopicCandidateImpactSource is the enumerated origin of a
// TopicCandidateImpact. The values are the Web-facing wire names and
// also drive the routing inside topic biz: capture_session and
// thought_reopen_session both come from the same scratchpad candidate
// file, thought impacts come from a topic's membership list, and
// compose_draft impacts come from the compose module's draft index.
type TopicCandidateImpactSource string

const (
	TopicCandidateSourceCaptureSession     TopicCandidateImpactSource = "capture_session"
	TopicCandidateSourceThoughtReopen      TopicCandidateImpactSource = "thought_reopen_session"
	TopicCandidateSourceThought            TopicCandidateImpactSource = "thought"
	TopicCandidateSourceComposeDraft       TopicCandidateImpactSource = "compose_draft"
)

// TopicCandidateImpact is the Web-facing list shape for
// GET /api/topics/{id}/candidates. Each entry is one piece of
// in-flight state that may shift the topic's membership without
// having been confirmed yet. Source is the discriminator and
// decides which of CandidateID / SessionID / ThoughtID / DraftID
// is populated for the entry.
type TopicCandidateImpact struct {
	Source      TopicCandidateImpactSource `json:"source"`
	CandidateID string                     `json:"candidate_id"`
	SessionID   string                     `json:"session_id,omitempty"`
	ThoughtID   string                     `json:"thought_id,omitempty"`
	DraftID     string                     `json:"draft_id,omitempty"`
	Title       string                     `json:"title"`
	MatchType   string                     `json:"match_type,omitempty"`
	Score       float64                    `json:"score"`
	Status      string                     `json:"status,omitempty"`
	Reasons     []string                   `json:"reasons,omitempty"`
	UpdatedAt   time.Time                  `json:"updated_at"`
}

// SynthesisDraft is the LLM-side wire shape returned by
// ai.SynthesisProvider. The HTTP-facing type is ComposeDraft; the
// compose service translates SynthesisDraft into ComposeDraft on the
// way in. We keep the type here (rather than folding into ComposeDraft)
// so the LLM provider boundary can evolve without breaking the
// application/persistence DTO.
type SynthesisDraft struct {
	ID          string                 `json:"id" yaml:"id"`
	ThoughtIDs  []string               `json:"thought_ids" yaml:"thought_ids"`
	Goal        string                 `json:"goal" yaml:"goal"`
	Format      string                 `json:"format" yaml:"format"`
	Content     string                 `json:"content" yaml:"content"`
	SourceLinks []string               `json:"source_links" yaml:"source_links"`
	Model       string                 `json:"model" yaml:"model"`
	Status      string                 `json:"status" yaml:"status"`
	History     []SynthesisDraftHistory `json:"history,omitempty" yaml:"history,omitempty"`
	CreatedAt   time.Time              `json:"created_at" yaml:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at" yaml:"updated_at"`
}

type SynthesisDraftHistory struct {
	Status    string    `json:"status" yaml:"status"`
	Message   string    `json:"message,omitempty" yaml:"message,omitempty"`
	ThoughtID string    `json:"thought_id,omitempty" yaml:"thought_id,omitempty"`
	At        time.Time `json:"at" yaml:"at"`
}

// ComposeSource represents a single source entry inside a
// ComposeBasket or a ComposeDraft. The source_type/source_id pair is
// the stable join key for the Web basket helper; the auxiliary
// fields (title/snippet/source_link/metadata) are display-only and
// may be rehydrated server-side when a draft is opened.
type ComposeSource struct {
	SourceType string            `json:"source_type" yaml:"source_type"`
	SourceID   string            `json:"source_id" yaml:"source_id"`
	Title      string            `json:"title,omitempty" yaml:"title,omitempty"`
	Snippet    string            `json:"snippet,omitempty" yaml:"snippet,omitempty"`
	SourceLink string            `json:"source_link,omitempty" yaml:"source_link,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// ComposeRequest is the body of POST /api/compose/drafts. It
// supersedes the legacy SynthesisRequest (thought_ids[]) shape —
// sources carry their own type discriminator so the server can
// hydrate Search/Topic/Capture rows in addition to Thought rows.
type ComposeRequest struct {
	Sources            []ComposeSource `json:"sources"`
	SelectedThoughtIDs []string        `json:"selected_thought_ids,omitempty"`
	Prompt             string          `json:"prompt,omitempty"`
	Goal               string          `json:"goal,omitempty"`
	Format             string          `json:"format,omitempty"`
}

// ComposeSaveRequest is the body of POST /api/compose/drafts/{id}/save.
// The draft_id path parameter identifies the stored draft; the body
// optionally overrides the rendered content and the title/tags used
// when the resulting Thought is captured.
type ComposeSaveRequest struct {
	Content string   `json:"content,omitempty"`
	Title   string   `json:"title,omitempty"`
	Tags    []string `json:"tags,omitempty"`
}

// ComposeDraft is the on-disk and over-the-wire shape of a compose
// draft. The YAML-backed file lives at
// workspace/compose/drafts/{draft_id}.yaml. The status transitions
// draft → saved when the user commits the draft to a Thought.
type ComposeDraft struct {
	ID             string                `json:"id" yaml:"id"`
	Sources        []ComposeSource       `json:"sources" yaml:"sources"`
	Goal           string                `json:"goal" yaml:"goal"`
	Format         string                `json:"format" yaml:"format"`
	Content        string                `json:"content" yaml:"content"`
	SourceLinks    []string              `json:"source_links" yaml:"source_links"`
	Model          string                `json:"model" yaml:"model"`
	Status         string                `json:"status" yaml:"status"`
	SavedThoughtID string                `json:"saved_thought_id,omitempty" yaml:"saved_thought_id,omitempty"`
	History        []ComposeDraftHistory `json:"history,omitempty" yaml:"history,omitempty"`
	CreatedAt      time.Time             `json:"created_at" yaml:"created_at"`
	UpdatedAt      time.Time             `json:"updated_at" yaml:"updated_at"`
	SavedAt        *time.Time            `json:"saved_at,omitempty" yaml:"saved_at,omitempty"`
}

type ComposeDraftHistory struct {
	Status    string    `json:"status" yaml:"status"`
	Message   string    `json:"message,omitempty" yaml:"message,omitempty"`
	ThoughtID string    `json:"thought_id,omitempty" yaml:"thought_id,omitempty"`
	At        time.Time `json:"at" yaml:"at"`
}

// ComposeSaveResult mirrors SynthesisSaveResult: it bundles the
// created Thought with the new thought's job and the source links
// the Web drawer uses to render the "Saved to thought-X" bubble.
type ComposeSaveResult struct {
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
