package service

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	githubapi "github.com/entire-vc/evc-mesh/internal/integration/github"
	gitlabapi "github.com/entire-vc/evc-mesh/internal/integration/gitlab"
)

// providerMergedAtTimeout bounds the one provider read a merged_at fill makes.
const providerMergedAtTimeout = 8 * time.Second

// providerMergedAt asks the link's provider when the PR/MR was merged. It
// returns nil (and no error) when the provider cannot be asked for this link
// (no integration in the workspace, a URL that is not a PR/MR URL) or does not
// report the PR/MR as merged with a time. It never invents a time.
func (s *vcsLinkService) providerMergedAt(ctx context.Context, l domain.VCSLink) (*time.Time, error) {
	if l.LinkType != domain.VCSLinkTypePR || l.Status != domain.VCSLinkStatusMerged {
		return nil, nil
	}
	readCtx, cancel := context.WithTimeout(ctx, providerMergedAtTimeout)
	defer cancel()
	switch l.Provider {
	case domain.VCSProviderGitLab:
		projectPath, iid, ok := gitlabapi.ParseMergeRequestURL(l.URL)
		if !ok {
			return nil, nil
		}
		src, ok := s.gitlabEvidenceSourceFor(ctx, l.TaskID)
		if !ok {
			return nil, nil
		}
		ev, err := src.GetMergeRequestEvidence(readCtx, projectPath, iid)
		if err != nil {
			return nil, fmt.Errorf("read gitlab mr %s!%d: %w", projectPath, iid, err)
		}
		// The client talks only to the configured host; GitLab's own web_url
		// proves the link names this MR there, not a namesake elsewhere.
		if !sameMRWebURL(ev.WebURL, l.URL) || ev.State != "merged" {
			return nil, nil
		}
		return ev.MergedAt, nil
	case domain.VCSProviderGitHub:
		owner, repo, number, ok := githubapi.ParsePullRequestURL(l.URL)
		if !ok || s.githubEvidence == nil {
			return nil, nil
		}
		ws := s.workspaceOfTask(ctx, l.TaskID)
		if ws == uuid.Nil {
			return nil, nil
		}
		src, ok := s.githubEvidence(ctx, ws)
		if !ok || src == nil {
			return nil, nil
		}
		pr, err := src.GetPullRequestHead(readCtx, owner, repo, number)
		if err != nil {
			return nil, fmt.Errorf("read github pr %s/%s#%d: %w", owner, repo, number, err)
		}
		if !pr.Merged {
			return nil, nil
		}
		return pr.MergedAt, nil
	}
	return nil, nil
}

// fillMergedAt stores the provider's merge time on a merged link whose
// merged_at is unknown. Returns true when the row was written; a link that
// already has a time, or a provider that reports none, writes nothing.
func (s *vcsLinkService) fillMergedAt(ctx context.Context, l domain.VCSLink) (bool, error) {
	if l.MergedAt != nil {
		return false, nil
	}
	at, err := s.providerMergedAt(ctx, l)
	if err != nil || at == nil {
		return false, err
	}
	return s.repo.SetMergedAt(ctx, l.ID, *at)
}

// MergedAtBackfillPage is one page of the merged_at backfill.
type MergedAtBackfillPage struct {
	// Next is the keyset cursor for the following page; uuid.Nil with Done.
	Next uuid.UUID
	// Done is true when this page reached the end of the candidates.
	Done bool
	// Seen and Filled count the links read and the links given a time.
	Seen, Filled int
}

// BackfillMergedAt reads the provider's merge time for up to limit merged pr
// links whose merged_at is NULL, after afterID in id order, and stores what
// the provider reports. A link the provider cannot resolve stays NULL and is
// passed by the cursor. Repeating it is idempotent: SetMergedAt never
// replaces a known time.
func (s *vcsLinkService) BackfillMergedAt(ctx context.Context, afterID uuid.UUID, limit int) (MergedAtBackfillPage, error) {
	links, err := s.repo.ListMergedPRLinksWithoutMergedAt(ctx, afterID, limit)
	if err != nil {
		return MergedAtBackfillPage{Next: afterID}, fmt.Errorf("list merged links without merged_at: %w", err)
	}
	// Done is decided only after every listed link got an answer. A run cut
	// short (the caller's deadline) reports Done=false with Next at the last
	// link that got one, so the link whose read was cut is asked again first
	// on the next page. A link's own provider read is bounded by
	// providerMergedAtTimeout, well inside a fresh run's deadline, so a slow
	// provider at the head cannot stall the walk: its own timeout is an
	// answer and the cursor passes it.
	page := MergedAtBackfillPage{Next: afterID, Seen: len(links)}
	for _, l := range links {
		if ctx.Err() != nil {
			return page, ctx.Err()
		}
		w, fErr := s.fillMergedAt(ctx, l)
		if w {
			page.Filled++
		}
		if ctx.Err() != nil {
			return page, ctx.Err()
		}
		page.Next = l.ID
		if fErr != nil {
			log.Printf("[vcs-merged-at] link=%s task=%s url=%q: %v", l.ID, l.TaskID, l.URL, fErr)
		}
	}
	if len(links) < limit {
		page.Done, page.Next = true, uuid.Nil
	}
	return page, nil
}

// MergedAtBackfiller is what the backfill sweeper needs from the VCS link
// service.
type MergedAtBackfiller interface {
	BackfillMergedAt(ctx context.Context, afterID uuid.UUID, limit int) (MergedAtBackfillPage, error)
}

// The api wires the sweeper by asserting the service to this interface.
var _ MergedAtBackfiller = (*vcsLinkService)(nil)

// MergedAtSweeper walks the merged links without merged_at one page per tick.
// After a full pass it rests for cooldown before starting over, so a link the
// provider cannot resolve (no integration, deleted MR) is asked at most once
// per pass rather than on every tick.
type MergedAtSweeper struct {
	svc      MergedAtBackfiller
	batch    int
	cooldown time.Duration
	now      func() time.Time

	mu        sync.Mutex
	cursor    uuid.UUID
	restUntil time.Time
}

// NewMergedAtSweeper builds a sweeper reading batch links per tick and
// resting cooldown after each full pass.
func NewMergedAtSweeper(svc MergedAtBackfiller, batch int, cooldown time.Duration) *MergedAtSweeper {
	return &MergedAtSweeper{svc: svc, batch: batch, cooldown: cooldown, now: time.Now}
}

// Tick runs one page. Returns the page it ran (zero while resting).
func (w *MergedAtSweeper) Tick(ctx context.Context) (MergedAtBackfillPage, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.now().Before(w.restUntil) {
		return MergedAtBackfillPage{}, nil
	}
	page, err := w.svc.BackfillMergedAt(ctx, w.cursor, w.batch)
	// A page cut short (the tick's deadline hit a slow provider) still moves
	// the cursor past the links it tried, so the next tick does not re-ask
	// the same slow head forever. A failed list returns the old cursor.
	w.cursor = page.Next
	if err != nil {
		return page, err
	}
	if page.Done {
		w.restUntil = w.now().Add(w.cooldown)
	}
	return page, nil
}
