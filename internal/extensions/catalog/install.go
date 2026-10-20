package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

const (
	maxDetailBytes   = 4 << 20
	maxArtifactBytes = 64 << 20
	maxManifestBytes = 1 << 20
	maxUnpackedBytes = 128 << 20
	installsTimeout  = 5 * time.Second
	// installsMinGap is the throttle between two anonymous install pings.
	installsMinGap = time.Second
)

// Error codes the install handler maps to answers.
const (
	CodeNotInCatalog = "extension_not_in_catalog"
	CodeBlocked      = "extension_blocked"
	CodeDigest       = "extension_artifact_digest_mismatch"
	CodeSparkDown    = "extension_catalog_unavailable"
)

func apiErr(status int, code, msg string) *apierror.Error {
	return &apierror.Error{Code: status, ErrorCode: code, Message: msg}
}

// InstallStore is the slice of the installed-extensions store the installer uses.
type InstallStore interface {
	Get(ctx context.Context, extensionID string) (*domain.InstalledExtension, error)
	List(ctx context.Context) ([]domain.InstalledExtension, error)
	InsertCatalog(ctx context.Context, ext *domain.InstalledExtension) error
	// UpdateCatalog moves a source=catalog row to a new release, keeping the
	// old version in previous_version.
	UpdateCatalog(ctx context.Context, ext *domain.InstalledExtension) error
}

// Installer installs an extension from the verified catalog: the trust anchor
// is the signed catalog entry (version, commit, artifact digest); the Spark
// detail page only supplies the manifest, and the artifact bytes must hash to
// the signed digest.
type Installer struct {
	Catalog  *Client
	SparkURL func() string
	// CounterOn reports whether anonymous install counting is enabled. Nil = on.
	CounterOn func() bool
	HTTP      *http.Client
	Store     InstallStore
	Journal   Journal
	OnChange  func(ctx context.Context)

	pingMu   sync.Mutex
	lastPing time.Time
}

type detailRelease struct {
	Version     string          `json:"version"`
	Manifest    json.RawMessage `json:"manifest"`
	Installable bool            `json:"installable"`
	Status      string          `json:"status"`
	Yanked      bool            `json:"yanked"`
}

// Detail is GET /mesh-extensions/{slug}: what the extension page shows.
type Detail struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Readme      string          `json:"readme"`
	Installs    int             `json:"installs"`
	Releases    []detailRelease `json:"releases"`
}

func (in *Installer) httpc() *http.Client {
	if in.HTTP != nil {
		return in.HTTP
	}
	return &http.Client{Timeout: fetchTimeout}
}

func (in *Installer) base() (string, error) {
	b := strings.TrimRight(strings.TrimSpace(in.SparkURL()), "/")
	if b == "" {
		return "", apiErr(http.StatusServiceUnavailable, CodeSparkDown, "Spark catalog is not configured")
	}
	return b, nil
}

func (in *Installer) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := in.httpc().Do(req)
	if err != nil {
		return nil, apiErr(http.StatusBadGateway, CodeSparkDown, "Spark is unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, apiErr(http.StatusNotFound, CodeNotInCatalog, "Spark has no such extension or release")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(http.StatusBadGateway, CodeSparkDown, fmt.Sprintf("Spark answered %d", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, apiErr(http.StatusBadGateway, CodeSparkDown, "Spark response was cut")
	}
	if int64(len(body)) > limit {
		return nil, apiErr(http.StatusBadGateway, CodeSparkDown, "Spark response is too large")
	}
	return body, nil
}

// Detail reads the extension page data (README, versions, checks).
func (in *Installer) Detail(ctx context.Context, id string) (*Detail, error) {
	base, err := in.base()
	if err != nil {
		return nil, err
	}
	body, err := in.get(ctx, base+"/api/v1/mesh-extensions/"+url.PathEscape(id), maxDetailBytes)
	if err != nil {
		return nil, err
	}
	var d Detail
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, apiErr(http.StatusBadGateway, CodeSparkDown, "Spark detail is not valid JSON")
	}
	return &d, nil
}

// Install installs catalog extension id at its latest compatible version.
func (in *Installer) Install(ctx context.Context, actor uuid.UUID, id string) (*domain.InstalledExtension, error) {
	cat, err := in.Catalog.Current()
	if err != nil {
		return nil, apiErr(http.StatusServiceUnavailable, CodeSparkDown, "the catalog has not been verified yet")
	}
	e, ok := cat.Entry(id)
	if !ok {
		return nil, apiErr(http.StatusNotFound, CodeNotInCatalog, "extension is not in the catalog")
	}
	rel := e.Latest
	if b, hit := cat.Blocks(id, rel.Version); hit {
		return nil, apiErr(http.StatusConflict, CodeBlocked, "blocked by the catalog: "+b.Reason)
	}
	if e.Runtime == "builtin" {
		return nil, apiErr(http.StatusConflict, repoinstall.CodeBuiltinRuntime, "this extension ships inside Mesh; nothing to install")
	}
	if rel.ArtifactSHA256 == nil || *rel.ArtifactSHA256 == "" {
		return nil, apiErr(http.StatusUnprocessableEntity, CodeDigest, "the catalog gives no artifact digest for this version")
	}
	if !repoinstall.MeshAPICompatible(rel.MeshAPI, extensions.ExtensionsAPIVersion) {
		return nil, apiErr(http.StatusUnprocessableEntity, repoinstall.CodeIncompatible, fmt.Sprintf("needs Mesh API %s", rel.MeshAPI))
	}
	if existing, gerr := in.Store.Get(ctx, id); gerr != nil {
		return nil, gerr
	} else if existing != nil {
		return nil, apiErr(http.StatusConflict, repoinstall.CodeIDConflict, fmt.Sprintf("an extension with id %q is already installed", id))
	}

	m, err := in.fetchVerified(ctx, e)
	if err != nil {
		return nil, err
	}

	row := &domain.InstalledExtension{
		ExtensionID: id, Version: rel.Version, Manifest: m.Raw, Source: domain.ExtensionSourceCatalog,
		CommitSHA: rel.CommitSHA, ArtifactSHA256: *rel.ArtifactSHA256, TrustLevel: domain.ExtensionTrustLevel(e.Trust), InstalledBy: &actor,
	}
	if err := in.Store.InsertCatalog(ctx, row); err != nil {
		return nil, err
	}
	if in.Journal != nil {
		details, _ := json.Marshal(map[string]any{"version": rel.Version, "commit_sha": rel.CommitSHA, "artifact_sha256": *rel.ArtifactSHA256, "trust": e.Trust, "source": "catalog"})
		if jerr := in.Journal.AppendJournal(ctx, &domain.InstanceExtensionJournalEntry{ActorID: &actor, ExtensionID: id, Action: domain.InstanceJournalInstalled, Details: details}); jerr != nil {
			log.Printf("[extensions] journal installed %s: %v", id, jerr)
		}
	}
	if in.OnChange != nil {
		in.OnChange(ctx)
	}
	go in.countInstall(id)
	return in.Store.Get(ctx, id)
}

