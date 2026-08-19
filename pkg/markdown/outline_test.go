package markdown

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fence is written out of band because the fixture below contains backtick
// fences and Go raw strings cannot.
const fence = "```"

// guideDoc is the fixture the outline tests share. It is deliberately a Russian
// document: the two slugifiers already in this repo drop non-ASCII entirely, so
// an ASCII-only fixture would pass against a slugifier that reduces every
// Cyrillic heading to the empty string.
//
// It carries, on purpose:
//   - YAML frontmatter whose `#` is a YAML comment, not a heading;
//   - a fenced code block containing lines that look exactly like headings;
//   - `### Linux` twice under DIFFERENT parents — separable by path;
//   - `### Логи` twice under the SAME parent — not separable by path, so the
//     ambiguity has to be refused rather than guessed;
//   - a subsection nested inside a section, to prove it travels with its parent.
var guideDoc = strings.Join([]string{
	"---",
	"title: Руководство",
	"# это комментарий YAML, а не заголовок",
	"---",
	"",
	"# Руководство по установке",
	"",
	"Вводный абзац.",
	"",
	"## Установка",
	"",
	"Общие слова про установку.",
	"",
	"### Linux",
	"",
	"Ставим через apt.",
	"",
	fence + "bash",
	"# это комментарий шелла, а не заголовок",
	"## и это тоже не заголовок",
	"apt install mesh",
	fence,
	"",
	"Ещё абзац про Linux.",
	"",
	"#### Зависимости",
	"",
	"Список зависимостей для Linux.",
	"",
	"### macOS",
	"",
	"Ставим через brew.",
	"",
	"## Настройка",
	"",
	"Про настройку.",
	"",
	"### Linux",
	"",
	"Правим /etc/mesh.conf.",
	"",
	"## Диагностика",
	"",
	"### Логи",
	"",
	"Первый блок про логи.",
	"",
	"### Логи",
	"",
	"Второй блок про логи.",
	"",
}, "\n")

// AC1 — the table of contents reflects the heading structure: right headings,
// right levels, right order, right nesting.
func TestTOC_ReflectsHeadingStructure(t *testing.T) {
	toc := TOC(guideDoc)

	type entry struct {
		level int
		text  string
		path  string
	}
	got := make([]entry, 0, len(toc))
	for _, h := range toc {
		got = append(got, entry{h.Level, h.Text, h.PathString()})
	}

	assert.Equal(t, []entry{
		{1, "Руководство по установке", "руководство-по-установке"},
		{2, "Установка", "руководство-по-установке/установка"},
		{3, "Linux", "руководство-по-установке/установка/linux"},
		{4, "Зависимости", "руководство-по-установке/установка/linux/зависимости"},
		{3, "macOS", "руководство-по-установке/установка/macos"},
		{2, "Настройка", "руководство-по-установке/настройка"},
		{3, "Linux", "руководство-по-установке/настройка/linux"},
		{2, "Диагностика", "руководство-по-установке/диагностика"},
		{3, "Логи", "руководство-по-установке/диагностика/логи"},
		{3, "Логи", "руководство-по-установке/диагностика/логи"},
	}, got)
}

// AC1, negative control — a `#` that only looks like a heading must not reach
// the table of contents. This is the classic line-by-line parsing defect, and
// the assertion is on absence, so it fails loudly if fence tracking regresses.
func TestTOC_IgnoresHeadingsInsideCodeFencesAndFrontmatter(t *testing.T) {
	toc := TOC(guideDoc)

	for _, h := range toc {
		assert.NotContains(t, h.Text, "не заголовок",
			"a line inside a code fence or YAML frontmatter reached the table of contents: %q", h.Text)
	}
	assert.Len(t, toc, 10, "the fixture has exactly ten real headings")
}

func TestTOC_IgnoresIndentedCodeAndHashesWithoutSpace(t *testing.T) {
	body := strings.Join([]string{
		"# Real heading",
		"",
		"    # indented code, four spaces",
		"",
		"#hashtag is not a heading",
		"",
		"see issue #123 for details",
		"",
		"####### seven hashes is not a heading",
		"",
		"## Also real",
	}, "\n")

	toc := TOC(body)
	require.Len(t, toc, 2)
	assert.Equal(t, "Real heading", toc[0].Text)
	assert.Equal(t, "Also real", toc[1].Text)
}

