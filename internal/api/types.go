package api

// These types represent the raw API response shapes from Perplexity's internal API.
// They are separate from the domain models to decouple API changes from the rest of
// the application.

// ThreadListResponse is the expected shape of GET /rest/threads/.
type ThreadListResponse []ThreadSummary

// ThreadSummary is a thread as returned in the list endpoint.
type ThreadSummary struct {
	UUID      string `json:"uuid"`
	Slug      string `json:"url_slug"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	SpaceUUID string `json:"collection_uuid,omitempty"`
	Bookmarked bool  `json:"is_bookmarked,omitempty"`
}

// ThreadDetailResponse is the expected shape of GET /rest/threads/{uuid}.
type ThreadDetailResponse struct {
	UUID      string        `json:"uuid"`
	Slug      string        `json:"url_slug"`
	Title     string        `json:"title"`
	CreatedAt string        `json:"created_at"`
	UpdatedAt string        `json:"updated_at"`
	SpaceUUID string        `json:"collection_uuid,omitempty"`
	Entries   []EntryDetail `json:"query_entries"`
}

// EntryDetail represents a single Q&A pair from the thread detail endpoint.
type EntryDetail struct {
	UUID        string        `json:"uuid"`
	Query       string        `json:"query_str"`
	Answer      string        `json:"answer"`
	Sources     []SourceEntry `json:"web_results"`
	Model       string        `json:"model,omitempty"`
	SearchFocus string        `json:"search_focus,omitempty"`
	CreatedAt   string        `json:"created_at"`
}

// SourceEntry represents a source/citation from the API.
type SourceEntry struct {
	Title   string `json:"name"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
	Favicon string `json:"favicon,omitempty"`
}

// CollectionListResponse is the expected shape of GET /rest/collections/.
type CollectionListResponse []CollectionSummary

// CollectionSummary is a space/collection as returned by the API.
type CollectionSummary struct {
	UUID         string   `json:"uuid"`
	Name         string   `json:"title"`
	Description  string   `json:"description,omitempty"`
	Instructions string   `json:"instructions,omitempty"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	ThreadUUIDs  []string `json:"thread_uuids,omitempty"`
}

// UserResponse is the expected shape of GET /rest/user/.
type UserResponse struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	ImageURL     string `json:"image_url,omitempty"`
	Subscription string `json:"subscription,omitempty"`
}

// RateLimitResponse is the expected shape of GET /rest/rate-limit/all.
type RateLimitResponse struct {
	RemainingPro  int `json:"remaining_pro"`
	RemainingFree int `json:"remaining_free"`
	LimitPro      int `json:"limit_pro"`
	LimitFree     int `json:"limit_free"`
}
