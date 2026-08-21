package pagination

// DefaultPageSize is the default number of items per page when not specified.
const DefaultPageSize = 50

// MaxPageSize is the maximum number of items per page to prevent abuse.
const MaxPageSize = 200

// Params holds pagination parameters extracted from an API request.
type Params struct {
	Page     int    `query:"page"`
	PageSize int    `query:"page_size"`
	SortBy   string `query:"sort_by"`
	SortDir  string `query:"sort_dir"` // "asc" or "desc"

	// RawLimit binds ?limit=N, the spelling every hand-written client reaches
	// for. Until this existed, ?limit=1 on a paginated endpoint was silently
	// discarded and the caller got the 50-item default instead — the request
	// looked honoured because a well-formed page came back.
	//
	// It is a separate field rather than a second tag on PageSize because Echo
	// binds one tag per field, and it is not named Limit because Limit() below
	// is already the accessor the repositories call.
	//
	// Normalize folds it into PageSize; nothing downstream reads it.
	RawLimit int `query:"limit"`
}

// Normalize ensures pagination parameters are within valid bounds.
//
// An explicit page_size wins over limit when a caller sends both, on the
// grounds that page_size is the documented name.
func (p *Params) Normalize() {
	if p.Page < 1 {
		p.Page = 1
	}
	if p.PageSize < 1 && p.RawLimit > 0 {
		p.PageSize = p.RawLimit
	}
	if p.PageSize < 1 {
		p.PageSize = DefaultPageSize
	}
	if p.PageSize > MaxPageSize {
		p.PageSize = MaxPageSize
	}
	if p.SortDir != "asc" && p.SortDir != "desc" {
		p.SortDir = "asc"
	}
}

// Offset returns the SQL OFFSET for the current page.
func (p Params) Offset() int {
	return (p.Page - 1) * p.PageSize
}

// Limit returns the SQL LIMIT for the current page.
func (p Params) Limit() int {
	return p.PageSize
}

// Page represents a single page of paginated results.
type Page[T any] struct {
	Items      []T  `json:"items"`
	TotalCount int  `json:"total_count"`
	Page       int  `json:"page"`
	PageSize   int  `json:"page_size"`
	TotalPages int  `json:"total_pages"`
	HasMore    bool `json:"has_more"`

	// Truncated is set by a handler that dropped content from individual items
	// (e.g. blanked long descriptions) to keep the serialized response under a
	// size ceiling. Item count, order, and offsets are unaffected — this only
	// ever means "some item(s) in this page lost a large field"; omitempty so
	// existing consumers never see the field at all.
	Truncated bool `json:"truncated,omitempty"`
}

// NewPage creates a new Page from a slice of items and a total count.
func NewPage[T any](items []T, totalCount int, params Params) *Page[T] {
	totalPages := 0
	if params.PageSize > 0 {
		totalPages = (totalCount + params.PageSize - 1) / params.PageSize
	}

	if items == nil {
		items = []T{}
	}

	return &Page[T]{
		Items:      items,
		TotalCount: totalCount,
		Page:       params.Page,
		PageSize:   params.PageSize,
		TotalPages: totalPages,
		HasMore:    params.Page < totalPages,
	}
}
