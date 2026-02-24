package pagination

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Params.Normalize
// ---------------------------------------------------------------------------

func TestParams_Normalize(t *testing.T) {
	tests := []struct {
		name             string
		input            Params
		expectedPage     int
		expectedPageSize int
		expectedSortDir  string
	}{
		{
			name:             "defaults_when_zero",
			input:            Params{},
			expectedPage:     1,
			expectedPageSize: DefaultPageSize,
			expectedSortDir:  "asc",
		},
		{
			name:             "defaults_for_negative_values",
			input:            Params{Page: -5, PageSize: -10, SortDir: "invalid"},
			expectedPage:     1,
			expectedPageSize: DefaultPageSize,
			expectedSortDir:  "asc",
		},
		{
			name:             "page_zero_becomes_1",
			input:            Params{Page: 0, PageSize: 25, SortDir: "asc"},
			expectedPage:     1,
			expectedPageSize: 25,
			expectedSortDir:  "asc",
		},
		{
			name:             "page_size_zero_becomes_default",
			input:            Params{Page: 3, PageSize: 0, SortDir: "desc"},
			expectedPage:     3,
			expectedPageSize: DefaultPageSize,
			expectedSortDir:  "desc",
		},
		{
			name:             "page_size_exceeds_max_capped",
			input:            Params{Page: 1, PageSize: 500, SortDir: "asc"},
			expectedPage:     1,
			expectedPageSize: MaxPageSize,
			expectedSortDir:  "asc",
		},
		{
			name:             "page_size_exactly_max_stays",
			input:            Params{Page: 1, PageSize: MaxPageSize, SortDir: "asc"},
			expectedPage:     1,
			expectedPageSize: MaxPageSize,
			expectedSortDir:  "asc",
		},
		{
			name:             "page_size_just_below_max_stays",
			input:            Params{Page: 1, PageSize: MaxPageSize - 1, SortDir: "desc"},
			expectedPage:     1,
			expectedPageSize: MaxPageSize - 1,
			expectedSortDir:  "desc",
		},
		{
			name:             "valid_values_preserved",
			input:            Params{Page: 5, PageSize: 100, SortDir: "desc"},
			expectedPage:     5,
			expectedPageSize: 100,
			expectedSortDir:  "desc",
		},
		{
			name:             "sort_dir_asc_accepted",
			input:            Params{Page: 1, PageSize: 10, SortDir: "asc"},
			expectedPage:     1,
			expectedPageSize: 10,
			expectedSortDir:  "asc",
		},
		{
			name:             "sort_dir_desc_accepted",
			input:            Params{Page: 1, PageSize: 10, SortDir: "desc"},
			expectedPage:     1,
			expectedPageSize: 10,
			expectedSortDir:  "desc",
		},
		{
			name:             "sort_dir_uppercase_rejected",
			input:            Params{Page: 1, PageSize: 10, SortDir: "ASC"},
			expectedPage:     1,
			expectedPageSize: 10,
			expectedSortDir:  "asc",
		},
		{
			name:             "sort_dir_random_rejected",
			input:            Params{Page: 1, PageSize: 10, SortDir: "random"},
			expectedPage:     1,
			expectedPageSize: 10,
			expectedSortDir:  "asc",
		},
		{
			name:             "sort_dir_empty_defaults_to_asc",
			input:            Params{Page: 1, PageSize: 10, SortDir: ""},
			expectedPage:     1,
			expectedPageSize: 10,
			expectedSortDir:  "asc",
		},
		{
			name:             "sort_by_preserved",
			input:            Params{Page: 1, PageSize: 10, SortBy: "created_at", SortDir: "desc"},
			expectedPage:     1,
			expectedPageSize: 10,
			expectedSortDir:  "desc",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.input
			p.Normalize()
			assert.Equal(t, tt.expectedPage, p.Page)
			assert.Equal(t, tt.expectedPageSize, p.PageSize)
			assert.Equal(t, tt.expectedSortDir, p.SortDir)
		})
	}
}

func TestParams_Normalize_PreservesSortBy(t *testing.T) {
	p := Params{SortBy: "updated_at"}
	p.Normalize()
	assert.Equal(t, "updated_at", p.SortBy)
}

// ---------------------------------------------------------------------------
// Params.Offset
// ---------------------------------------------------------------------------

func TestParams_Offset(t *testing.T) {
	tests := []struct {
		name     string
		page     int
		pageSize int
		expected int
	}{
		{"first_page", 1, 50, 0},
		{"second_page", 2, 50, 50},
		{"third_page", 3, 50, 100},
		{"page_1_size_10", 1, 10, 0},
		{"page_5_size_10", 5, 10, 40},
		{"page_1_size_200", 1, 200, 0},
		{"page_2_size_200", 2, 200, 200},
		{"page_10_size_25", 10, 25, 225},
		{"page_1_size_1", 1, 1, 0},
		{"page_100_size_1", 100, 1, 99},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Params{Page: tt.page, PageSize: tt.pageSize}
			assert.Equal(t, tt.expected, p.Offset())
		})
	}
}