func TestTOC_TildeFenceAndClosingHashes(t *testing.T) {
	body := strings.Join([]string{
		"## Title with closing hashes ##",
		"",
		"~~~",
		"# not a heading",
		"~~~",
		"",
		"## After the fence",
	}, "\n")

	toc := TOC(body)
	require.Len(t, toc, 2)
	assert.Equal(t, "Title with closing hashes", toc[0].Text)
	assert.Equal(t, "After the fence", toc[1].Text)
}

// AC2 — a section is that section and nothing after it. The load-bearing
// assertion is the absence of the NEXT heading's text: "returns something
// non-empty" would pass against a function that returns the whole document.
//
// AC3 — the nested `#### Зависимости` travels with its parent `### Linux`.
func TestSection_ReturnsOnlyThatSectionIncludingSubsections(t *testing.T) {
	h, content, err := Section(guideDoc, "установка/linux")
	require.NoError(t, err)

	assert.Equal(t, "Linux", h.Text)
	assert.Equal(t, 3, h.Level)

	assert.True(t, strings.HasPrefix(content, "### Linux\n"),
		"the section starts at its own heading line, got: %q", firstLine(content))

	// AC3: the subsection and its body are inside the parent section.
	assert.Contains(t, content, "#### Зависимости")
	assert.Contains(t, content, "Список зависимостей для Linux.")

	// Its own body, including the fenced block that lives inside it.
	assert.Contains(t, content, "Ставим через apt.")
	assert.Contains(t, content, "apt install mesh")
	assert.Contains(t, content, "Ещё абзац про Linux.")

	// AC2: the next sibling heading of the same level, and its body, are OUT.
	assert.NotContains(t, content, "macOS",
		"the next sibling's heading text leaked into the section")
	assert.NotContains(t, content, "Ставим через brew.",
		"the next sibling's body leaked into the section")

	// And nothing from the sections after it either.
	assert.NotContains(t, content, "Настройка")
	assert.NotContains(t, content, "Диагностика")
}

// AC3 again, one level up: a section ends at the next heading of the same OR
// HIGHER rank, so `## Установка` swallows every `###` and `####` under it and
// stops at the next `##`.
func TestSection_EndsAtSameOrHigherLevel(t *testing.T) {
	_, content, err := Section(guideDoc, "установка")
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(content, "## Установка\n"))
	assert.Contains(t, content, "### Linux")
	assert.Contains(t, content, "#### Зависимости")
	assert.Contains(t, content, "### macOS")
	assert.Contains(t, content, "Ставим через brew.")

	assert.NotContains(t, content, "## Настройка",
		"the section ran past the next heading of the same level")
	assert.NotContains(t, content, "Про настройку.")
}

func TestSection_LastSectionRunsToEndOfDocument(t *testing.T) {
	_, content, err := Section(guideDoc, "логи-2")
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(content, "### Логи\n"))
	assert.Contains(t, content, "Второй блок про логи.")
	assert.NotContains(t, content, "Первый блок про логи.")
}

// AC4 — duplicate heading text under two different parents. The path picks the
// right one; the bare name resolves to neither.
func TestSection_DuplicateHeadingUnderDifferentParents(t *testing.T) {
	_, install, err := Section(guideDoc, "установка/linux")
	require.NoError(t, err)
	assert.Contains(t, install, "Ставим через apt.")
	assert.NotContains(t, install, "Правим /etc/mesh.conf.")

	_, configure, err := Section(guideDoc, "настройка/linux")
	require.NoError(t, err)
	assert.Contains(t, configure, "Правим /etc/mesh.conf.")
	assert.NotContains(t, configure, "Ставим через apt.")
}

