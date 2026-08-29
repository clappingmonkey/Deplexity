package api

import "encoding/json"

// These types represent the raw API response shapes from Perplexity's internal API.
// Reverse-engineered from live API responses (May 2026, API version 2.18).

// --- Thread List: POST /rest/thread/list_ask_threads ---

// ThreadListResponse is the shape of POST /rest/thread/list_ask_threads.
type ThreadListResponse []ThreadListItem

// ThreadListItem is a thread summary in the list_ask_threads response.
type ThreadListItem struct {
	UUID             string              `json:"uuid"`
	Title            string              `json:"title"`
	Slug             string              `json:"slug"`
	ThreadNumber     int                 `json:"thread_number"`
	Mode             string              `json:"mode"`
	Status           string              `json:"status"`
	ContextUUID      string              `json:"context_uuid"`
	DisplayModel     string              `json:"display_model"`
	SearchFocus      string              `json:"search_focus"`
	QueryStr         string              `json:"query_str"`
	AnswerPreview    string              `json:"answer_preview"`
	LastQueryTime    string              `json:"last_query_datetime"`
	HasNextPage      bool                `json:"has_next_page"`
	TotalThreads     int                 `json:"total_threads"`
	Collection       *ThreadCollection   `json:"collection,omitempty"`
	Source           string              `json:"source"`
	QueryCount       int                 `json:"query_count"`
}

// ThreadCollection is the inline collection/space info returned with each thread.
type ThreadCollection struct {
	UUID  string `json:"uuid"`
	Title string `json:"title"`
	Emoji string `json:"emoji"`
	Slug  string `json:"slug"`
}

// ThreadListRequest is the POST body for list_ask_threads.
type ThreadListRequest struct {
	Limit         int    `json:"limit"`
	Ascending     bool   `json:"ascending"`
	Offset        int    `json:"offset"`
	SearchTerm    string `json:"search_term"`
	ExcludeASI    bool   `json:"exclude_asi"`
	IncludeAssets bool   `json:"include_assets"`
}

// --- Spaces: GET /rest/spaces ---

// SpacesResponse is the shape of GET /rest/spaces.
type SpacesResponse struct {
	InvitedSpaces      []SpaceItem `json:"invited_spaces"`
	PrivateSpaces      []SpaceItem `json:"private_spaces"`
	SharedSpaces       []SpaceItem `json:"shared_spaces"`
	SavedSpaces        []SpaceItem `json:"saved_spaces"`
	OrganizationSpaces []SpaceItem `json:"organization_spaces"`
}

// SpaceItem is a space/collection in the spaces response.
type SpaceItem struct {
	UUID        string `json:"uuid"`
	Title       string `json:"title"`
	Slug        string `json:"slug"`
	Emoji       string `json:"emoji"`
	Updated     string `json:"updated"`
	HasNextPage bool   `json:"has_next_page"`
}

// --- Space Detail: GET /rest/collections/get_collection?collection_slug={slug} ---
//
// The spaces list endpoint omits per-space configuration (instructions,
// description, suggested queries, primers). This detail endpoint carries them.
// Note: the query param must be collection_slug — collection_uuid returns 422.

// CollectionDetailResponse is the shape of GET /rest/collections/get_collection.
type CollectionDetailResponse struct {
	UUID                            string           `json:"uuid"`
	Title                           string           `json:"title"`
	Slug                            string           `json:"slug"`
	Emoji                           string           `json:"emoji"`
	URL                             string           `json:"url"`
	Description                     string           `json:"description"`
	Instructions                    string           `json:"instructions"`
	KnowledgeDreamInstructions      string           `json:"knowledge_dream_instructions"`
	ProjectStatusSummaryInstruction string           `json:"project_status_summary_instruction"`
	SuggestedQueries                []SuggestedQuery `json:"suggested_queries"`
	Primers                         []Primer         `json:"primers"`
	FocusedWebConfig                json.RawMessage  `json:"focused_web_config"`
	MemoryMode                      string           `json:"memory_mode"`
	Access                          int              `json:"access"`
	UserPermission                  int              `json:"user_permission"`
	ThreadCount                     int              `json:"thread_count"`
	PageCount                       int              `json:"page_count"`
	FileCount                       int              `json:"file_count"`
	UpdatedDatetime                 string           `json:"updated_datetime"`
}

// SuggestedQuery is a single suggested query on a space.
type SuggestedQuery struct {
	Query string `json:"query"`
}

// Primer is a primer entry on a space (grouped queries by type).
type Primer struct {
	PrimerType string   `json:"primer_type"`
	Queries    []string `json:"queries"`
}

// --- Skills ---
//
// Space-attached skills are fetched via /rest/skills/selectable filtered by
// collection_uuid. Space-specific skills carry scope=="collection"; global
// skills carry scope=="global". Full skill detail (including a pre-signed S3
// file_url for the SKILL.md body) comes from /rest/skills/{id}.

// SkillsSelectableResponse is the shape of GET /rest/skills/selectable.
type SkillsSelectableResponse struct {
	Skills     []SkillSummary `json:"skills"`
	NextCursor *string        `json:"next_cursor"`
}

// SkillSummary is a skill entry in the selectable list.
type SkillSummary struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Description        string `json:"description"`
	DisplayName        string `json:"display_name"`
	DisplayDescription string `json:"display_description"`
	Scope              string `json:"scope"`
}

// SkillDetailResponse is the shape of GET /rest/skills/{id}.
type SkillDetailResponse struct {
	IsOwner   bool             `json:"is_owner"`
	IsCreator bool             `json:"is_creator"`
	Enabled   bool             `json:"enabled"`
	Installed bool             `json:"installed"`
	Skill     SkillDetailInner `json:"skill"`
}