// ---------------------------------------------------------------------------
// Params.Limit
// ---------------------------------------------------------------------------

func TestParams_Limit(t *testing.T) {
	tests := []struct {
		name     string
		pageSize int
		expected int
	}{
		{"default_50", 50, 50},
		{"size_10", 10, 10},
		{"size_200", 200, 200},
		{"size_1", 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Params{PageSize: tt.pageSize}
			assert.Equal(t, tt.expected, p.Limit())
		})
	}
}

func TestParams_Limit_EqualsPageSize(t *testing.T) {
	for _, size := range []int{1, 10, 50, 100, 200} {
		p := Params{PageSize: size}
		assert.Equal(t, size, p.Limit(), "Limit should equal PageSize for size=%d", size)
	}
}

// ---------------------------------------------------------------------------
// Params.SortDir (via Normalize)
// ---------------------------------------------------------------------------

func TestParams_SortDir_Normalize(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"asc", "asc", "asc"},
		{"desc", "desc", "desc"},
		{"empty", "", "asc"},
		{"ASC_uppercase", "ASC", "asc"},
		{"DESC_uppercase", "DESC", "asc"},
		{"random", "foobar", "asc"},
		{"mixed_case", "Asc", "asc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Params{Page: 1, PageSize: 10, SortDir: tt.input}
			p.Normalize()
			assert.Equal(t, tt.expected, p.SortDir)
		})
	}
}

// ---------------------------------------------------------------------------
// NewPage
// ---------------------------------------------------------------------------

func TestNewPage_BasicPagination(t *testing.T) {
	items := []string{"a", "b", "c"}
	params := Params{Page: 1, PageSize: 10}

	page := NewPage(items, 3, params)

	assert.Equal(t, items, page.Items)
	assert.Equal(t, 3, page.TotalCount)
	assert.Equal(t, 1, page.Page)
	assert.Equal(t, 10, page.PageSize)
	assert.Equal(t, 1, page.TotalPages)
	assert.False(t, page.HasMore)
}

func TestNewPage_TotalPagesCalculation(t *testing.T) {
	tests := []struct {
		name          string
		totalCount    int
		pageSize      int
		expectedPages int
	}{
		{"exact_division", 100, 10, 10},
		{"with_remainder", 101, 10, 11},
		{"single_page", 5, 10, 1},
		{"exactly_one_page", 10, 10, 1},
		{"large_page_size", 5, 200, 1},
		{"many_pages", 999, 50, 20},
		{"one_item", 1, 50, 1},
		{"one_item_per_page", 5, 1, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := Params{Page: 1, PageSize: tt.pageSize}
			page := NewPage([]int{}, tt.totalCount, params)
			assert.Equal(t, tt.expectedPages, page.TotalPages)
		})
	}
}

func TestNewPage_HasMore(t *testing.T) {
	tests := []struct {
		name       string
		page       int
		pageSize   int
		totalCount int
		hasMore    bool
	}{
		{"first_of_multiple", 1, 10, 25, true},
		{"middle_page", 2, 10, 25, true},
		{"last_page", 3, 10, 25, false},
		{"single_page", 1, 10, 5, false},
		{"exact_boundary_first", 1, 10, 10, false},
		{"exact_boundary_first_of_two", 1, 10, 20, true},
		{"exact_boundary_second_of_two", 2, 10, 20, false},
		{"beyond_total", 5, 10, 25, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := Params{Page: tt.page, PageSize: tt.pageSize}
			page := NewPage([]string{}, tt.totalCount, params)
			assert.Equal(t, tt.hasMore, page.HasMore)
		})
	}
}

func TestNewPage_NilItems_BecomesEmptySlice(t *testing.T) {
	params := Params{Page: 1, PageSize: 10}
	page := NewPage[string](nil, 0, params)

	require.NotNil(t, page.Items)
	assert.Equal(t, []string{}, page.Items)
	assert.Len(t, page.Items, 0)
}

func TestNewPage_NilItems_NonZeroTotal(t *testing.T) {
	// Edge case: nil items but total > 0 (e.g., page beyond range)
	params := Params{Page: 100, PageSize: 10}
	page := NewPage[int](nil, 50, params)

	require.NotNil(t, page.Items)
	assert.Equal(t, []int{}, page.Items)
	assert.Equal(t, 50, page.TotalCount)
	assert.Equal(t, 5, page.TotalPages)
	assert.False(t, page.HasMore) // page 100 > 5 total pages
}

