package wire

// DiscoveryPage is the DiscoveryPage object in docs/05-portability.md Snapshot pagination.
type DiscoveryPage struct {
	GeneratedAt string                   `json:"generated_at"`
	ExpiresAt   string                   `json:"expires_at"`
	Messages    []PublicTransportMessage `json:"messages"`
	NextCursor  *string                  `json:"next_cursor"`
}

// MessageListQuery is the MessageListQuery object in docs/05-portability.md Listing and date filters.
type MessageListQuery struct {
	Date   string `json:"date,omitempty"`
	From   string `json:"from,omitempty"`
	Until  string `json:"until,omitempty"`
	Limit  int64  `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

// AuthorMessageListQuery is the AuthorMessageListQuery object in docs/05-portability.md Listing and date filters.
type AuthorMessageListQuery struct {
	Date     string `json:"date,omitempty"`
	From     string `json:"from,omitempty"`
	Until    string `json:"until,omitempty"`
	Limit    int64  `json:"limit,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	AuthorID string `json:"author_id"`
}

// ChangesPage is the ChangesPage object in docs/05-portability.md Incremental sync.
type ChangesPage struct {
	ProviderID string            `json:"provider_id"`
	Items      []ChangesPageItem `json:"items"`
	NextAfter  int64             `json:"next_after"`
	More       bool              `json:"more"`
}

// ChangesPageItem is the inline ChangesPage.items object in docs/05-portability.md Incremental sync.
type ChangesPageItem struct {
	Sequence int64                  `json:"sequence"`
	Message  PublicTransportMessage `json:"message"`
}
