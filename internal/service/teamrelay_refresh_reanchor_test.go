package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// R4 (#669122e6): a Team Relay copy's body can be rewritten out from under its
// comments by RefreshIfStale (R3, #d5aedf36) — an edit the comment's author
// never made and never saw. Without re-anchoring on that path, every anchored
// comment on an actively-edited mounted page goes stale the moment the
// freshness check catches up, same defect class as #90dd31f9 (own-document
// PATCH), different trigger.

// fakeTRRefresher stands in for teamRelayMountService.RefreshIfStale's two
// branches, the ones GetByIDInWorkspace has to tell apart: nextBody set is the
// branch that rewrote the body and bumped the version (RefreshSyncedCopy's
// bumpVersion=true); nextBody == "" is the same-hash branch that only stamps
// synced_at, touching neither. Re-anchoring must run on the first and not the
// second — this fake is what lets a test drive each independently of the real
// Team Relay client and the TTL clock.
type fakeTRRefresher struct {
	storage  DocumentStore
	nextBody string
}

func (f *fakeTRRefresher) RefreshIfStale(ctx context.Context, doc *domain.Document) error {
	if f.nextBody == "" {
		return nil
	}
	if err := f.storage.Upload(ctx, doc.StorageKey, strings.NewReader(f.nextBody), int64(len(f.nextBody)), documentContentType); err != nil {
		return err
	}
	doc.Version++
	doc.Body = f.nextBody
	return nil
}

// seedTeamRelayCopy inserts a document already shaped like a mounted copy —
// SourceKind plus the four fields chk_documents_source_shape requires in prod
// — bypassing Create (which only ever produces SourceKindOwn) the way a real
// mount bypasses it too, via CreateExternalCopy.
func seedTeamRelayCopy(t *testing.T, f *documentFixture, body string) *domain.Document {
	t.Helper()
	share, path, sha := "share-id", "notes/design.md", "deadbeef"
	syncedAt := time.Now().Add(-time.Hour)
	doc := &domain.Document{
		ID:           uuid.New(),
		ProjectID:    f.projectID,
		Title:        "Смонтированный документ",
		StorageKey:   "docs/" + uuid.New().String(),
		Version:      1,
		SourceKind:   domain.DocumentSourceTeamRelay,
		SourceShare:  &share,
		SourcePath:   &path,
		SourceSHA256: &sha,
		SyncedAt:     &syncedAt,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	f.repo.Seed(doc)
	require.NoError(t, f.storage.Upload(context.Background(), doc.StorageKey, strings.NewReader(body), int64(len(body)), documentContentType))
	return doc
}

// AC1: an edit AROUND the quote, made outside (on the Team Relay source, not
// through Update) and picked up by the next open, must leave the comment on
// its own words — proved by slicing the stored offsets, not by trusting the
// response, same discipline as requireAnchorHonest everywhere else in this
// file. Cyrillic body per AC4: on ASCII this would pass under either byte or
// rune offsets, which is exactly the ambiguity #90dd31f9 was about.
func TestGetByIDInWorkspace_TeamRelayRefreshReanchorsAroundEdit(t *testing.T) {
	f := setupDocumentService(t)
	refresher := &fakeTRRefresher{storage: f.storage}
	f.svc.trRefresher = refresher

	const v1 = "# Заголовок\n\nЯкорь висит вот на этой фразе снаружи.\n"
	doc := seedTeamRelayCopy(t, f, v1)
	commentID := seedAnchoredComment(t, f, doc.ID, v1, "висит вот на этой фразе снаружи")

	v2 := "# Заголовок\n\nНовый абзац сверху, добавленный правкой снаружи, в Team Relay.\n\n" +
		"Якорь висит вот на этой фразе снаружи.\n"
	refresher.nextBody = v2

	got, err := f.svc.GetByIDInWorkspace(context.Background(), doc.ID, f.wsID)
	require.NoError(t, err)
	assert.Equal(t, v2, got.Body, "the refreshed body is what the read returns")

	after := storedAnchor(t, f, commentID)
	requireAnchorHonest(t, v2, after)
	require.False(t, after.IsOrphaned(), "the quoted sentence is still on the page, just pushed down by an external edit")
}

// AC2: an edit that rewrites the quote ITSELF, made on the source and picked
// up on open, must leave the comment honestly orphaned — not silently glued
// to whatever text now occupies its old byte range.
func TestGetByIDInWorkspace_TeamRelayRefreshOrphansCommentWhenQuoteIsRewritten(t *testing.T) {
	f := setupDocumentService(t)
	refresher := &fakeTRRefresher{storage: f.storage}
	f.svc.trRefresher = refresher

	const v1 = "Абзац один.\n\nЯкорь висит вот на этой фразе снаружи.\n"
	doc := seedTeamRelayCopy(t, f, v1)
	commentID := seedAnchoredComment(t, f, doc.ID, v1, "висит вот на этой фразе снаружи")

	v2 := "Абзац один.\n\nЭту фразу переписали снаружи до неузнаваемости.\n"
	refresher.nextBody = v2

	_, err := f.svc.GetByIDInWorkspace(context.Background(), doc.ID, f.wsID)
	require.NoError(t, err)

	after := storedAnchor(t, f, commentID)
	requireAnchorHonest(t, v2, after)
	require.True(t, after.IsOrphaned(), "the source rewrote the exact sentence this comment was about")
}

// Counterpart to the two tests above, not a numbered AC: the same-hash branch
// (source unchanged, RefreshIfStale only stamps synced_at) must NOT run the
// re-anchor pass. This is what proves the gate added for R4 keys off "the body
// was actually rewritten", not off "a Team Relay document was opened" — the
// latter would re-scan every mounted document's comments on every single open,
// which is the §3.6 network storm concern applied to a local write instead of
// a network call.
func TestGetByIDInWorkspace_TeamRelayRefreshWithinTTL_DoesNotReanchor(t *testing.T) {
	f := setupDocumentService(t)
	refresher := &fakeTRRefresher{storage: f.storage} // nextBody left empty: same-hash branch
	f.svc.trRefresher = refresher

	const v1 = "Абзац один.\n\nЯкорь висит вот на этой фразе снаружи.\n"
	doc := seedTeamRelayCopy(t, f, v1)
	seedAnchoredComment(t, f, doc.ID, v1, "висит вот на этой фразе снаружи")

	writesBefore := f.comments.AnchorWrites()
	_, err := f.svc.GetByIDInWorkspace(context.Background(), doc.ID, f.wsID)
	require.NoError(t, err)

	assert.Equal(t, writesBefore, f.comments.AnchorWrites(), "the source did not change, so there is nothing to re-anchor against")
}