func TestNewPage_EmptyItems(t *testing.T) {
	params := Params{Page: 1, PageSize: 10}
	page := NewPage([]string{}, 0, params)

	require.NotNil(t, page.Items)
	assert.Equal(t, []string{}, page.Items)
	assert.Equal(t, 0, page.TotalCount)
	assert.Equal(t, 0, page.TotalPages)
	assert.False(t, page.HasMore)
}

func TestNewPage_ZeroTotal(t *testing.T) {
	params := Params{Page: 1, PageSize: 50}
	page := NewPage([]string{}, 0, params)

	assert.Equal(t, 0, page.TotalCount)
	assert.Equal(t, 0, page.TotalPages)
	assert.False(t, page.HasMore)
	assert.Equal(t, 1, page.Page)
	assert.Equal(t, 50, page.PageSize)
}

func TestNewPage_ZeroPageSize(t *testing.T) {
	// Edge case: zero page size should result in 0 total pages (no division by zero)
	params := Params{Page: 1, PageSize: 0}
	page := NewPage([]string{"a"}, 10, params)

	assert.Equal(t, 0, page.TotalPages)
	assert.False(t, page.HasMore)
}

func TestNewPage_ExactPageBoundary(t *testing.T) {
	// 20 items, 10 per page = exactly 2 pages
	params := Params{Page: 2, PageSize: 10}
	page := NewPage([]string{"item"}, 20, params)

	assert.Equal(t, 2, page.TotalPages)
	assert.False(t, page.HasMore) // page 2 is the last
}

func TestNewPage_OneItem(t *testing.T) {
	params := Params{Page: 1, PageSize: 50}
	page := NewPage([]string{"only"}, 1, params)

	assert.Equal(t, 1, page.TotalCount)
	assert.Equal(t, 1, page.TotalPages)
	assert.False(t, page.HasMore)
	assert.Equal(t, []string{"only"}, page.Items)
}

func TestNewPage_PreservesPageAndPageSize(t *testing.T) {
	params := Params{Page: 3, PageSize: 25}
	page := NewPage([]string{}, 100, params)

	assert.Equal(t, 3, page.Page)
	assert.Equal(t, 25, page.PageSize)
}

func TestNewPage_GenericTypes(t *testing.T) {
	t.Run("int_items", func(t *testing.T) {
		params := Params{Page: 1, PageSize: 10}
		page := NewPage([]int{1, 2, 3}, 3, params)
		assert.Equal(t, []int{1, 2, 3}, page.Items)
	})

	t.Run("struct_items", func(t *testing.T) {
		type Item struct{ Name string }
		params := Params{Page: 1, PageSize: 10}
		items := []Item{{Name: "a"}, {Name: "b"}}
		page := NewPage(items, 2, params)
		assert.Equal(t, items, page.Items)
	})
}

// ---------------------------------------------------------------------------
// Page JSON serialization
// ---------------------------------------------------------------------------

func TestPage_JSONSerialization(t *testing.T) {
	params := Params{Page: 2, PageSize: 10}
	page := NewPage([]string{"x", "y"}, 25, params)

	data, err := json.Marshal(page)
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)

	assert.Contains(t, raw, "items")
	assert.Contains(t, raw, "total_count")
	assert.Contains(t, raw, "page")
	assert.Contains(t, raw, "page_size")
	assert.Contains(t, raw, "total_pages")
	assert.Contains(t, raw, "has_more")
}

func TestPage_JSONSerialization_EmptyItems_NotNull(t *testing.T) {
	params := Params{Page: 1, PageSize: 10}
	page := NewPage[string](nil, 0, params)

	data, err := json.Marshal(page)
	require.NoError(t, err)

	// items should be [] not null
	var raw map[string]json.RawMessage
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)

	assert.Equal(t, "[]", string(raw["items"]))
}

func TestPage_JSONRoundtrip(t *testing.T) {
	params := Params{Page: 1, PageSize: 10}
	original := NewPage([]string{"a", "b", "c"}, 50, params)

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded Page[string]
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.Items, decoded.Items)
	assert.Equal(t, original.TotalCount, decoded.TotalCount)
	assert.Equal(t, original.Page, decoded.Page)
	assert.Equal(t, original.PageSize, decoded.PageSize)
	assert.Equal(t, original.TotalPages, decoded.TotalPages)
	assert.Equal(t, original.HasMore, decoded.HasMore)
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

func TestConstants(t *testing.T) {
	assert.Equal(t, 50, DefaultPageSize)
	assert.Equal(t, 200, MaxPageSize)
}