// countInstall tells Spark one more instance installed id: anonymous (no body,
// no identity), throttled to one ping per second, off when the instance
// setting says so. A failure is only logged.
func (in *Installer) countInstall(id string) {
	if in.CounterOn != nil && !in.CounterOn() {
		return
	}
	in.pingMu.Lock()
	throttled := time.Since(in.lastPing) < installsMinGap
	if !throttled {
		in.lastPing = time.Now()
	}
	in.pingMu.Unlock()
	if throttled {
		return
	}
	base, err := in.base()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), installsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/mesh-extensions/"+url.PathEscape(id)+"/installs", http.NoBody)
	if err != nil {
		return
	}
	resp, err := in.httpc().Do(req)
	if err != nil {
		log.Printf("[extensions] install counter for %s: %v", id, err)
		return
	}
	_ = resp.Body.Close()
}

// manifestFile is the manifest's path inside a release artifact.
const manifestFile = "mesh-extension.json"

// manifestFromArtifact reads mesh-extension.json from a gzip-compressed tar
// artifact. The bytes were already matched to the signed digest, so the manifest
// that gets installed is the one the signature covers.
func manifestFromArtifact(art []byte) (json.RawMessage, error) {
	zr, err := gzip.NewReader(bytes.NewReader(art))
	if err != nil {
		return nil, fmt.Errorf("artifact is not a gzip archive: %w", err)
	}
	defer zr.Close()
	// Bound the decompressed stream, not only the compressed artifact: skipping
	// entries before the manifest still inflates them.
	tr := tar.NewReader(io.LimitReader(zr, maxUnpackedBytes))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("artifact has no " + manifestFile)
		}
		if err != nil {
			return nil, fmt.Errorf("artifact archive: %w", err)
		}
		if path.Clean(h.Name) != manifestFile || h.Typeflag != tar.TypeReg {
			continue
		}
		b, rerr := io.ReadAll(io.LimitReader(tr, maxManifestBytes+1))
		if rerr != nil || len(b) > maxManifestBytes {
			return nil, errors.New("artifact " + manifestFile + " is unreadable or too large")
		}
		return b, nil
	}
}

// fetchVerified returns the manifest of e's latest release. The artifact is
// hashed against the signed catalog digest first, and the manifest is read from
// those verified bytes; Spark's detail answer only tells whether the release is
// installable and not yanked. Nothing is stored here.
func (in *Installer) fetchVerified(ctx context.Context, e *Entry) (extensions.Manifest, error) {
	id, rel := e.ID, e.Latest
	d, err := in.Detail(ctx, id)
	if err != nil {
		return extensions.Manifest{}, err
	}
	installable := false
	for _, r := range d.Releases {
		if r.Version == rel.Version && r.Installable && !r.Yanked {
			installable = true
		}
	}
	if !installable {
		return extensions.Manifest{}, apiErr(http.StatusUnprocessableEntity, repoinstall.CodeManifestInvalid, "Spark has no installable release "+rel.Version)
	}

	base, err := in.base()
	if err != nil {
		return extensions.Manifest{}, err
	}
	art, err := in.get(ctx, base+"/api/v1/mesh-extensions/"+url.PathEscape(id)+"/releases/"+url.PathEscape(rel.Version)+"/artifact", maxArtifactBytes)
	if err != nil {
		return extensions.Manifest{}, err
	}
	sum := sha256.Sum256(art)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), *rel.ArtifactSHA256) {
		return extensions.Manifest{}, apiErr(http.StatusUnprocessableEntity, CodeDigest, "artifact does not match the catalog digest; nothing was installed")
	}

	raw, err := manifestFromArtifact(art)
	if err != nil {
		return extensions.Manifest{}, apiErr(http.StatusUnprocessableEntity, repoinstall.CodeManifestInvalid, err.Error())
	}
	m, err := repoinstall.ValidateManifest(raw)
	if err != nil {
		return extensions.Manifest{}, err
	}
	if m.ID != id || m.Version != rel.Version {
		return extensions.Manifest{}, apiErr(http.StatusUnprocessableEntity, repoinstall.CodeManifestInvalid,
			fmt.Sprintf("manifest says %s@%s, the catalog says %s@%s", m.ID, m.Version, id, rel.Version))
	}
	return m, nil
}
