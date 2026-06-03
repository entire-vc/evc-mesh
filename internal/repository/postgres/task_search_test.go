package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestListTasksSearch_ByShortID(t *testing.T) {
	mode, clean := searchMode("#66418dda")
	assert.Equal(t, "prefix", mode)
	assert.Equal(t, "66418dda", clean)
}

func TestListTasksSearch_ByUUID(t *testing.T) {
	mode, clean := searchMode("66418dda-cecc-4a38-9895-23eef44536a5")
	assert.Equal(t, "uuid", mode)
	assert.Equal(t, "66418dda-cecc-4a38-9895-23eef44536a5", clean)
}

func TestListTasksSearch_ByHexNoHash(t *testing.T) {
	mode, clean := searchMode("66418dda")
	assert.Equal(t, "prefix", mode)
	assert.Equal(t, "66418dda", clean)
}

func TestListTasksSearch_TextRegression(t *testing.T) {
	mode, clean := searchMode("fix login bug")
	assert.Equal(t, "text", mode)
	assert.Equal(t, "fix login bug", clean)
}

func TestListTasksSearch_UppercaseHex(t *testing.T) {
	mode, clean := searchMode("#66418DDA")
	assert.Equal(t, "prefix", mode)
	assert.Equal(t, "66418dda", clean)
}

func TestListTasksSearch_ShortHexTooShort(t *testing.T) {
	// 5 hex chars — below the 6-char minimum, treated as text
	mode, _ := searchMode("abc12")
	assert.Equal(t, "text", mode)
}

func TestListTasksSearch_HashOnly(t *testing.T) {
	// bare "#" should fall through to text
	mode, _ := searchMode("#")
	assert.Equal(t, "text", mode)
}
