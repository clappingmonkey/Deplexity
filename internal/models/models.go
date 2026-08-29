package models

import (
	"encoding/json"
	"time"
)

// User represents a Perplexity user profile.
type User struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	ImageURL     string `json:"image_url,omitempty"`
	Subscription string `json:"subscription,omitempty"`
}

// Thread represents a conversation thread.
type Thread struct {
	UUID       string    `json:"uuid"`
	Slug       string    `json:"slug"`
	Title      string    `json:"title"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	SpaceUUID  string    `json:"space_uuid,omitempty"`
	Entries    []Entry   `json:"entries,omitempty"`
	Bookmarked bool      `json:"bookmarked,omitempty"`
	Complete   bool      `json:"complete"`
	// NextCursor is the pagination cursor to resume from when Complete is
	// false. The entries already fetched are persisted alongside it.
	NextCursor string `json:"next_cursor,omitempty"`
}

// Entry represents a single query-response pair within a thread.
type Entry struct {
	UUID        string    `json:"uuid"`
	Query       string    `json:"query"`
	Answer      string    `json:"answer"`
	Sources     []Source  `json:"sources,omitempty"`
	Model       string    `json:"model,omitempty"`
	SearchFocus string    `json:"search_focus,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Source represents a citation/reference from an answer.
type Source struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
	Favicon string `json:"favicon,omitempty"`
}

// Space represents a Perplexity Space (collection).
//
// Core identity fields (Name, UpdatedAt) keep their existing JSON keys for
// backwards compatibility. Fields added for issue #61 use the raw API names.
type Space struct {
	UUID         string     `json:"uuid"`
	Name         string     `json:"name"`
	Slug         string     `json:"slug,omitempty"`
	Emoji        string     `json:"emoji,omitempty"`
	Description  string     `json:"description,omitempty"`
	Instructions string     `json:"instructions,omitempty"`
	CreatedAt    *time.Time `json:"created_at,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ThreadUUIDs  []string   `json:"thread_uuids,omitempty"`

	// Enrichment fields from GET /rest/collections/get_collection (issue #61).
	URL                             string          `json:"url,omitempty"`
	KnowledgeDreamInstructions      string          `json:"knowledge_dream_instructions,omitempty"`
	ProjectStatusSummaryInstruction string          `json:"project_status_summary_instruction,omitempty"`
	SuggestedQueries                []string        `json:"suggested_queries,omitempty"`
	Primers                         []Primer        `json:"primers,omitempty"`
	FocusedWebConfig                json.RawMessage `json:"focused_web_config,omitempty"`
	MemoryMode                      string          `json:"memory_mode,omitempty"`
	Access                          int             `json:"access,omitempty"`
	UserPermission                  int             `json:"user_permission,omitempty"`
	ThreadCount                     int             `json:"thread_count,omitempty"`
	PageCount                       int             `json:"page_count,omitempty"`
	FileCount                       int             `json:"file_count,omitempty"`

	// Skills attached to this space (scope == "collection").
	Skills []Skill `json:"skills,omitempty"`
}

// Primer is a primer entry on a space (grouped queries by type).
type Primer struct {
	PrimerType string   `json:"primer_type"`
	Queries    []string `json:"queries"`
}

// Skill represents a skill attached to a space. The SKILL.md body is fetched
// during enrichment (the source URL is a short-lived pre-signed link) and
// written to a sidecar file by the exporters; it is not embedded in JSON.
type Skill struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Scope       string            `json:"scope,omitempty"`
	Categories  []string          `json:"categories,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	CreatedAt   string            `json:"created_at,omitempty"`
	UpdatedAt   string            `json:"updated_at,omitempty"`
	// BodyFile is the relative path to the written SKILL.md (e.g. "skills/git-commit.md").
	BodyFile string `json:"body_file,omitempty"`
	// Body holds the fetched SKILL.md content. Not serialized to JSON; written
	// to a sidecar file by the exporters.
	Body string `json:"-"`
}

// ExportManifest holds metadata about an export run.
type ExportManifest struct {
	Version     string            `json:"version"`
	ExportedAt  time.Time         `json:"exported_at"`
	Formats     []string          `json:"formats"`
	Counts      ExportCounts      `json:"counts"`
	ThreadIndex map[string]string `json:"thread_index,omitempty"` // uuid -> slug
}

// ExportCounts holds the number of each item type exported.
type ExportCounts struct {
	Threads   int `json:"threads"`
	Spaces    int `json:"spaces"`
	Bookmarks int `json:"bookmarks"`
	Sources   int `json:"sources"`
}

// ThreadIndex holds the cached list of thread UUIDs for resumable export.
type ThreadIndex struct {
	Threads   []ThreadRef `json:"threads"`
	Total     int         `json:"total"`
	FetchedAt time.Time   `json:"fetched_at"`
	Complete  bool        `json:"complete"` // false if listing was interrupted
}

// ThreadRef is a lightweight reference to a thread from the list endpoint.
type ThreadRef struct {
	UUID              string    `json:"uuid"`
	Title             string    `json:"title"`
	SpaceUUID         string    `json:"space_uuid,omitempty"`
	UpdatedAt         time.Time `json:"updated_at"`
	PreviousUpdatedAt time.Time `json:"-"`
}

// SavedSession represents the persisted authentication session.
type SavedSession struct {
	SessionToken string    `json:"session_token"`
	CSRFToken    string    `json:"csrf_token,omitempty"`
	Cookies      []Cookie  `json:"cookies"`
	SavedAt      time.Time `json:"saved_at"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
}

// Cookie represents a simplified browser cookie for persistence.
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires,omitempty"`
	Secure   bool    `json:"secure"`
	HTTPOnly bool    `json:"http_only"`
}