// AC4, the part that matters — a bare repeated name is REFUSED, not silently
// resolved to the first match, and the refusal names references that work.
func TestSection_BareDuplicateNameIsRefusedNotFirstMatch(t *testing.T) {
	_, _, err := Section(guideDoc, "linux")
	require.Error(t, err)

	var ambiguous *AmbiguousError
	require.ErrorAs(t, err, &ambiguous)
	assert.ElementsMatch(t, []string{"linux-1", "linux-2"}, ambiguous.Anchors)
	assert.ElementsMatch(t, []string{
		"руководство-по-установке/установка/linux",
		"руководство-по-установке/настройка/linux",
	}, ambiguous.Paths)

	// The anchors the error hands back must each resolve to exactly one
	// section — an error suggesting a reference that is itself ambiguous is
	// no better than a first match.
	_, first, err := Section(guideDoc, ambiguous.Anchors[0])
	require.NoError(t, err)
	_, second, err := Section(guideDoc, ambiguous.Anchors[1])
	require.NoError(t, err)
	assert.NotEqual(t, first, second)
}

// AC4 — duplicates under the SAME parent cannot be told apart by path, so the
// path is ambiguous too and only the anchor resolves them.
func TestSection_DuplicateHeadingUnderSameParent(t *testing.T) {
	_, _, err := Section(guideDoc, "диагностика/логи")
	var ambiguous *AmbiguousError
	require.ErrorAs(t, err, &ambiguous,
		"two sibling headings with the same text are not separable by path and must be refused")

	_, firstLog, err := Section(guideDoc, "логи-1")
	require.NoError(t, err)
	assert.Contains(t, firstLog, "Первый блок про логи.")
	assert.NotContains(t, firstLog, "Второй блок про логи.")

	// The path may still be used to narrow the branch, with the anchor
	// picking the sibling.
	_, secondLog, err := Section(guideDoc, "диагностика/логи-2")
	require.NoError(t, err)
	assert.Contains(t, secondLog, "Второй блок про логи.")
	assert.NotContains(t, secondLog, "Первый блок про логи.")
}

// AC4 — a duplicated slug leaves NO unsuffixed anchor behind. If `логи` were
// still a valid anchor for the first occurrence, the refusal above would be
// decorative: the caller would work around it by using the bare name and get a
// silent first match after all.
func TestTOC_DuplicateSlugLeavesNoBareAnchor(t *testing.T) {
	anchors := map[string]bool{}
	for _, h := range TOC(guideDoc) {
		assert.False(t, anchors[h.Anchor], "anchor %q is not unique", h.Anchor)
		anchors[h.Anchor] = true
	}

	assert.True(t, anchors["логи-1"])
	assert.True(t, anchors["логи-2"])
	assert.False(t, anchors["логи"], "the bare slug of a duplicated heading must address nothing")
	assert.True(t, anchors["linux-1"])
	assert.True(t, anchors["linux-2"])
	assert.False(t, anchors["linux"])

	// A heading whose slug is unique keeps the clean anchor.
	assert.True(t, anchors["установка"])
	assert.True(t, anchors["macos"])
}

// AC5 — Cyrillic and other non-ASCII headings survive into anchors. Both
// slugifiers already in this repo return "" for every one of these inputs.
func TestAnchorize_NonASCII(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Установка", "установка"},
		{"Руководство по установке", "руководство-по-установке"},
		{"Логи и метрики", "логи-и-метрики"},
		{"ЗАГЛАВНЫМИ БУКВАМИ", "заглавными-буквами"},
		{"Раздел 2. Настройка", "раздел-2-настройка"},
		{"Ошибки/предупреждения", "ошибки-предупреждения"},
		{"Раздел  с  двойными  пробелами", "раздел-с-двойными-пробелами"},
		{"  Отбитые пробелы  ", "отбитые-пробелы"},
		{"Ελληνικά", "ελληνικά"},
		{"日本語のセクション", "日本語のセクション"},
		{"snake_case_kept", "snake_case_kept"},
		{"**Жирный** заголовок", "жирный-заголовок"},
		{"Mixed Кириллица and latin", "mixed-кириллица-and-latin"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, Anchorize(tc.in), "Anchorize(%q)", tc.in)
	}
}

