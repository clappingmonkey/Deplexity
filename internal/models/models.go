package models

import "time"

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
