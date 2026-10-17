package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/integration/teamrelay"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/mdoc"
)

// DefaultTeamRelaySyncTTL is the freshness window used when a project's Team
// Relay settings don't name one. R5 is what lets a human change it per
// project; until it exists, this is the TTL every mounted share gets.
const DefaultTeamRelaySyncTTL = 5 * time.Minute

// defaultMountExcludedPaths is what a project with no exclusions configured
// gets: card artifacts live on the card's artifacts tab already, so mounting a
// second copy of them under Docs would be pure noise. See
// TeamRelaySettings.ExcludedPaths for why this is a default applied at read
// time rather than a value written into every settings row.
var defaultMountExcludedPaths = []string{"artifacts"}

// effectiveExcludedPaths returns the exclusion list a mount run uses. nil
// (field absent — never configured) selects the default; an explicitly empty
// slice selects none. Anything else is used as given, entries trimmed, blanks
// dropped — the upsert already refuses an entry that is not a bare segment,
// so by the time it is stored here it is clean.
func effectiveExcludedPaths(settings domain.TeamRelaySettings) []string {
	if settings.ExcludedPaths == nil {
		return defaultMountExcludedPaths
	}
	out := make([]string, 0, len(settings.ExcludedPaths))
	for _, p := range settings.ExcludedPaths {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// isMountPathExcluded reports whether sharePath's FIRST segment is one of the
// excluded segments. First segment, not substring: "artifacts" excludes
// "artifacts/card42/note.md", does not exclude "my-artifacts/x.md" or
// "docs/artifacts-intro.md".
func isMountPathExcluded(sharePath string, excluded []string) bool {
	seg := sharePath
	if i := strings.IndexByte(sharePath, '/'); i >= 0 {
		seg = sharePath[:i]
	}
	for _, e := range excluded {
		if seg == e {
			return true
		}
	}
	return false
}

// RelaySyncClient is the slice of the Team Relay sync-protocol client that
// mounting and freshness refresh depend on. It exists so a test can substitute
// a counting fake instead of the real HTTP client — the AC3 counter test
// (open twice inside TTL → zero calls on the second open; push synced_at past
// TTL → exactly one call) has no other way to observe "did this make a
// network call" without one.
type RelaySyncClient interface {
	FilesIndex(ctx context.Context, relayURL, shareID, agentKey string) ([]teamrelay.SyncIndexEntry, error)
	Download(ctx context.Context, relayURL, shareID, path, agentKey string) (*teamrelay.SyncDocument, error)
	// Write is the conditional write-back (R8). ifMatchSHA256 is the hash of
	// the version being replaced; the relay refuses anything else, including
	// the wildcard. A refused write returns teamrelay.ErrSyncConflict and has
	// changed nothing on their side.
	Write(ctx context.Context, relayURL, shareID, path, agentKey, ifMatchSHA256 string, body []byte) (*teamrelay.SyncWriteResult, error)
}

// realRelaySyncClient is RelaySyncClient backed by the actual sync-protocol
// HTTP calls in internal/integration/teamrelay.
type realRelaySyncClient struct{}

func (realRelaySyncClient) FilesIndex(ctx context.Context, relayURL, shareID, agentKey string) ([]teamrelay.SyncIndexEntry, error) {
	return teamrelay.SyncFilesIndex(ctx, relayURL, shareID, agentKey)
}

func (realRelaySyncClient) Download(ctx context.Context, relayURL, shareID, filePath, agentKey string) (*teamrelay.SyncDocument, error) {
	return teamrelay.SyncDownload(ctx, relayURL, shareID, filePath, agentKey)
}

func (realRelaySyncClient) Write(ctx context.Context, relayURL, shareID, filePath, agentKey, ifMatchSHA256 string, body []byte) (*teamrelay.SyncWriteResult, error) {
	return teamrelay.SyncWrite(ctx, relayURL, shareID, filePath, agentKey, ifMatchSHA256, body)
}

// KeyDescriber is the seam over teamrelay.DescribeAgentKey — same reasoning as
// RelaySyncClient above: a test substitutes a fake instead of reaching a real
// Team Relay server.
type KeyDescriber interface {
	DescribeAgentKey(ctx context.Context, relayURL, shareID, agentKey string) (*teamrelay.AgentKeyDescription, error)
}

type realKeyDescriber struct{}

func (realKeyDescriber) DescribeAgentKey(ctx context.Context, relayURL, shareID, agentKey string) (*teamrelay.AgentKeyDescription, error) {
	return teamrelay.DescribeAgentKey(ctx, relayURL, shareID, agentKey)
}

// MountStatus names why SyncMount did or did not materialize a share, in a
// form the caller can render as a distinct, legible state — AC-4's negative
// control is exactly this: a protoken key or an unreachable relay must not
// look like "this share has no documents", which is what an empty result
// with no status would otherwise mean.
type MountStatus string

const (
	// MountStatusOK means the run completed — Mounted/Skipped describe what it did.
	MountStatusOK MountStatus = "ok"
	// MountStatusNotConfigured means this project has no Team Relay integration,
	// or it names no share — an ordinary, unremarkable state, not a failure.
	MountStatusNotConfigured MountStatus = "not_configured"
	// MountStatusKeyRejected mirrors teamrelay.ErrKeyRejected: the agent key is
	// missing or unrecognized. Distinct from ForeignShare so an operator isn't
	// told to rotate a key that was never the problem.
	MountStatusKeyRejected MountStatus = "key_rejected"
	// MountStatusForeignShare mirrors teamrelay.ErrForeignShare: a real key, but
	// not valid for the configured share.
	MountStatusForeignShare MountStatus = "foreign_share"
	// MountStatusKeyExpired mirrors teamrelay.ErrKeyExpired: the key was once
	// valid for this share but its TTL has passed. Distinct from
	// MountStatusForeignShare — see ErrKeyExpired's own doc comment for why
	// collapsing the two was the actual defect #218d5847's AC4 exists to close.
	MountStatusKeyExpired MountStatus = "key_expired"
	// MountStatusUnreachable mirrors teamrelay.ErrUnreachable: the relay could
	// not be asked at all (DNS/connection/timeout) — distinct from being asked
	// and refused.
	MountStatusUnreachable MountStatus = "unreachable"
	// MountStatusShareNotFound mirrors teamrelay.ErrNotFound: the share id
	// itself does not exist on the relay.
	MountStatusShareNotFound MountStatus = "share_not_found"
	// MountStatusError is any other failure — deliberately its own state rather
	// than folded into Unreachable, so an unclassified error is never silently
	// reported as one of the four named sentinels it isn't.
	MountStatusError MountStatus = "error"
)

// classifyMountError maps a files-index/download failure to the MountStatus a
// caller can show distinctly. Unrecognized errors fall to MountStatusError —
// never to MountStatusOK, and never silently folded into one of the four named
// sentinels they are not.
func classifyMountError(err error) MountStatus {
	switch {
	case errors.Is(err, teamrelay.ErrKeyRejected):
		return MountStatusKeyRejected
	case errors.Is(err, teamrelay.ErrKeyExpired):
		return MountStatusKeyExpired
	case errors.Is(err, teamrelay.ErrForeignShare):
		return MountStatusForeignShare
	case errors.Is(err, teamrelay.ErrUnreachable):
		return MountStatusUnreachable
	case errors.Is(err, teamrelay.ErrNotFound):
		return MountStatusShareNotFound
	default:
		return MountStatusError
	}
}

// MountResult reports what SyncMount did. A share materializing zero new
// documents and a share this key cannot reach both look like "nothing
// happened" from the tree alone — Status is what a caller renders instead of
// guessing from Mounted==0.
type MountResult struct {
	Status   MountStatus
	Mounted  int // rows created this run
	Skipped  int // files-index entries that already had a copy row
	Rejected int // files-index entries whose content was binary, not text — never mounted (see errBinaryFileEntry)
	Excluded int // files-index entries skipped because their first path segment is in the exclusion list
	Updated  int // existing copies whose source changed on the share — body rewritten this run
	Archived int // live copies whose source file is gone from the share — soft-deleted this run (see archiveVanishedCopies)
	Err      error
}

// errBinaryFileEntry marks a files-index entry SyncMount must count as
// Rejected and move past, never as a fatal MountStatusError. Without this
// distinction, one PNG in an otherwise-normal share would abort the loop in
// SyncMount before any later entry in sorted order was even looked at — the
// bug this guards against would trade "140 corrupt documents" for "zero
// documents mounted, whole sync appears broken" instead of just skipping the
// one file that cannot be a document body.
var errBinaryFileEntry = errors.New("teamrelay: file content is binary, not text — refusing to mount as a document")

// TeamRelayRefresher is what a document read calls to keep a Team Relay copy
// fresh before serving it. Optional on documentService, the same way watch is
// — a deployment with no Team Relay integration wired serves copies exactly as
// they were last synced.
type TeamRelayRefresher interface {
	// RefreshIfStale brings doc up to date against its source when its TTL has
	// elapsed, mutating doc.SourceSHA256/SyncedAt/Version in place and
	// rewriting the stored body ONLY when the source's hash has actually
	// changed. Inside the TTL it makes no network call at all — that silence
	// is the mechanism AC-3 is written around, not an optimization on top of a
	// simpler always-check design.
	RefreshIfStale(ctx context.Context, doc *domain.Document) error
}

// TeamRelayWriter pushes an edit made to a copy back to the original, R8.
//
// Optional in exactly the way TeamRelayRefresher is: unwired, a copy is
// read-only in practice — an edit to it is refused rather than silently kept
// local, because a copy that has drifted from its original is the state the
// refresher will later resolve by discarding the local side.
type TeamRelayWriter interface {
	// WriteBack sends body to the document's source, conditional on the
	// sha256 the copy was last synced at, and returns the source's new hash.
	//
	// It returns ErrExternalSourceConflict when the original moved since we
	// read it. That is not a failure to write — it is a refusal, and the
	// caller MUST treat it as one: nothing was written on either side, and the
	// only correct response is to re-read and rebuild the edit on the new
	// original.
	WriteBack(ctx context.Context, doc *domain.Document, body string) (newSHA256 string, err error)
}

// TeamRelayMountService materializes a Team Relay share as a Docs subtree and
// keeps individual copies fresh on open.
type TeamRelayMountService interface {
	TeamRelayRefresher
	TeamRelayWriter
	// SyncMount walks the project's configured share's files-index once and
	// creates a copy document for every entry that has no copy yet, and any
	// intermediate folder placeholders the mount point or the entries'
	// directories need. Already-mounted entries are left untouched — a re-run
	// only materializes what's new, which is what keeps this off the
	// per-tree-read path (§3.6): the expensive walk happens here, on an
	// explicit call, never implicitly inside a Docs tree GET.
	SyncMount(ctx context.Context, projectID uuid.UUID) (*MountResult, error)
	// SyncDueMounts is the scheduled counterpart of SyncMount: it walks every
	// enabled Team Relay integration and re-runs the mount for those whose
	// configured interval (TeamRelaySettings.SyncIntervalMinutes, default 15m)
	// has elapsed since their last sync. A project an explicit "Sync now" is
	// already syncing is skipped rather than waited on — the sweep must never
	// block on one slow share. Returns how many projects it synced and how
	// many it considered and left alone.
	SyncDueMounts(ctx context.Context) (synced, skipped int, err error)
	// RefreshSleepingKeyExpiries asks Team Relay's own introspection endpoint
	// for every enabled integration's key expiry, independent of any user
	// having opened a document or clicked "Sync now" — the two triggers
	// recordSyncOutcome already covers. Without this, an integration nobody
	// touches keeps key_expires_at NULL until the moment someone finally opens
	// it and the key has already died: the same "warning arrives with the
	// breakage, not before it" failure AC4 (#218d5847) was built to prevent,
	// just on a trigger that never fires (#bab2e6be). Meant to be called
	// periodically by a caller-owned scheduler, not on any request path.
	RefreshSleepingKeyExpiries(ctx context.Context) (checked, updated int, err error)
}

type teamRelayMountService struct {
	documentRepo repository.DocumentRepository
	storage      DocumentStore
	piService    ProjectIntegrationService
	piRepo       repository.ProjectIntegrationRepository
	client       RelaySyncClient
	keyDescriber KeyDescriber
	// attachmentRepo is the embed importer's persistence (A4). Nil in a
	// deployment that did not wire it: embeds are then skipped silently — an
	// optional collaborator in exactly the way piRepo is, for the same reason
	// (existing constructors and tests must keep compiling and behaving).
	attachmentRepo repository.DocumentAttachmentRepository
	// relayResolver and projectRepo resolve the relay URL per workspace through
	// the team-relay extension. Unwired, the URL comes from the process env, as
	// before the extension existed.
	relayResolver TeamRelayURLResolver
	projectRepo   repository.ProjectRepository

	// mountLocks carries one *sync.Mutex per project that has ever been synced.
	// It is what makes the two sync producers — a human's "Sync now" (SyncMount,
	// which waits) and the scheduler (SyncDueMounts, which tries and moves on) —
	// mutually exclusive per project without serializing unrelated projects on
	// one global lock. sync.Map because the key set is open-ended and every
	// operation on it is load-or-store.
	mountLocks sync.Map
	// syncMu guards nextSyncDue, the in-memory schedule. Nothing persistent: the
	// anchor for "when did this project last sync" is LastSyncCheckedAt in the
	// database (recordSyncOutcome), which survives restarts; this map only
	// carries the time FORWARD between sweeps so a restarted process doesn't
	// re-walk every share in its first minute.
	syncMu      sync.Mutex
	nextSyncDue map[uuid.UUID]time.Time
}

// TeamRelayMountServiceOption configures optional collaborators, the same
// pattern DocumentServiceOption uses.
type TeamRelayMountServiceOption func(*teamRelayMountService)

// WithRelaySyncClient overrides the real HTTP client — the seam the AC3
// counting-fake test and the AC4 sentinel tests both need.
func WithRelaySyncClient(c RelaySyncClient) TeamRelayMountServiceOption {
	return func(s *teamRelayMountService) { s.client = c }
}

// WithKeyDescriber overrides the real Team Relay agent-key introspection
// call — the seam #218d5847's AC4 producer tests need, same reasoning as
// WithRelaySyncClient.
func WithKeyDescriber(d KeyDescriber) TeamRelayMountServiceOption {
	return func(s *teamRelayMountService) { s.keyDescriber = d }
}

// WithAttachmentRepository wires the embed importer's attachment persistence.
// Without it the mount still works; image embeds in mounted bodies simply stay
// unresolved (what every deployment did before A4).
func WithAttachmentRepository(r repository.DocumentAttachmentRepository) TeamRelayMountServiceOption {
	return func(s *teamRelayMountService) { s.attachmentRepo = r }
}

// WithRelayURLResolver resolves the relay base URL per workspace (instance and
// workspace level settings) instead of reading the env var. projects maps a
// project to its workspace.
func WithRelayURLResolver(r TeamRelayURLResolver, projects repository.ProjectRepository) TeamRelayMountServiceOption {
	return func(s *teamRelayMountService) { s.relayResolver, s.projectRepo = r, projects }
}

// relayURLFor returns the relay base URL for the project's workspace.
func (s *teamRelayMountService) relayURLFor(ctx context.Context, projectID uuid.UUID) (string, error) {
	if s.relayResolver == nil {
		return "", errors.New("teamrelay: relay URL resolver not configured")
	}
	if s.projectRepo == nil {
		return "", errors.New("teamrelay: relay URL resolver wired without a project repository")
	}
	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("teamrelay: load project %s: %w", projectID, err)
	}
	if project == nil {
		return "", fmt.Errorf("teamrelay: project %s not found", projectID)
	}
	relayURL, _, ok := s.relayResolver.ResolveRelayURL(ctx, project.WorkspaceID)
	if !ok {
		return "", fmt.Errorf("teamrelay: relay URL not configured for workspace %s", project.WorkspaceID)
	}
	return relayURL, nil
}

// NewTeamRelayMountService returns a TeamRelayMountService. storage may be nil
// the same way documentService's can be — every call that needs it answers a
// clear "storage not configured" error rather than a nil-pointer panic. piRepo
// may also be nil (recordSyncOutcome no-ops without it) — kept optional
// rather than required so existing callers/tests that construct this service
// without touching R5-A's accounting columns at all keep compiling.
func NewTeamRelayMountService(
	documentRepo repository.DocumentRepository,
	storage DocumentStore,
	piService ProjectIntegrationService,
	piRepo repository.ProjectIntegrationRepository,
	opts ...TeamRelayMountServiceOption,
) TeamRelayMountService {
	s := &teamRelayMountService{
		documentRepo: documentRepo,
		storage:      storage,
		piService:    piService,
		piRepo:       piRepo,
		client:       realRelaySyncClient{},
		keyDescriber: realKeyDescriber{},
		nextSyncDue:  make(map[uuid.UUID]time.Time),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// relaySettings is what settingsFor resolves once per call: the project's
// parsed Team Relay settings plus the two things every relay call needs.
type relaySettings struct {
	settings domain.TeamRelaySettings
	agentKey string
	relayURL string
}

// settingsFor loads and parses the project's Team Relay integration. It
// returns apierror.NotFound (unwrapped) when there is no integration at all,
// so callers that treat "not configured" as an ordinary state (SyncMount) can
// tell it apart from a real failure (RefreshIfStale, where the caller already
// knows the document is a copy and "not configured" would be a data
// inconsistency worth logging).
func (s *teamRelayMountService) settingsFor(ctx context.Context, projectID uuid.UUID) (*relaySettings, error) {
	pi, err := s.piService.GetTeamRelay(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if !pi.Enabled || pi.AgentKey == "" {
		return nil, apierror.NotFound("TeamRelayIntegration")
	}
	var settings domain.TeamRelaySettings
	if jsonErr := json.Unmarshal(pi.Settings, &settings); jsonErr != nil {
		return nil, fmt.Errorf("teamrelay: parse settings for project %s: %w", projectID, jsonErr)
	}
	if settings.ShareID == "" {
		return nil, apierror.NotFound("TeamRelayIntegration")
	}
	relayURL, err := s.relayURLFor(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return &relaySettings{settings: settings, agentKey: pi.AgentKey, relayURL: relayURL}, nil
}

// syncTTL is settings.SyncTTLSeconds as a duration, or DefaultTeamRelaySyncTTL
// when unset. See TeamRelaySettings.SyncTTLSeconds for why R3 owns a default
// rather than requiring R5's UI to exist first.
func syncTTL(settings domain.TeamRelaySettings) time.Duration {
	if settings.SyncTTLSeconds <= 0 {
		return DefaultTeamRelaySyncTTL
	}
	return time.Duration(settings.SyncTTLSeconds) * time.Second
}

// recordSyncOutcome stamps R5-A's accounting columns
// (last_sync_checked_at/status/error, key_expires_at/source) after an actual
// attempt to reach Team Relay — the two producers #218d5847's AC4 named and
// never wired to anything:
//
//  1. (mandatory) the call's own outcome is always recorded, distinguishing
//     key_expired from a generic error rather than leaving §3.9's empty tree
//     unexplained.
//  2. (load-bearing, now that evc-team-relay#230/#bc11d499 is live) a
//     successful call also asks the key's own self-describe endpoint for its
//     real expires_at, so key_expiry_source="source" is a fact Team Relay
//     reports — not a date typed into a form ("manual", still supported by
//     the schema but deliberately NOT given a write path by this change; see
//     the commit message for why leaving it unbuilt was Bill's own accepted
//     minimal bar for this reopening, not a corner cut).
//
// Best-effort throughout: a failure to write these columns, or to reach the
// introspection endpoint, is logged and never propagates. By the time this
// runs, the caller's actual document refresh or mount walk has already
// succeeded or failed on its own terms — a bookkeeping hiccup on top of that
// must not turn a successful sync into a reported failure, or a real failure
// into two different errors racing to be returned.
func (s *teamRelayMountService) recordSyncOutcome(ctx context.Context, projectID uuid.UUID, rs *relaySettings, callErr error) {
	if s.piRepo == nil {
		return
	}

	status := "ok"
	errMsg := ""
	switch {
	case callErr == nil:
		// status/errMsg already "ok"/"".
	case errors.Is(callErr, teamrelay.ErrKeyExpired):
		status = "key_expired"
	default:
		status = "error"
		errMsg = callErr.Error()
	}
	if recErr := s.piRepo.RecordSyncCheck(ctx, projectID, "team_relay", timeNow(), status, errMsg); recErr != nil {
		log.Printf("teamrelay: record sync check for project %s: %v", projectID, recErr)
	}

	if status != "ok" {
		// A key that just came back expired (or any other failure) will not
		// usefully self-describe either — the same auth check gates both
		// routes on the Team Relay side. Skip the extra round trip rather
		// than log an identical failure twice.
		return
	}
	desc, descErr := s.keyDescriber.DescribeAgentKey(ctx, rs.relayURL, rs.settings.ShareID, rs.agentKey)
	if descErr != nil {
		log.Printf("teamrelay: describe agent key for project %s: %v", projectID, descErr)
		return
	}
	if setErr := s.piRepo.SetKeyExpiry(ctx, projectID, "team_relay", desc.ExpiresAt, "source"); setErr != nil {
		log.Printf("teamrelay: set key expiry for project %s: %v", projectID, setErr)
	}
}

// DefaultTeamRelaySyncInterval is how often the background mount sync re-runs
// SyncMount for a project whose settings don't name an interval. Fifteen
// minutes: often enough that a share edit appears in Mesh "by itself" within a
// coffee break, rare enough that a hundred sleeping projects don't turn into a
// walking-the-whole-share storm against a relay we don't own.
const DefaultTeamRelaySyncInterval = 15 * time.Minute

// MinTeamRelaySyncInterval is the floor a configured interval may not go
// below. SyncMount walks the whole share; anything tighter than five minutes
// is a loop that spends its life inside the walk. The upsert refuses lower
// values at write time — this constant is the one number both sides read.
const MinTeamRelaySyncInterval = 5 * time.Minute

// syncInterval is settings.SyncIntervalMinutes as a duration: nil → default,
// 0 → disabled (the caller checks for zero and skips), anything else → the
// configured value floored at MinTeamRelaySyncInterval (the floor is enforced
// at write time; the clamp here is defense for rows written by hand).
func syncInterval(settings domain.TeamRelaySettings) time.Duration {
	if settings.SyncIntervalMinutes == nil {
		return DefaultTeamRelaySyncInterval
	}
	d := time.Duration(*settings.SyncIntervalMinutes) * time.Minute
	if d == 0 {
		return 0
	}
	if d < MinTeamRelaySyncInterval {
		return MinTeamRelaySyncInterval
	}
	return d
}

// DefaultTeamRelayKeyExpiryRefreshInterval is how often
// RefreshSleepingKeyExpiries should be run by its caller. Keys live 90 days;
// once a day is far more than enough to give AC4's warning window (days, not
// hours) time to appear before anything breaks, and anything much more
// frequent is unjustified traffic against a service we don't own (#bab2e6be).
const DefaultTeamRelayKeyExpiryRefreshInterval = 24 * time.Hour

// RefreshSleepingKeyExpiries is documented on the TeamRelayMountService
// interface. It is best-effort PER INTEGRATION and deliberately narrower than
// recordSyncOutcome: unlike that function, a failure here — DescribeAgentKey
// returning teamrelay.ErrUnreachable or any other error — is logged and the
// integration is skipped, NEVER written to RecordSyncCheck. "Could not ask"
// must never read as "asked, and the key is bad" — that would turn every
// integration this one sweep happens not to reach into a false warning, which
// is worse than the NULL it already shows (the same lesson #218d5847 decided
// this task is closing a gap in, not repeating). Only a successful describe
// call writes anything, via SetKeyExpiry — the one column this task's AC
// needs populated without a user ever touching the integration.
//
// One malformed or misconfigured integration (bad settings JSON, no share ID,
// no key) does not stop the sweep for the rest — each is independent, so a
// single bad row degrades to "one integration stays NULL", never "nobody's
// key expiry refreshes today".
func (s *teamRelayMountService) RefreshSleepingKeyExpiries(ctx context.Context) (checked, updated int, err error) {
	if s.piRepo == nil {
		return 0, 0, nil
	}
	integrations, err := s.piRepo.ListEnabledByType(ctx, "team_relay")
	if err != nil {
		return 0, 0, fmt.Errorf("teamrelay: list enabled integrations: %w", err)
	}
	for _, pi := range integrations {
		checked++
		var settings domain.TeamRelaySettings
		if jsonErr := json.Unmarshal(pi.Settings, &settings); jsonErr != nil {
			log.Printf("teamrelay: refresh key expiry: parse settings for project %s: %v", pi.ProjectID, jsonErr)
			continue
		}
		if settings.ShareID == "" || pi.AgentKey == "" {
			continue
		}
		relayURL, urlErr := s.relayURLFor(ctx, pi.ProjectID)
		if urlErr != nil {
			log.Printf("teamrelay: refresh key expiry for project %s: %v", pi.ProjectID, urlErr)
			continue
		}
		desc, descErr := s.keyDescriber.DescribeAgentKey(ctx, relayURL, settings.ShareID, pi.AgentKey)
		if descErr != nil {
			log.Printf("teamrelay: refresh key expiry for project %s: %v", pi.ProjectID, descErr)
			continue
		}
		if setErr := s.piRepo.SetKeyExpiry(ctx, pi.ProjectID, "team_relay", desc.ExpiresAt, "source"); setErr != nil {
			log.Printf("teamrelay: refresh key expiry: set for project %s: %v", pi.ProjectID, setErr)
			continue
		}
		updated++
	}
	return checked, updated, nil
}

// RefreshIfStale is the AC-2/AC-3 mechanism: a document opened inside its TTL
// costs zero network calls; opened after the TTL has elapsed costs exactly
// one, and the body is rewritten only when that one call's hash differs from
// what's already stored.
func (s *teamRelayMountService) RefreshIfStale(ctx context.Context, doc *domain.Document) error {
	if doc == nil || doc.SourceKind != domain.DocumentSourceTeamRelay {
		return nil
	}
	if doc.SyncedAt == nil || doc.SourceShare == nil || doc.SourcePath == nil || doc.SourceSHA256 == nil {
		// The CHECK constraint (chk_documents_source_shape) makes this
		// unreachable in practice — a copy is never written without all four.
		// Treated as an error rather than a silent no-op: a copy claiming to
		// mirror a source it cannot describe is exactly the state that
		// constraint exists to rule out.
		return fmt.Errorf("teamrelay: copy %s is missing source metadata", doc.ID)
	}

	rs, err := s.settingsFor(ctx, doc.ProjectID)
	if err != nil {
		return err
	}

	now := timeNow()
	// The TTL gate: inside it, zero network calls. This branch is what the AC3
	// counter test asserts directly — a mutation that deletes it must turn a
	// same-run second open into a second network call.
	if now.Sub(*doc.SyncedAt) < syncTTL(rs.settings) {
		return nil
	}

	remote, err := s.client.Download(ctx, rs.relayURL, *doc.SourceShare, *doc.SourcePath, rs.agentKey)
	// #218d5847 AC4: every actual attempt to reach Team Relay — success or
	// failure — stamps the sync-check accounting columns, and a successful
	// one also refreshes key_expires_at from the relay's own introspection
	// endpoint. This is the ONLY place that used to make this exact call and
	// silently discard everything about the outcome except "did the copy
	// refresh" — RefreshIfStale fires on ordinary document opens, which is
	// what makes it the passive, no-explicit-action path the spec's "заранее"
	// requirement needs, unlike SyncMount's explicit button.
	s.recordSyncOutcome(ctx, doc.ProjectID, rs, err)
	if err != nil {
		return fmt.Errorf("teamrelay: refresh copy %s: %w", doc.ID, err)
	}

	// Freshness is decided by sha256, never by a timestamp from either side —
	// updated_at/UpdatedAt do not move on a same-content rewrite and cannot
	// order two writes inside one microsecond. This comparison is the one
	// place that would silently start doing the wrong thing if it were swapped
	// for remote.UpdatedAt instead — see the mutation control in
	// teamrelay_mount_service_test.go.
	if remote.SHA256 == *doc.SourceSHA256 {
		if _, stampErr := s.documentRepo.RefreshSyncedCopy(ctx, doc.ID, remote.SHA256, now, false); stampErr != nil {
			return fmt.Errorf("teamrelay: stamp synced_at for %s: %w", doc.ID, stampErr)
		}
		doc.SyncedAt = &now
		return nil
	}

	return s.rewriteCopyBody(ctx, doc, remote, rs, now)
}

// refreshChangedCopy downloads a copy's source and rewrites the body when it
// really differs — the index hash said so, the download is the authority.
func (s *teamRelayMountService) refreshChangedCopy(ctx context.Context, doc *domain.Document, sourcePath string, rs *relaySettings) error {
	remote, err := s.client.Download(ctx, rs.relayURL, rs.settings.ShareID, sourcePath, rs.agentKey)
	if err != nil {
		return err
	}
	now := timeNow()
	if doc.SourceSHA256 != nil && remote.SHA256 == *doc.SourceSHA256 {
		_, err = s.documentRepo.RefreshSyncedCopy(ctx, doc.ID, remote.SHA256, now, false)
		return err
	}
	return s.rewriteCopyBody(ctx, doc, remote, rs, now)
}

// rewriteCopyBody replaces a copy's stored body with remote's: the shared tail
// of RefreshIfStale (a document open found its source changed) and the scheduled
// sync (the files-index hash differs from the copy's). The caller has already
// established that remote.SHA256 differs from the stored one.
func (s *teamRelayMountService) rewriteCopyBody(ctx context.Context, doc *domain.Document, remote *teamrelay.SyncDocument, rs *relaySettings, now time.Time) error {
	if verr := rejectBinaryBody(remote.Content); verr != nil {
		// The source changed to something that cannot be a document body. Refuse
		// the refresh rather than overwrite a good, previously-synced body with
		// binary content — the existing copy stays exactly as it was, still
		// stamped at its old (still-valid) sha256, so the next open tries again.
		return fmt.Errorf("teamrelay: refresh copy %s: source is now binary, not text: %w", doc.ID, verr)
	}
	if s.storage == nil {
		return fmt.Errorf("teamrelay: storage not configured, cannot refresh copy %s", doc.ID)
	}
	if uploadErr := s.storage.Upload(ctx, doc.StorageKey, bytes.NewReader(remote.Content), int64(len(remote.Content)), documentContentType); uploadErr != nil {
		return fmt.Errorf("teamrelay: upload refreshed body for %s: %w", doc.ID, uploadErr)
	}
	newVersion, err := s.documentRepo.RefreshSyncedCopy(ctx, doc.ID, remote.SHA256, now, true)
	if err != nil {
		return fmt.Errorf("teamrelay: stamp refreshed copy %s: %w", doc.ID, err)
	}
	sha := remote.SHA256
	doc.SourceSHA256 = &sha
	doc.SyncedAt = &now
	doc.Version = newVersion
	doc.Body = string(remote.Content)
	// A4: the body changed, so its embeds are stale by construction. Best-effort
	// here too — the refresh already succeeded; an unreachable index must not
	// turn it into a reported failure, and an unmarked pass retries on the next
	// mount sync's backfill.
	if ierr := s.importEmbedsForRefreshedCopy(ctx, doc, remote.Content, remote.SHA256, rs); ierr != nil {
		log.Printf("teamrelay: refresh embeds for copy %s: %v", doc.ID, ierr)
	}
	return nil
}

// ErrExternalSourceConflict is what a refused write-back surfaces as: the
// original changed in Team Relay between our last read of it and this write.
//
// Wrapped rather than returned bare so a caller can errors.Is it while the log
// line still names the document and path involved.
var ErrExternalSourceConflict = errors.New("external source changed since it was read")

// WriteBack is the R8 mechanism: an edit to a copy is pushed to the original
// conditionally on the hash the copy was last synced at, so a concurrent edit
// on their side is detected and refused instead of overwritten.
//
// The precondition is source_sha256 — the hash of the version whose text the
// editor was actually shown. It is never a wildcard and never empty: the
// client refuses both locally, before any request leaves the process.
func (s *teamRelayMountService) WriteBack(ctx context.Context, doc *domain.Document, body string) (string, error) {
	if doc == nil || doc.SourceKind != domain.DocumentSourceTeamRelay {
		return "", fmt.Errorf("teamrelay: write-back called for a document that is not a Team Relay copy")
	}
	if doc.SourceShare == nil || doc.SourcePath == nil || doc.SourceSHA256 == nil || *doc.SourceSHA256 == "" {
		// Same reasoning as RefreshIfStale's equivalent guard: the CHECK
		// constraint makes this unreachable, and a copy that cannot say which
		// version it holds is precisely the row that must not be allowed to
		// write anywhere.
		return "", fmt.Errorf("teamrelay: copy %s is missing source metadata, refusing to write back", doc.ID)
	}

	rs, err := s.settingsFor(ctx, doc.ProjectID)
	if err != nil {
		return "", err
	}

	result, err := s.client.Write(ctx, rs.relayURL, *doc.SourceShare, *doc.SourcePath, rs.agentKey, *doc.SourceSHA256, []byte(body))
	if err != nil {
		if errors.Is(err, teamrelay.ErrSyncConflict) {
			// Deliberately NOT retried here, at any count. The rebuild this
			// needs is the user's — their edit was made against text that no
			// longer exists, and re-sending the same bytes with a refreshed
			// hash would resolve the conflict by discarding whatever the other
			// writer put there. That is the blind overwrite with an extra step.
			return "", fmt.Errorf("%w: copy %s (%s)", ErrExternalSourceConflict, doc.ID, *doc.SourcePath)
		}
		return "", fmt.Errorf("teamrelay: write back copy %s: %w", doc.ID, err)
	}
	return result.SHA256, nil
}

// SyncMount is called identically whether the share mounts at a configured
// subtree (settings.DocsMountPath non-empty) or at the project root (empty) —
// mountPathSegments returns nil for the empty case, and resolveOrCreateFolderPath
// treats a nil prefix exactly like any other empty segment list. There is no
// second branch here for the two modes; if there were, it would be here.
func (s *teamRelayMountService) SyncMount(ctx context.Context, projectID uuid.UUID) (*MountResult, error) {
	mu := s.projectMountLock(projectID)
	mu.Lock()
	defer mu.Unlock()
	return s.syncMountLocked(ctx, projectID), nil
}

// projectMountLock returns the per-project mutex, creating it on first use.
func (s *teamRelayMountService) projectMountLock(projectID uuid.UUID) *sync.Mutex {
	muAny, _ := s.mountLocks.LoadOrStore(projectID, &sync.Mutex{})
	return muAny.(*sync.Mutex)
}

// markSynced pushes the project's next scheduled sync one interval out.
func (s *teamRelayMountService) markSynced(projectID uuid.UUID, interval time.Duration) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	s.nextSyncDue[projectID] = timeNow().Add(interval)
}

// TeamRelaySyncSweepInterval is how often SyncDueMounts should be run by its
// caller. It is NOT the sync cadence — that is per-project
// (TeamRelaySettings.SyncIntervalMinutes) — it is how often the scheduler
// wakes up to check whose cadence has elapsed. A minute keeps the effective
// start of a due sync within a minute of its nominal time without the sweep
// itself being any kind of load.
const TeamRelaySyncSweepInterval = time.Minute

// SyncDueMounts is documented on the TeamRelayMountService interface.
//
// The "due" decision has two anchors. Cold start (no in-memory entry): the
// integration's LastSyncCheckedAt — the same column recordSyncOutcome already
// stamps on every FilesIndex — plus the interval; an integration that has
// NEVER been checked is due immediately, because an unmounted share is the
// state this whole schedule exists to end. Warm (an entry in nextSyncDue,
// written by any completed sync since process start): that timestamp. This is
// why a restart doesn't re-walk every share in its first minute: the DB anchor
// survives, only the forward-looking half of the schedule is volatile.
//
// Failures pace themselves: whatever SyncMount reports — a dead relay, an
// expired key — the project's next attempt is one full interval out, the same
// as a success. Retrying a failing share every minute would be a tight loop
// against a service we don't own; retrying it every 15 minutes is the same
// cadence a human repeatedly clicking "Sync now" would produce, which is the
// loudest behavior this mechanism is allowed to have.
func (s *teamRelayMountService) SyncDueMounts(ctx context.Context) (synced, skipped int, err error) {
	if s.piRepo == nil {
		return 0, 0, nil
	}
	integrations, err := s.piRepo.ListEnabledByType(ctx, "team_relay")
	if err != nil {
		return 0, 0, fmt.Errorf("teamrelay: list enabled integrations: %w", err)
	}
	now := timeNow()
	for _, pi := range integrations {
		var settings domain.TeamRelaySettings
		if jsonErr := json.Unmarshal(pi.Settings, &settings); jsonErr != nil {
			log.Printf("teamrelay: scheduled sync: parse settings for project %s: %v", pi.ProjectID, jsonErr)
			skipped++
			continue
		}
		if settings.ShareID == "" || pi.AgentKey == "" {
			skipped++
			continue
		}
		interval := syncInterval(settings)
		if interval == 0 {
			// Explicitly disabled: 0 is a setting, not an absence of one.
			skipped++
			continue
		}

		s.syncMu.Lock()
		due, hasDue := s.nextSyncDue[pi.ProjectID]
		s.syncMu.Unlock()
		if !hasDue {
			if pi.LastSyncCheckedAt != nil {
				due = pi.LastSyncCheckedAt.Add(interval)
			} else {
				// Never synced at all: due now. A share nobody ever mounted is
				// the state this schedule exists to end, not something to wait
				// one interval on.
				due = now
			}
		}
		if now.Before(due) {
			skipped++
			continue
		}

		mu := s.projectMountLock(pi.ProjectID)
		if !mu.TryLock() {
			// A "Sync now" (or a previous sweep tick's sync) is in flight;
			// when it finishes it writes nextSyncDue itself.
			skipped++
			continue
		}
		result := s.syncMountLocked(ctx, pi.ProjectID)
		mu.Unlock()
		synced++
		// syncMountLocked already pushed the schedule on success; a failed
		// attempt returns before it does, so pace failures here — same
		// interval, no tighter retry loop (see the doc comment above).
		s.markSynced(pi.ProjectID, interval)
		if result != nil && result.Status != MountStatusOK && result.Status != MountStatusNotConfigured {
			log.Printf("teamrelay: scheduled sync for project %s: status=%s err=%v", pi.ProjectID, resultStatus(result), result.Err)
		}
	}
	return synced, skipped, nil
}

// resultStatus renders a MountResult's status for logs without nil-handling
// at every call site.
func resultStatus(r *MountResult) MountStatus {
	if r == nil {
		return MountStatusError
	}
	return r.Status
}

// syncMountLocked is the lock-held core of SyncMount. Its errors travel inside
// MountResult (Err + a classified Status), never as a Go error — SyncMount is a
// report, not a fallible call, and every caller treats it that way.
func (s *teamRelayMountService) syncMountLocked(ctx context.Context, projectID uuid.UUID) *MountResult {
	rs, err := s.settingsFor(ctx, projectID)
	if err != nil {
		var apiErr *apierror.Error
		if errors.As(err, &apiErr) && apiErr.StatusCode() == 404 {
			return &MountResult{Status: MountStatusNotConfigured}
		}
		return &MountResult{Status: MountStatusError, Err: err}
	}

	// Pace the next scheduled attempt however this run ends: a failed manual
	// sync used to leave an overdue deadline behind, and the sweep then retried
	// the failing share every minute instead of one interval out.
	defer s.markSynced(projectID, syncInterval(rs.settings))

	entries, err := s.client.FilesIndex(ctx, rs.relayURL, rs.settings.ShareID, rs.agentKey)
	// Same accounting as RefreshIfStale's Download call — see that call site's
	// comment. SyncMount is the explicit "Sync now" action, so this is what
	// keeps the settings screen's freshness fact current for a project whose
	// documents nobody has opened recently enough to hit the passive path.
	s.recordSyncOutcome(ctx, projectID, rs, err)
	if err != nil {
		return &MountResult{Status: classifyMountError(err)}
	}

	// Stable order so a folder's placeholder is always resolved/created before
	// any file inside it is processed, and so re-runs behave identically.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

	excluded := effectiveExcludedPaths(rs.settings)
	mountPrefix := mountPathSegments(rs.settings.DocsMountPath)
	cache := map[string]*uuid.UUID{"": nil}
	// Built once per sync for the embed pass — every entry, including excluded
	// ones: an image under artifacts/ is not a document, but a body that embeds
	// it still resolves (see syncIndexByPath).
	indexByPath := syncIndexByPath(entries)
	result := &MountResult{Status: MountStatusOK}

	for _, entry := range entries {
		if isMountPathExcluded(entry.Path, excluded) {
			result.Excluded++
			continue
		}
		// An image/PDF entry is never a document body, and its extension says
		// so before a single byte is fetched. Counted Rejected exactly as the
		// content-based check inside createCopyDocument would have counted it
		// — but without the download that check needed, which matters now that
		// syncs run on a schedule: a share with a hundred screenshots was a
		// hundred binary rejections re-downloaded every interval, forever. The
		// bytes still reach Mesh as an embed attachment when a mounted body
		// references them — the one way an image belongs in Docs at all.
		// Checked BEFORE folder resolution: a folder holding only images
		// (an image-only card's artifacts folder) must not get an empty
		// placeholder at the project root. The folder appears when its first
		// non-image file is mounted.
		if teamrelay.IsEmbeddableFile(entry.Path) {
			result.Rejected++
			continue
		}

		dir, name := splitDirAndName(entry.Path)
		segments := append(append([]string{}, mountPrefix...), dirSegments(dir)...)

		parentID, ferr := s.resolveOrCreateFolderPath(ctx, projectID, segments, cache)
		if ferr != nil {
			return &MountResult{Status: MountStatusError, Mounted: result.Mounted, Err: ferr}
		}

		existing, gerr := s.documentRepo.GetBySourceInProject(ctx, projectID, rs.settings.ShareID, entry.Path)
		if gerr != nil {
			return &MountResult{Status: MountStatusError, Mounted: result.Mounted, Err: gerr}
		}

		if existing != nil {
			// A3: the scheduled sync is what keeps a mounted copy current when
			// nobody opens it, so an indexed hash that differs from the copy's
			// is refreshed here, not left for the next document open. Best-effort
			// per file, like the embed passes below: one unreadable file must not
			// abort the sweep over the rest of the share.
			if existing.SourceSHA256 != nil && *existing.SourceSHA256 != entry.SHA256 {
				if uerr := s.refreshChangedCopy(ctx, existing, entry.Path, rs); uerr != nil {
					log.Printf("teamrelay: refresh changed copy %s (%s): %v", existing.ID, entry.Path, uerr)
				} else {
					result.Updated++
					continue
				}
			}
			result.Skipped++
			// Embed maintenance (A4), two tiers. The full scan (backfillEmbeds)
			// runs only for a body whose embed pass never completed — mounted
			// before A4, or a pass that hit a transient failure. Everything
			// else gets the cheap currency pass, which reads the body's
			// ATTACHMENTS, never the body: an image can change while the body
			// that references it does not, so the body-hash marker alone can
			// never detect it (the exact gap the first version of this code
			// shipped with, caught by its own test). Best-effort both ways: the
			// mount already succeeded for this entry; an image must not undo it.
			if !s.embedsImported(existing, entry.SHA256) &&
				existing.SourceSHA256 != nil && *existing.SourceSHA256 == entry.SHA256 {
				if berr := s.backfillEmbeds(ctx, existing, entry.SHA256, rs, indexByPath); berr != nil {
					log.Printf("teamrelay: backfill embeds for copy %s (%s): %v", existing.ID, entry.Path, berr)
				}
			} else if s.attachmentRepo != nil {
				if rerr := s.refreshEmbedAttachments(ctx, existing, rs, indexByPath); rerr != nil {
					log.Printf("teamrelay: refresh embeds for copy %s (%s): %v", existing.ID, entry.Path, rerr)
				}
			}
			continue
		}

		if cerr := s.createCopyDocument(ctx, projectID, parentID, name, entry, rs, indexByPath); cerr != nil {
			if errors.Is(cerr, errBinaryFileEntry) {
				result.Rejected++
				continue
			}
			return &MountResult{Status: classifyMountError(cerr), Mounted: result.Mounted, Skipped: result.Skipped, Rejected: result.Rejected, Err: cerr}
		}
		result.Mounted++
	}

	if aerr := s.archiveVanishedCopies(ctx, projectID, rs, entries, excluded, result); aerr != nil {
		return &MountResult{
			Status: MountStatusError, Mounted: result.Mounted, Skipped: result.Skipped,
			Rejected: result.Rejected, Excluded: result.Excluded, Archived: result.Archived, Err: aerr,
		}
	}

	// A completed sync — manual or scheduled, this is the one body both go
	// through — is the project's sync for scheduling purposes: push the next
	// scheduled attempt one interval out so the sweep doesn't re-walk this
	// share a minute after a human just did.
	s.markSynced(projectID, syncInterval(rs.settings))

	return result
}

// archiveVanishedCopies soft-deletes every live copy of this share whose
// source file the files-index no longer represents — the file was deleted or
// moved on the share, or its folder became excluded. This is the A2 half of
// the mount contract: without it a moved file leaves its old copy behind
// forever, listed in Docs but backed by nothing.
//
// "Archive" is SoftDelete, the same штатный reversible mechanism a manual
// delete uses: the row and its stored body stay, the slug and the
// uq_documents_source slot free up, so a file that reappears at that path
// simply mounts fresh on the next run. There is deliberately no separate
// archive table — a copy that comes back should not arrive as a new document
// with a broken history.
//
// The empty-index guard is load-bearing, not defensive noise: a share whose
// files-index legitimately returns zero entries (an API change, a relay bug,
// a wrong-but-200 response) must not read as "every file was deleted". No
// index, no verdict — nothing is archived. FilesIndex errors never reach here
// (SyncMount returns before this runs), which is the other half of the same
// guard: an unreachable relay must not archive the project's Docs subtree.
func (s *teamRelayMountService) archiveVanishedCopies(
	ctx context.Context,
	projectID uuid.UUID,
	rs *relaySettings,
	entries []teamrelay.SyncIndexEntry,
	excluded []string,
	result *MountResult,
) error {
	if len(entries) == 0 {
		return nil
	}
	live, err := s.documentRepo.ListLiveCopiesByShare(ctx, projectID, rs.settings.ShareID)
	if err != nil {
		return err
	}
	indexed := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		indexed[e.Path] = struct{}{}
	}
	now := timeNow()
	for _, doc := range live {
		if doc.SourcePath == nil {
			continue
		}
		// A copy under a newly-excluded folder is as unmounted as one whose
		// file vanished: the exclusion list says this subtree must not be in
		// Docs, and a copy mounted before the exclusion was configured is a
		// zombie the setting was written to remove.
		_, inIndex := indexed[*doc.SourcePath]
		excl := isMountPathExcluded(*doc.SourcePath, excluded)
		// "Not in the index" is not "deleted": the index lists only
		// sync-artifact rows (see SyncIndexEntry), so a file that is still on
		// the share but no longer carries that source drops out of it. Only a
		// 404 on the download proves the file is gone; any other answer (the
		// body, or an error) keeps the copy.
		if !inIndex && !excl && !s.sourceIsGone(ctx, rs, *doc.SourcePath) {
			continue
		}
		if !inIndex || excl {
			if derr := s.documentRepo.SoftDelete(ctx, doc.ID, now, systemActorID, domain.ActorTypeSystem); derr != nil {
				return derr
			}
			result.Archived++
		}
	}
	return nil
}

// sourceIsGone reports whether the relay answers 404 for sourcePath. Anything
// else — a body, an unreachable relay, a rejected key — is "not proven gone".
func (s *teamRelayMountService) sourceIsGone(ctx context.Context, rs *relaySettings, sourcePath string) bool {
	_, err := s.client.Download(ctx, rs.relayURL, rs.settings.ShareID, sourcePath, rs.agentKey)
	return errors.Is(err, teamrelay.ErrNotFound)
}

// MaxTeamRelayEmbedBytes caps one embedded image/PDF download. The share's
// files-index names the size before the download, so an over-cap reference is
// skipped without a single byte fetched. Ten megabytes is generous for a
// screenshot and hostile for a "note" that is really a video with the wrong
// extension — and the cap exists because attachment storage is ours, billed
// and backed up, while the share is a foreign system we take at its word.
const MaxTeamRelayEmbedBytes = 10 << 20

// embedsImported reports whether doc's ONE-TIME full embed scan has already
// run for the body hash sourceSHA256 — the gate for backfillEmbeds only. It
// deliberately does NOT mean the doc's images are current: an image can change
// while the body hash stays put, which is what refreshEmbedAttachments exists
// to catch. Nil marker (a copy from before A4, or an import that hit a
// transient error) and a marker from another body hash both mean "scan".
func (s *teamRelayMountService) embedsImported(doc *domain.Document, sourceSHA256 string) bool {
	if s.attachmentRepo == nil {
		return true // embeds disabled here — treat as done, skip all the work
	}
	return doc.EmbedSyncedSHA256 != nil && *doc.EmbedSyncedSHA256 == sourceSHA256
}

// importEmbeds copies every image/PDF the body references into Mesh's
// attachment storage (A4). The body's own bytes are never touched: the share's
// markdown keeps its share-relative references, and the SERVE side rewrites
// them from the attachment records — rewriting here would be pushed back to
// the original by the next WriteBack and corrupt it.
//
// Per-reference failures are contained: a reference whose target is not on the
// files-index is a dead embed on the source side (nothing to do, not an
// error), and a permanently undownloadable one (over the size cap, sniffed as
// a non-image) is skipped without failing the pass. Only a TRANSIENT failure
// (download/upload/repo error) suppresses the marker, so the next sync retries
// it; everything else stamps documents.embed_synced_sha256 = sourceSHA256.
//
// The pass is best-effort from the mount's point of view on purpose: a share
// whose text mounts fine but whose image is unreachable must not lose its
// document over the image.
func (s *teamRelayMountService) importEmbeds(
	ctx context.Context,
	doc *domain.Document,
	body string,
	sourceSHA256 string,
	rs *relaySettings,
	indexByPath map[string]teamrelay.SyncIndexEntry,
) error {
	if s.attachmentRepo == nil || s.storage == nil || doc.SourcePath == nil {
		return nil
	}
	refs := teamrelay.FindImageRefs(body)
	transient := false
	done := make(map[string]bool, len(refs))
	docDir, _ := splitDirAndName(*doc.SourcePath)
	for _, ref := range refs {
		for _, cand := range embedTargetCandidates(ref.Target, docDir) {
			if done[cand] {
				break
			}
			entry, ok := indexByPath[cand]
			if !ok {
				continue // next candidate
			}
			done[cand] = true
			if err := s.syncEmbedAttachment(ctx, doc, cand, entry, rs); err != nil {
				log.Printf("teamrelay: import embed %q for copy %s: %v", cand, doc.ID, err)
				transient = true
			}
			break
		}
	}
	if transient {
		// Leave the marker unset: the next sync re-runs this pass and retries
		// what failed. Per-reference permanent skips (above) do NOT set this —
		// retrying them is free (size is known from the index) and silent.
		return nil
	}
	if err := s.documentRepo.SetEmbedSyncedSHA256(ctx, doc.ID, sourceSHA256); err != nil {
		return fmt.Errorf("teamrelay: stamp embed marker for %s: %w", doc.ID, err)
	}
	doc.EmbedSyncedSHA256 = &sourceSHA256
	return nil
}

// embedTargetCandidates lists the share paths a single embed reference may
// name, in resolution order, mirroring how the reference reads against the
// referencing document's own location:
//
//  1. an explicit "./" or "../" path is tried relative to the document,
//     then at the share root for compatibility with existing wiki embeds;
//  2. a target that already carries a directory ("Notes/pic.png") is taken as
//     share-relative, as written;
//  3. a bare name ("pic.png") is first tried next to the referencing document
//     ("Notes/pic.png" for a doc at "Notes/doc.md"), then at the share's root.
//
// The first candidate found on the files-index wins; a reference no candidate
// matches is a dead embed and resolves to nothing. Trailing/leading slashes and
// interior "./"/"../" are cleaned so a hand-typed path still matches the index's
// canonical spelling.
func embedTargetCandidates(target, docDir string) []string {
	cleaned := path.Clean("/" + strings.TrimPrefix(target, "/"))
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" || cleaned == "." {
		return nil
	}
	if strings.HasPrefix(target, "./") || strings.HasPrefix(target, "../") {
		relative := strings.TrimPrefix(path.Clean("/"+docDir+"/"+target), "/")
		if relative != cleaned {
			return []string{relative, cleaned}
		}
	}
	if strings.Contains(target, "/") {
		return []string{cleaned}
	}
	if docDir == "" {
		return []string{cleaned}
	}
	return []string{docDir + "/" + cleaned, cleaned}
}

// syncEmbedAttachment brings doc's attachment for share path sourcePath up to
// date with entry: skipped outright when the live attachment already matches
// the source hash (the common case after the first sync — no download at all),
// and replaced (old row soft-deleted, new bytes stored) when the source file
// changed. This is the per-reference dedup the partial unique index
// uq_document_attachments_source enforces structurally.
func (s *teamRelayMountService) syncEmbedAttachment(
	ctx context.Context,
	doc *domain.Document,
	sourcePath string,
	entry teamrelay.SyncIndexEntry,
	rs *relaySettings,
) error {
	existing, err := s.attachmentRepo.GetLiveBySourcePath(ctx, doc.ID, sourcePath)
	if err != nil {
		return err
	}
	if existing != nil && existing.SourceSHA256 != nil && *existing.SourceSHA256 == entry.SHA256 {
		return nil
	}
	if entry.Size > MaxTeamRelayEmbedBytes {
		log.Printf("teamrelay: embed %q for copy %s is %d bytes, over the %d cap — skipped", sourcePath, doc.ID, entry.Size, MaxTeamRelayEmbedBytes)
		return nil
	}

	remote, err := s.client.Download(ctx, rs.relayURL, *doc.SourceShare, sourcePath, rs.agentKey)
	if err != nil {
		return err
	}
	contentType := http.DetectContentType(remote.Content)
	if !isEmbeddableContentType(contentType) {
		log.Printf("teamrelay: embed %q for copy %s sniffs as %q, not an image or pdf — skipped", sourcePath, doc.ID, contentType)
		return nil
	}
	// The index claimed a hash; the download produced bytes. If they disagree
	// the index is stale about this file, and stamping the marker now would pin
	// an import the next sync believes is current. Treat it as transient.
	sum := sha256.Sum256(remote.Content)
	if hex.EncodeToString(sum[:]) != entry.SHA256 {
		return fmt.Errorf("downloaded content hash does not match files-index hash for %q", sourcePath)
	}

	// Every step that can fail for a transient reason runs while the OLD
	// attachment is still live: the new object is uploaded first. Soft-deleting
	// the old row before that left a window where an upload or insert failure
	// meant no live row at all, and the next sync — which enumerates live
	// attachments to learn a document's source paths — had nothing to retry, so
	// the image stayed broken until the body changed.
	id := uuid.New()
	name := path.Base(sourcePath)
	storageKey := documentAttachmentStorageKey(doc.ProjectID, doc.ID, id, name)
	if err := s.storage.Upload(ctx, storageKey, bytes.NewReader(remote.Content), int64(len(remote.Content)), contentType); err != nil {
		return err
	}
	sha := entry.SHA256
	att := &domain.DocumentAttachment{
		ID:             id,
		DocumentID:     doc.ID,
		Name:           name,
		MimeType:       contentType,
		SizeBytes:      int64(len(remote.Content)),
		StorageKey:     storageKey,
		UploadedBy:     systemActorID,
		UploadedByType: domain.ActorTypeSystem,
		CreatedAt:      timeNow(),
		SourcePath:     &sourcePath,
		SourceSHA256:   &sha,
	}
	// Retiring the old row and inserting the new one is one transaction, so a
	// failure leaves the OLD attachment live with its stale hash and the next
	// sync retries. The new object is orphaned on failure and removed here.
	var werr error
	if existing != nil {
		werr = s.attachmentRepo.Replace(ctx, existing.ID, att, timeNow())
	} else {
		werr = s.attachmentRepo.Create(ctx, att)
	}
	if werr != nil {
		_ = s.storage.Delete(ctx, storageKey)
		return werr
	}
	return nil
}

// isEmbeddableContentType is the byte-level half of the embed whitelist: what
// the extension CLAIMED is checked before the download, what the bytes actually
// are is checked after. http.DetectContentType reads only the first 512 bytes,
// so this is cheap for the common case and catches the renamed-executable case
// the extension check cannot.
func isEmbeddableContentType(ct string) bool {
	return strings.HasPrefix(ct, "image/") || ct == "application/pdf"
}

// importEmbedsForRefreshedCopy is the RefreshIfStale rewrite path's entry into
// the embed pass: the body just changed, so the marker is stale by
// construction. It fetches the files-index itself (RefreshIfStale normally
// downloads a single file, not the index) — one extra call per body REWRITE,
// never per freshness check.
func (s *teamRelayMountService) importEmbedsForRefreshedCopy(
	ctx context.Context,
	doc *domain.Document,
	body []byte,
	sourceSHA256 string,
	rs *relaySettings,
) error {
	if s.attachmentRepo == nil || s.storage == nil {
		return nil
	}
	entries, err := s.client.FilesIndex(ctx, rs.relayURL, rs.settings.ShareID, rs.agentKey)
	if err != nil {
		return err
	}
	indexByPath := syncIndexByPath(entries)
	return s.importEmbeds(ctx, doc, string(body), sourceSHA256, rs, indexByPath)
}

// syncIndexByPath is the lookup map the embed pass resolves candidates against.
// Built from every files-index entry — including ones the mount itself
// excludes: an image under artifacts/ is not mounted as a document, but a body
// that embeds it still gets its image.
func syncIndexByPath(entries []teamrelay.SyncIndexEntry) map[string]teamrelay.SyncIndexEntry {
	m := make(map[string]teamrelay.SyncIndexEntry, len(entries))
	for _, e := range entries {
		m[e.Path] = e
	}
	return m
}

// refreshEmbedAttachments is the per-sync currency pass for an already-scanned
// copy: it compares the document's LIVE imported attachments against the
// files-index — no body read, no regex, a handful of rows — and reconciles the
// differences:
//
//   - an attachment whose source file's hash changed is replaced
//     (syncEmbedAttachment soft-deletes the old row);
//   - an attachment whose source file is GONE from the share is soft-deleted —
//     the same liveness rule archiveVanishedCopies applies to the documents
//     themselves, for the same reason: serving an image the source no longer
//     has is serving a stale copy of something its owner believes they deleted.
//
// New embeds in an unchanged body cannot appear here — the body did not
// change, so its reference set did not either — which is exactly why this pass
// does not need the body.
func (s *teamRelayMountService) refreshEmbedAttachments(
	ctx context.Context,
	doc *domain.Document,
	rs *relaySettings,
	indexByPath map[string]teamrelay.SyncIndexEntry,
) error {
	atts, err := s.attachmentRepo.ListLiveByDocument(ctx, doc.ID)
	if err != nil {
		return err
	}
	if len(atts) == 0 {
		return nil
	}
	for _, att := range atts {
		if att.SourcePath == nil {
			continue // a human upload; not ours to reconcile
		}
		entry, ok := indexByPath[*att.SourcePath]
		if !ok {
			if derr := s.attachmentRepo.SoftDelete(ctx, att.ID, timeNow()); derr != nil {
				return derr
			}
			log.Printf("teamrelay: embed %q for copy %s vanished from the share — attachment archived", *att.SourcePath, doc.ID)
			continue
		}
		att := att
		if serr := s.syncEmbedAttachment(ctx, doc, *att.SourcePath, entry, rs); serr != nil {
			return serr
		}
	}
	return nil
}

// backfillEmbeds is the SyncMount skipped-branch entry into the embed pass: a
// copy that already exists but has no marker (mounted before A4) or a marker
// from a different body hash. Runs only when the stored body is CURRENT
// (doc.SourceSHA256 == sourceSHA256) — a body the refresher has not caught up
// with yet gets its embeds imported when it is actually refreshed, against the
// body it will actually have.
func (s *teamRelayMountService) backfillEmbeds(
	ctx context.Context,
	doc *domain.Document,
	sourceSHA256 string,
	rs *relaySettings,
	indexByPath map[string]teamrelay.SyncIndexEntry,
) error {
	if s.attachmentRepo == nil || s.storage == nil {
		return nil
	}
	rc, err := s.storage.Download(ctx, doc.StorageKey)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	if err != nil {
		return err
	}
	return s.importEmbeds(ctx, doc, string(body), sourceSHA256, rs, indexByPath)
}

// resolveOrCreateFolderPath walks segments from the project's top level,
// creating an empty 'own' placeholder document for any segment that does not
// exist yet, and returns the id of the last one (nil for an empty path — the
// project root itself, which is exactly what an entry at the share's own
// top level resolves to when DocsMountPath is also empty).
//
// This is the ONE function both mount modes go through (AC-5): the caller
// builds segments as DocsMountPath's own segments followed by the entry's
// directory segments, and DocsMountPath being empty simply means the first
// part of that list is empty. Nothing downstream of this call can tell which
// mode produced its segments — that is the point.
func (s *teamRelayMountService) resolveOrCreateFolderPath(
	ctx context.Context,
	projectID uuid.UUID,
	segments []string,
	cache map[string]*uuid.UUID,
) (*uuid.UUID, error) {
	parentID := cache[""]
	if len(segments) == 0 {
		return parentID, nil
	}

	walked := make([]string, 0, len(segments))
	// Once a segment resolves through a moved placeholder, the rest of the share
	// path lives under that placeholder's CURRENT path, not under the share's own
	// path, so deeper lookups walk from realBase instead of from the top level.
	var realBase []string
	baseLen := 0
	for _, raw := range segments {
		slug := folderSlug(raw)
		walked = append(walked, slug)
		key := strings.Join(walked, "/")

		if cached, ok := cache[key]; ok {
			parentID = cached
			if movedDoc, wasMoved := cache[movedCacheKey(key)]; wasMoved {
				// A cache hit must not forget that this folder was found through its alias.
				base, perr := s.livePath(ctx, *movedDoc)
				if perr != nil {
					return nil, perr
				}
				realBase, baseLen = base, len(walked)
			}
			continue
		}

		lookup := walked
		if realBase != nil {
			lookup = append(append([]string{}, realBase...), walked[baseLen:]...)
		}
		doc, depth, err := s.documentRepo.GetByPathInProject(ctx, projectID, lookup)
		if err != nil {
			return nil, err
		}
		if doc != nil && depth == len(lookup) {
			cache[key] = &doc.ID
			parentID = &doc.ID
			continue
		}

		// A placeholder someone filed away (the docs layout moves root documents
		// into canon folders) is no longer at this path, but its old path is
		// recorded as an alias. It is still this share folder: use it, or every
		// scheduled sync leaves a new empty duplicate at the top level.
		moved, aerr := s.documentRepo.GetByPathAlias(ctx, projectID, key)
		if aerr != nil {
			return nil, aerr
		}
		if moved != nil {
			base, perr := s.livePath(ctx, moved.ID)
			if perr != nil {
				return nil, perr
			}
			realBase, baseLen = base, len(walked)
			cache[key] = &moved.ID
			cache[movedCacheKey(key)] = &moved.ID
			parentID = &moved.ID
			continue
		}

		newID, cerr := s.createFolderPlaceholder(ctx, projectID, parentID, slug, raw)
		if cerr != nil {
			return nil, cerr
		}
		cache[key] = newID
		parentID = newID
	}
	return parentID, nil
}

// movedCacheKey marks, in the folder cache, a folder that was found through its
// alias rather than at its share path.
func movedCacheKey(key string) string { return "\x00moved\x00" + key }

// livePath returns the current slug path of the document id, walking its
// parents up to the top level.
func (s *teamRelayMountService) livePath(ctx context.Context, id uuid.UUID) ([]string, error) {
	var slugs []string
	for cur := &id; cur != nil; {
		doc, err := s.documentRepo.GetByID(ctx, *cur)
		if err != nil {
			return nil, err
		}
		if doc == nil || len(slugs) > 64 {
			return nil, fmt.Errorf("teamrelay: cannot resolve the current path of folder %s", id)
		}
		slugs = append([]string{doc.Slug}, slugs...)
		cur = doc.ParentID
	}
	return slugs, nil
}

// createFolderPlaceholder makes an empty 'own' document that exists only to
// hold a subtree — a directory in the share has no files-index entry of its
// own (files-index lists only sync-artifact files, see SyncIndexEntry's doc
// comment), so nothing else will ever materialize this node.
func (s *teamRelayMountService) createFolderPlaceholder(ctx context.Context, projectID uuid.UUID, parentID *uuid.UUID, slug, title string) (*uuid.UUID, error) {
	if s.storage == nil {
		return nil, fmt.Errorf("teamrelay: storage not configured, cannot mount folder %q", title)
	}
	id := uuid.New()
	storageKey := documentStorageKey(projectID, id)
	if err := s.storage.Upload(ctx, storageKey, strings.NewReader(""), 0, documentContentType); err != nil {
		return nil, fmt.Errorf("teamrelay: upload placeholder for %q: %w", title, err)
	}
	now := timeNow()
	doc := &domain.Document{
		ID:            id,
		ProjectID:     projectID,
		ParentID:      parentID,
		Slug:          slug,
		Title:         title,
		StorageKey:    storageKey,
		Position:      0,
		Version:       1,
		CreatedBy:     systemActorID,
		CreatedByType: domain.ActorTypeSystem,
		UpdatedBy:     &systemActorID,
		UpdatedByType: actorTypeSystemPtr(),
		CreatedAt:     now,
		UpdatedAt:     now,
		// Explicit rather than relying on the DB's DEFAULT 'own' (which is what
		// Create's own INSERT list depends on — see DocumentRepo.Create): the
		// object this function hands back is used immediately, and an explicit
		// value is correct in every caller, mock included, without needing a
		// round-trip re-read to see what the database defaulted it to.
		SourceKind: domain.DocumentSourceOwn,
	}
	if err := s.documentRepo.Create(ctx, doc); err != nil {
		_ = s.storage.Delete(ctx, storageKey)
		return nil, fmt.Errorf("teamrelay: create folder placeholder %q: %w", title, err)
	}
	return &doc.ID, nil
}

// createCopyDocument downloads one files-index entry's body and stores it as
// a Team Relay copy document — the actual, necessary "walk the whole share"
// cost, paid once per file the first time SyncMount sees it (idempotent
// re-runs skip anything GetBySourceInProject already finds), never again on a
// plain tree read.
func (s *teamRelayMountService) createCopyDocument(ctx context.Context, projectID uuid.UUID, parentID *uuid.UUID, name string, entry teamrelay.SyncIndexEntry, rs *relaySettings, indexByPath map[string]teamrelay.SyncIndexEntry) error {
	if s.storage == nil {
		return fmt.Errorf("teamrelay: storage not configured, cannot mount %q", entry.Path)
	}

	remote, err := s.client.Download(ctx, rs.relayURL, rs.settings.ShareID, entry.Path, rs.agentKey)
	if err != nil {
		return fmt.Errorf("teamrelay: download %q: %w", entry.Path, err)
	}
	if verr := rejectBinaryBody(remote.Content); verr != nil {
		log.Printf("teamrelay: skipping %q — %v", entry.Path, verr)
		return errBinaryFileEntry
	}

	title := titleFromFileName(name)
	slug := mdoc.Slugify(title)
	id := uuid.New()
	if !hasLetterOrDigit(slug) {
		slug = mdoc.Slugify(name)
	}
	if !hasLetterOrDigit(slug) {
		slug = "doc-" + id.String()[:8]
	}

	storageKey := documentStorageKey(projectID, id)
	err = s.storage.Upload(ctx, storageKey, bytes.NewReader(remote.Content), int64(len(remote.Content)), documentContentType)
	if err != nil {
		return fmt.Errorf("teamrelay: upload body for %q: %w", entry.Path, err)
	}

	now := timeNow()
	shareID := rs.settings.ShareID
	sourcePath := entry.Path
	sha := remote.SHA256
	doc := &domain.Document{
		ID:            id,
		ProjectID:     projectID,
		ParentID:      parentID,
		Slug:          slug,
		Title:         title,
		StorageKey:    storageKey,
		Position:      0,
		Version:       1,
		CreatedBy:     systemActorID,
		CreatedByType: domain.ActorTypeSystem,
		UpdatedBy:     &systemActorID,
		UpdatedByType: actorTypeSystemPtr(),
		CreatedAt:     now,
		UpdatedAt:     now,

		SourceKind:   domain.DocumentSourceTeamRelay,
		SourceShare:  &shareID,
		SourcePath:   &sourcePath,
		SourceSHA256: &sha,
		SyncedAt:     &now,
	}
	err = s.documentRepo.CreateExternalCopy(ctx, doc)
	if isSlugTaken(err) {
		// Two share files can slugify alike (ISSUES.md / issues.md, a_b / a-b),
		// and a live document made by hand can hold the slug too. One such file
		// must not abort the sweep over the whole share, so the copy takes a
		// slug made unique by its own id.
		doc.Slug = slug + "-" + id.String()[:8]
		log.Printf("teamrelay: slug %q taken for %q, mounting as %q", slug, entry.Path, doc.Slug)
		err = s.documentRepo.CreateExternalCopy(ctx, doc)
	}
	if err != nil {
		_ = s.storage.Delete(ctx, storageKey)
		return fmt.Errorf("teamrelay: create copy for %q: %w", entry.Path, err)
	}
	// A4: import the body's image embeds now that the copy exists. Best-effort
	// by design — a copy that mounted is worth more than the image beside it,
	// and an unmarked pass (importEmbeds's transient path) retries next sync.
	if ierr := s.importEmbeds(ctx, doc, string(remote.Content), remote.SHA256, rs, indexByPath); ierr != nil {
		log.Printf("teamrelay: import embeds for new copy %q: %v", entry.Path, ierr)
	}
	return nil
}

// isSlugTaken reports the repository's 409 for a slug held by a live sibling.
func isSlugTaken(err error) bool {
	var ae *apierror.Error
	return errors.As(err, &ae) && ae.ErrorCode == apierror.CodeDocumentSlugTaken
}

// actorTypeSystemPtr is a small allocation helper: domain.ActorTypeSystem is a
// typed constant, and UpdatedByType wants a *domain.ActorType, so this is the
// one place that takes its address instead of every call site declaring its
// own local variable to do it.
func actorTypeSystemPtr() *domain.ActorType {
	t := domain.ActorTypeSystem
	return &t
}

// mountPathSegments turns TeamRelaySettings.DocsMountPath into the segment
// list resolveOrCreateFolderPath walks. Empty in, nil out — which is exactly
// the "mount at project root" mode, with no separate flag for it.
func mountPathSegments(mountPath string) []string {
	mp := strings.Trim(mountPath, "/")
	if mp == "" {
		return nil
	}
	return strings.Split(mp, "/")
}

// splitDirAndName takes a files-index path ("Notes/Sub/Welcome.md") apart into
// its directory ("Notes/Sub") and file name ("Welcome.md"). A top-level file
// ("Welcome.md") yields an empty directory.
func splitDirAndName(p string) (dir, name string) {
	d, n := path.Split(p)
	return strings.TrimSuffix(d, "/"), n
}

// dirSegments splits a directory string into path segments, or nil for the
// share's own top level.
func dirSegments(dir string) []string {
	if dir == "" {
		return nil
	}
	return strings.Split(dir, "/")
}

// titleFromFileName strips a file's extension for use as a document title —
// "Welcome.md" becomes "Welcome". A name with no extension is used as-is.
func titleFromFileName(name string) string {
	ext := path.Ext(name)
	if ext == "" {
		return name
	}
	return strings.TrimSuffix(name, ext)
}

// folderSlug is mdoc.Slugify with the same non-degenerate fallback
// document_service.go's Create uses (hasLetterOrDigit) — a folder named only
// in Cyrillic must slugify to something distinguishable from a sibling folder
// with a different Cyrillic name (see §3.5 in the spec doc: the naive
// ASCII-only slugifier collapses distinct Russian titles to the same "-").
func folderSlug(raw string) string {
	slug := mdoc.Slugify(raw)
	if hasLetterOrDigit(slug) {
		return slug
	}
	// A directory name with no letter/digit in any script — rare, but the same
	// shape Create's own fallback handles. A stable per-name digest keeps two
	// differently-punctuated folders from colliding onto the same slug.
	//
	// Hashed rather than hex-of-the-raw-bytes: the raw form is only as long as
	// the name, so a short name like "-" or "..." produced fewer than the 16
	// hex characters this truncates to and PANICKED with a slice-bounds error,
	// taking down the whole mount for one oddly-named folder. A digest is
	// always 64 characters wide regardless of input length.
	sum := sha256.Sum256([]byte(raw))
	return "folder-" + hex.EncodeToString(sum[:])[:16]
}