// AC5 — the anchors of a wholly Cyrillic document are non-empty and distinct.
// The failure this guards against is not "wrong anchor" but "every anchor is
// the empty string", which makes every heading collide and none addressable.
func TestTOC_CyrillicDocumentHasUsableAnchors(t *testing.T) {
	body := strings.Join([]string{
		"# Первый",
		"текст",
		"## Второй",
		"текст",
		"## Третий",
		"текст",
	}, "\n")

	toc := TOC(body)
	require.Len(t, toc, 3)
	assert.Equal(t, []string{"первый", "второй", "третий"},
		[]string{toc[0].Anchor, toc[1].Anchor, toc[2].Anchor})

	_, content, err := Section(body, "Второй")
	require.NoError(t, err, "a reference written in the original casing must resolve")
	assert.Equal(t, "## Второй\nтекст\n", content)
}

// AC6 at the pure level — the outline is derived from the body it is given, so
// two bodies give two outlines. The service test proves the same across a real
// write; this proves the function itself holds no state.
func TestTOC_IsRecomputedFromTheBodyGiven(t *testing.T) {
	before := "# Один\nтело\n\n## Два\nтело\n"
	after := "# Один\nтело\n\n## Два\nтело\n\n## Три\nновое тело\n"

	assert.Len(t, TOC(before), 2)
	assert.Len(t, TOC(after), 3)

	_, _, err := Section(before, "три")
	require.Error(t, err, "a heading that does not exist yet must not resolve")

	_, content, err := Section(after, "три")
	require.NoError(t, err)
	assert.Contains(t, content, "новое тело")
}

func TestSection_UnknownReferenceIsAClearError(t *testing.T) {
	_, _, err := Section(guideDoc, "нет-такого-раздела")

	var notFound *NotFoundError
	require.ErrorAs(t, err, &notFound)
	assert.Contains(t, err.Error(), "нет-такого-раздела")
}

func TestSection_PathThatDoesNotNestIsNotFound(t *testing.T) {
	// `macos` exists and `настройка` exists, but macOS is not under Настройка.
	_, _, err := Section(guideDoc, "настройка/macos")
	var notFound *NotFoundError
	require.ErrorAs(t, err, &notFound)
}

func TestSection_EmptyAndNoisyReferences(t *testing.T) {
	for _, ref := range []string{"", "/", "///", "   "} {
		_, _, err := Section(guideDoc, ref)
		require.Error(t, err, "ref %q must not resolve", ref)
	}

	// Extra separators around a real reference are noise, not meaning.
	_, content, err := Section(guideDoc, "/установка/linux/")
	require.NoError(t, err)
	assert.Contains(t, content, "Ставим через apt.")
}

func TestTOC_EmptyAndHeadinglessBodies(t *testing.T) {
	assert.Empty(t, TOC(""))
	assert.Empty(t, TOC("просто текст без заголовков\nи вторая строка\n"))
}

// A heading with no sluggable characters still gets an address. Documented
// behaviour rather than an accident: the alternative is a section nobody can
// reach.
func TestTOC_UnsluggableHeadingFallsBackToPosition(t *testing.T) {
	body := "# ***\nтело\n\n## 🎉\nещё тело\n"
	toc := TOC(body)
	require.Len(t, toc, 2)
	assert.Equal(t, "section-1", toc[0].Anchor)
	assert.Equal(t, "section-2", toc[1].Anchor)

	_, content, err := Section(body, "section-2")
	require.NoError(t, err)
	assert.Contains(t, content, "ещё тело")
}

// Setext headings (`===` / `---` underlines) are NOT recognised. Asserted so
// that the limitation is a decision on the record rather than something a reader
// has to infer from the absence of a test: `---` is also a thematic break and a
// frontmatter delimiter, and guessing between them is how a table of contents
// acquires entries the author never wrote.
func TestTOC_SetextHeadingsAreNotRecognised(t *testing.T) {
	body := "Заголовок\n=========\n\nтело\n"
	assert.Empty(t, TOC(body))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
