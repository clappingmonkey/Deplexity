package api

// These types represent the raw API response shapes from Perplexity's internal API.
// Reverse-engineered from live API responses (May 2026, API version 2.18).

// --- Thread List: GET /rest/thread/list_recent ---

// ThreadListResponse is the shape of GET /rest/thread/list_recent.
type ThreadListResponse []ThreadListItem

// ThreadListItem is a thread summary in the list_recent response.
type ThreadListItem struct {
	UUID            string  `json:"uuid"`
	Title           string  `json:"title"`
	Link            string  `json:"link"`
	Variant         string  `json:"variant"`
	Unread          bool    `json:"unread"`
	Status          string  `json:"status"`
	ContextUUID     string  `json:"context_uuid"`
	TaskDescription *string `json:"task_description"`
	AnswerPreview   *string `json:"answer_preview"`
	ModeType        int     `json:"mode_type"`
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