// SkillDetailInner is the nested skill object in the skill detail response.
type SkillDetailInner struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Scope       string            `json:"scope"`
	FileURL     string            `json:"file_url"`
	Categories  []string          `json:"categories"`
	Tags        map[string]string `json:"tags"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

// --- Thread Detail: GET /rest/thread/{uuid} ---

// ThreadDetailResponse is the shape of GET /rest/thread/{uuid}.
type ThreadDetailResponse struct {
	Entries          []ThreadEntry   `json:"entries"`
	BackgroundEntries []interface{}  `json:"background_entries"`
	HasNextPage      bool            `json:"has_next_page"`
	NextCursor       *string         `json:"next_cursor"`
	Status           string          `json:"status"`
	ThreadMetadata   ThreadMetadata  `json:"thread_metadata"`
}

// ThreadMetadata contains thread-level metadata.
type ThreadMetadata struct {
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Mode      string `json:"mode"`
	Status    string `json:"thread_status"`
}

// ThreadEntry represents a single Q&A entry within a thread.
type ThreadEntry struct {
	UUID            string  `json:"uuid"`
	BackendUUID     string  `json:"backend_uuid"`
	ContextUUID     string  `json:"context_uuid"`
	QueryStr        string  `json:"query_str"`
	DisplayModel    string  `json:"display_model"`
	SearchFocus     string  `json:"search_focus"`
	Status          string  `json:"status"`
	Mode            string  `json:"mode"`
	Personalized    bool    `json:"personalized"`
	ThreadTitle     string  `json:"thread_title"`
	ThreadURLSlug   string  `json:"thread_url_slug"`
	BookmarkState   string  `json:"bookmark_state"`
	Blocks          []Block `json:"blocks"`
	RelatedQueries  []string `json:"related_queries"`
	CreatedAt       string  `json:"entry_created_datetime"`
	UpdatedAt       string  `json:"entry_updated_datetime"`
}

// Block represents a content block within an entry.
type Block struct {
	IntendedUsage    string            `json:"intended_usage"`
	MarkdownBlock    *MarkdownBlock    `json:"markdown_block,omitempty"`
	WebResultBlock   *WebResultBlock   `json:"web_result_block,omitempty"`
	PlanBlock        *PlanBlock        `json:"plan_block,omitempty"`
	WorkflowBlock    *WorkflowBlock    `json:"workflow_block,omitempty"`
}

// MarkdownBlock contains the answer text in markdown format.
type MarkdownBlock struct {
	Progress string `json:"progress"`
	Answer   string `json:"answer"`
}

// WebResultBlock contains web search results/sources.
type WebResultBlock struct {
	Progress   string      `json:"progress"`
	WebResults []WebResult `json:"web_results"`
}

// WebResult is a single source/citation.
type WebResult struct {
	Name         string        `json:"name"`
	URL          string        `json:"url"`
	Snippet      string        `json:"snippet"`
	Timestamp    string        `json:"timestamp,omitempty"`
	MetaData     WebResultMeta `json:"meta_data"`
	IsAttachment bool          `json:"is_attachment"`
}

// WebResultMeta contains metadata for a web result.
type WebResultMeta struct {
	Client           string   `json:"client"`
	CitationDomain   *string  `json:"citation_domain_name"`
	DomainName       string   `json:"domain_name,omitempty"`
	Description      string   `json:"description,omitempty"`
	PublishedDate    string   `json:"published_date,omitempty"`
	Images           []string `json:"images,omitempty"`
}

// PlanBlock contains the search plan/steps.
type PlanBlock struct {
	Progress string     `json:"progress"`
	Goals    []PlanGoal `json:"goals"`
}

// PlanGoal is a single step in the plan.
type PlanGoal struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Final       bool   `json:"final"`
}

// WorkflowBlock contains workflow/step information.
type WorkflowBlock struct {
	Version string         `json:"version"`
	Status  string         `json:"status"`
	Steps   []WorkflowStep `json:"steps"`
}

// WorkflowStep is a step in the workflow.
type WorkflowStep struct {
	Status string         `json:"status"`
	Title  string         `json:"title"`
	Items  []WorkflowItem `json:"items"`
}

// WorkflowItem is an item within a workflow step.
type WorkflowItem struct {
	ID      string      `json:"id"`
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

// --- User: GET /api/user ---

// UserResponse is the shape of GET /api/user.
type UserResponse struct {
	ID                 string `json:"id"`
	Email              string `json:"email"`
	Username           string `json:"username,omitempty"`
	PaymentTier        string `json:"payment_tier"`
	SubscriptionStatus string `json:"subscription_status"`
	SubscriptionSource string `json:"subscription_source"`
	IsInOrganization   bool   `json:"is_in_organization"`
	Created            bool   `json:"created"`
}

// --- Session: GET /api/auth/session ---

// SessionResponse is the shape of GET /api/auth/session.
type SessionResponse struct {
	Expires                 string       `json:"expires"`
	PreventUsernameRedirect bool         `json:"preventUsernameRedirect"`
	User                    SessionUser  `json:"user"`
}

// SessionUser is the user info within the session response.
type SessionUser struct {
	ID                 string `json:"id"`
	Email              string `json:"email"`
	Username           string `json:"username"`
	OrgRole            string `json:"org_role"`
	OrgUUID            string `json:"org_uuid"`
	SubscriptionStatus string `json:"subscription_status"`
	SubscriptionSource string `json:"subscription_source"`
}
