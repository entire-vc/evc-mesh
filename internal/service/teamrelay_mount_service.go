package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/integration/teamrelay"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/mdoc"
)

// teamRelayRelayURLEnvVar is where the relay's base URL comes from — the same
// variable tr_search_handler.go and tr_document_handler.go already read via
// os.Getenv, kept here as one named constant instead of a third copy of the
// literal string.
const teamRelayRelayURLEnvVar = "MESH_TEAMRELAY_RELAY_URL"

// DefaultTeamRelaySyncTTL is the freshness window used when a project's Team
// Relay settings don't name one. R5 is what lets a human change it per
// project; until it exists, this is the TTL every mounted share gets.
const DefaultTeamRelaySyncTTL = 5 * time.Minute

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
	Status  MountStatus
	Mounted int // rows created this run
	Skipped int // files-index entries that already had a copy row
	Err     error
}

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
}

type teamRelayMountService struct {
	documentRepo repository.DocumentRepository
	storage      DocumentStore
	piService    ProjectIntegrationService
	piRepo       repository.ProjectIntegrationRepository
	client       RelaySyncClient
	keyDescriber KeyDescriber
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
	relayURL := os.Getenv(teamRelayRelayURLEnvVar)
	if relayURL == "" {
		return nil, fmt.Errorf("teamrelay: %s not configured", teamRelayRelayURLEnvVar)
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
	rs, err := s.settingsFor(ctx, projectID)
	if err != nil {
		var apiErr *apierror.Error
		if errors.As(err, &apiErr) && apiErr.StatusCode() == 404 {
			return &MountResult{Status: MountStatusNotConfigured}, nil
		}
		return &MountResult{Status: MountStatusError, Err: err}, nil
	}

	entries, err := s.client.FilesIndex(ctx, rs.relayURL, rs.settings.ShareID, rs.agentKey)
	// Same accounting as RefreshIfStale's Download call — see that call site's
	// comment. SyncMount is the explicit "Sync now" action, so this is what
	// keeps the settings screen's freshness fact current for a project whose
	// documents nobody has opened recently enough to hit the passive path.
	s.recordSyncOutcome(ctx, projectID, rs, err)
	if err != nil {
		return &MountResult{Status: classifyMountError(err)}, nil
	}

	// Stable order so a folder's placeholder is always resolved/created before
	// any file inside it is processed, and so re-runs behave identically.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

	mountPrefix := mountPathSegments(rs.settings.DocsMountPath)
	cache := map[string]*uuid.UUID{"": nil}
	result := &MountResult{Status: MountStatusOK}

	for _, entry := range entries {
		dir, name := splitDirAndName(entry.Path)
		segments := append(append([]string{}, mountPrefix...), dirSegments(dir)...)

		parentID, ferr := s.resolveOrCreateFolderPath(ctx, projectID, segments, cache)
		if ferr != nil {
			return &MountResult{Status: MountStatusError, Mounted: result.Mounted, Err: ferr}, nil
		}

		existing, gerr := s.documentRepo.GetBySourceInProject(ctx, projectID, rs.settings.ShareID, entry.Path)
		if gerr != nil {
			return &MountResult{Status: MountStatusError, Mounted: result.Mounted, Err: gerr}, nil
		}
		if existing != nil {
			result.Skipped++
			continue
		}

		if cerr := s.createCopyDocument(ctx, projectID, parentID, name, entry, rs); cerr != nil {
			return &MountResult{Status: classifyMountError(cerr), Mounted: result.Mounted, Err: cerr}, nil
		}
		result.Mounted++
	}

	return result, nil
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
	for _, raw := range segments {
		slug := folderSlug(raw)
		walked = append(walked, slug)
		key := strings.Join(walked, "/")

		if cached, ok := cache[key]; ok {
			parentID = cached
			continue
		}

		doc, depth, err := s.documentRepo.GetByPathInProject(ctx, projectID, walked)
		if err != nil {
			return nil, err
		}
		if doc != nil && depth == len(walked) {
			cache[key] = &doc.ID
			parentID = &doc.ID
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
func (s *teamRelayMountService) createCopyDocument(ctx context.Context, projectID uuid.UUID, parentID *uuid.UUID, name string, entry teamrelay.SyncIndexEntry, rs *relaySettings) error {
	if s.storage == nil {
		return fmt.Errorf("teamrelay: storage not configured, cannot mount %q", entry.Path)
	}

	remote, err := s.client.Download(ctx, rs.relayURL, rs.settings.ShareID, entry.Path, rs.agentKey)
	if err != nil {
		return fmt.Errorf("teamrelay: download %q: %w", entry.Path, err)
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
	if err := s.storage.Upload(ctx, storageKey, bytes.NewReader(remote.Content), int64(len(remote.Content)), documentContentType); err != nil {
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
	if err := s.documentRepo.CreateExternalCopy(ctx, doc); err != nil {
		_ = s.storage.Delete(ctx, storageKey)
		return fmt.Errorf("teamrelay: create copy for %q: %w", entry.Path, err)
	}
	return nil
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
