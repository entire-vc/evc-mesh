package themes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/storage"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

const (
	maxCSSBytes   = 512 << 10
	maxViewsBytes = 2 << 20

	// AssetsPath is where GET serves bundle files; the file name is the
	// sha256 of the content plus .css or .js.
	AssetsPath = "/api/v1/theme/assets/"
)

// AssetStore is the slice of object storage the theme backend needs.
type AssetStore interface {
	Upload(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error
	GetObject(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error)
}

// Service is the theme backend: install, choose, resolve.
type Service struct {
	Installed repository.InstalledExtensionRepository
	Themes    repository.ThemeRepository
	// Journal switches the admin journal on (nil: off). The entries themselves are
	// written by the stores in the mutation's own transaction, so a failed write
	// fails the install, choice or delete instead of leaving it unrecorded.
	Journal repository.InstanceExtensionPolicyRepository
	// Store holds the bundle files; nil when S3 is not configured, which only
	// refuses themes that carry a stylesheet or views.
	Store AssetStore
}

func assetKey(sha, ext string) string { return "themes/" + sha + "." + ext }

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// PrepareInstall verifies the files a theme manifest points at and stores them
// under their hash. A file whose digest differs from the manifest's, or a
// views bundle the contract report was not built for, stops the install
// (ADR D9); nothing is written to the database by this step. Implements
// repoinstall.ThemeAssets.
func (s *Service) PrepareInstall(ctx context.Context, m extensions.Manifest, fetch repoinstall.FetchFunc) error {
	spec, err := ParseSpec(m)
	if err != nil {
		return err
	}
	type file struct {
		asset       *Asset
		ext, ctype  string
		limit       int
		field, what string
	}
	var files []file
	if a := spec.CSSAsset(); a != nil {
		files = append(files, file{a, "css", "text/css; charset=utf-8", maxCSSBytes, "theme.css.sha256", "stylesheet"})
	}
	if a := spec.ViewsAsset(); a != nil {
		files = append(files, file{a, "js", "text/javascript; charset=utf-8", maxViewsBytes, "theme.views.sha256", "views bundle"})
	}
	if len(files) > 0 && s.Store == nil {
		return apierror.ServiceUnavailable("object storage is not configured: a theme with CSS or views cannot be installed")
	}
	type ready struct {
		key, ctype string
		data       []byte
	}
	var out []ready
	for _, f := range files {
		data, ferr := fetch(ctx, f.asset.Path)
		if ferr != nil {
			return ferr
		}
		if len(data) == 0 || len(data) > f.limit {
			return unprocessable(CodeThemeInvalid, fmt.Sprintf("%s %q must be between 1 byte and %d bytes", f.what, f.asset.Path, f.limit), map[string]string{f.field: "size out of range"})
		}
		if got := sha256Hex(data); got != f.asset.SHA256 {
			return unprocessable(CodeHashMismatch, fmt.Sprintf("%s %q does not match the hash in the manifest", f.what, f.asset.Path),
				map[string]string{f.field: fmt.Sprintf("manifest says %s, the file is %s", f.asset.SHA256, got)})
		}
		if f.ext == "css" {
			if perr := CheckCSSPolicy(data); perr != nil {
				return unprocessable(CodeThemeInvalid, "theme CSS breaks the install policy: "+perr.Error(), map[string]string{"theme.css.path": perr.Error()})
			}
		}
		out = append(out, ready{assetKey(f.asset.SHA256, f.ext), f.ctype, data})
	}
	// Upload only after every file passed: a rejected theme leaves nothing behind.
	for _, r := range out {
		if uerr := s.Store.Upload(ctx, r.key, bytes.NewReader(r.data), int64(len(r.data)), r.ctype); uerr != nil {
			return apierror.InternalError("could not store the theme bundle: " + uerr.Error())
		}
	}
	return nil
}

// InstallBuiltin installs a theme shipped with Mesh. Idempotent: installing
// again refreshes the row.
func (s *Service) InstallBuiltin(ctx context.Context, actor uuid.UUID, id string) (*domain.InstalledExtension, error) {
	if !builtinKnown(id) {
		return nil, apierror.NotFound("builtin theme")
	}
	raw, err := builtinFile(id, repoinstall.ManifestFile)
	if err != nil {
		return nil, apierror.InternalError(err.Error())
	}
	m, err := validateBuiltinManifest(id, raw)
	if err != nil {
		return nil, err
	}
	if m.ID != id {
		return nil, apierror.InternalError("builtin theme id does not match its directory")
	}
	fetch := func(_ context.Context, path string) ([]byte, error) { return builtinFile(id, path) }
	if err := s.PrepareInstall(ctx, m, fetch); err != nil {
		return nil, err
	}
	row := &domain.InstalledExtension{ExtensionID: m.ID, Version: m.Version, Manifest: m.Raw, Source: domain.ExtensionSourceBuiltin, TrustLevel: domain.ExtensionTrustBuiltin}
	if err := s.Themes.InstallBuiltin(ctx, row, s.entry(actor, m.ID, domain.InstanceJournalInstalled, map[string]any{"version": m.Version, "source": "builtin", "kind": KindTheme})); err != nil {
		return nil, err
	}
	return s.Installed.Get(ctx, m.ID)
}

// Delete removes an installed theme. Every selection that pointed at it falls
// back (workspace: "as the instance", instance: mesh-default) with its settings
// cleared. A built-in theme can be removed too: it stays in builtin_available.
// An id that is not an installed theme is a 404.
func (s *Service) Delete(ctx context.Context, actor uuid.UUID, id string) error {
	row, err := s.Installed.Get(ctx, id)
	if err != nil {
		return err
	}
	if row == nil || parseTheme(row) == nil {
		return apierror.NotFound("theme")
	}
	ok, err := s.Themes.DeleteTheme(ctx, id, s.entry(actor, id, domain.InstanceJournalThemeDeleted, map[string]any{"version": row.Version, "source": row.Source, "kind": KindTheme}))
	if err != nil {
		return err
	}
	if !ok {
		return apierror.NotFound("theme")
	}
	return nil
}

// entry is the journal record that goes into the mutation's transaction; nil
// when the journal is off.
func (s *Service) entry(actor uuid.UUID, extID, action string, details map[string]any) *domain.InstanceExtensionJournalEntry {
	if s.Journal == nil {
		return nil
	}
	raw, _ := json.Marshal(details)
	return &domain.InstanceExtensionJournalEntry{ActorID: &actor, ExtensionID: extID, Action: action, Details: raw}
}

// installedTheme is an installed row of kind theme with its parsed parts.
type installedTheme struct {
	row      *domain.InstalledExtension
	manifest extensions.Manifest
	spec     *Spec
}

// load returns the installed theme id, or nil when there is no such usable theme
// (not installed, not a theme, blocked, or no longer valid).
func (s *Service) load(ctx context.Context, id string) (*installedTheme, error) {
	row, err := s.Installed.Get(ctx, id)
	if err != nil || row == nil || row.Blocked {
		return nil, err
	}
	return parseTheme(row), nil
}

func parseTheme(row *domain.InstalledExtension) *installedTheme {
	m, err := extensions.ParseManifest(row.Manifest)
	if err != nil || m.Kind != KindTheme {
		return nil
	}
	spec, err := ParseSpec(m)
	if err != nil {
		return nil
	}
	return &installedTheme{row: row, manifest: m, spec: spec}
}

// Info is a theme as GET /themes lists it.
type Info struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Publisher   string            `json:"publisher"`
	Description string            `json:"description"`
	Source      string            `json:"source"`
	TrustLevel  string            `json:"trust_level"`
	Modes       []string          `json:"modes"`
	HasCSS      bool              `json:"has_css"`
	HasViews    bool              `json:"has_views"`
	Slots       map[string]string `json:"slots,omitempty"`
	Settings    map[string]any    `json:"settings"`
	// Tokens are the theme's own values per mode (light/dark), enough to paint
	// a preview before the theme is chosen; settings bound to a token are applied by the caller.
	Tokens   map[string]map[string]string `json:"tokens"`
	Warnings []ContrastWarning            `json:"contrast_warnings"`
}

// List returns the installed themes with the settings schemas the form needs.
func (s *Service) List(ctx context.Context) ([]Info, error) {
	rows, err := s.Installed.List(ctx)
	if err != nil {
		return nil, err
	}
	// mesh-default is always on the list: its settings schema is what the admin edits before anything is stored.
	if !hasRow(rows, domain.DefaultThemeID) {
		if raw, ferr := builtinFile(domain.DefaultThemeID, repoinstall.ManifestFile); ferr == nil {
			rows = append(rows, domain.InstalledExtension{ExtensionID: domain.DefaultThemeID, Manifest: raw, Source: domain.ExtensionSourceBuiltin, TrustLevel: domain.ExtensionTrustBuiltin})
		}
	}
	out := []Info{}
	for i := range rows {
		t := parseTheme(&rows[i])
		if t == nil || rows[i].Blocked {
			continue
		}
		settings := map[string]any{}
		for _, lv := range []string{LevelInstance, LevelWorkspace} {
			if sch := LevelSchema(t.manifest, lv); sch != nil {
				settings[lv] = sch
			}
		}
		out = append(out, Info{
			ID: t.manifest.ID, Name: t.manifest.Name, Version: t.manifest.Version, Publisher: t.manifest.Publisher,
			Description: t.manifest.Description, Source: string(rows[i].Source), TrustLevel: string(rows[i].TrustLevel),
			Modes: t.spec.modeNames(), HasCSS: t.spec.CSS != nil, HasViews: t.spec.Views != nil, Slots: t.spec.Slots,
			Settings: settings, Tokens: nonNilTokens(t.spec.Tokens), Warnings: nonNil(CheckContrast(t.spec.Tokens)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func nonNilTokens(t map[string]map[string]string) map[string]map[string]string {
	if t == nil {
		return map[string]map[string]string{}
	}
	return t
}

func nonNil(w []ContrastWarning) []ContrastWarning {
	if w == nil {
		return []ContrastWarning{}
	}
	return w
}

// Warnings is the contrast check of an installed theme's tokens.
func (s *Service) Warnings(ctx context.Context, id string) ([]ContrastWarning, error) {
	t, err := s.load(ctx, id)
	if err != nil || t == nil {
		return nil, err
	}
	return nonNil(CheckContrast(t.spec.Tokens)), nil
}

// Resolved is what a browser needs to paint one theme (GET /theme).
type Resolved struct {
	ThemeID     string         `json:"theme_id"`
	Source      string         `json:"source"` // workspace | instance | default
	TokensCSS   string         `json:"tokens_css"`
	ThemeCSSURL string         `json:"theme_css_url"`
	ViewsURL    string         `json:"views_url"`
	Settings    map[string]any `json:"settings"`
	// Modes are the colour modes the theme ships; one entry forces that mode.
	Modes []string `json:"modes"`
	// Brand is the theme outside the app (browser chrome, manifest, emails);
	// always complete, mesh-default where the theme is silent.
	Brand Brand  `json:"brand"`
	Hash  string `json:"hash"`
}

func decodeSettings(raw json.RawMessage) map[string]any {
	out := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// Resolve applies the three levels: the workspace's theme, else the instance's,
// else mesh-default (ADR D3). workspaceID nil resolves the instance theme.
func (s *Service) Resolve(ctx context.Context, workspaceID *uuid.UUID) (*Resolved, error) {
	var wsSel *domain.ThemeSelection
	if workspaceID != nil {
		var err error
		if wsSel, err = s.Themes.GetWorkspace(ctx, *workspaceID); err != nil {
			return nil, err
		}
	}
	instSel, err := s.Themes.GetInstance(ctx)
	if err != nil {
		return nil, err
	}
	pick := func(sel *domain.ThemeSelection) (*installedTheme, error) {
		if sel == nil || sel.ThemeID == nil {
			return nil, nil
		}
		return s.load(ctx, *sel.ThemeID)
	}
	source := "workspace"
	t, err := pick(wsSel)
	if err != nil {
		return nil, err
	}
	if t == nil {
		source = "instance"
		if t, err = pick(instSel); err != nil {
			return nil, err
		}
	}
	if t == nil {
		return defaultResolved(), nil
	}
	var instSettings, wsSettings map[string]any
	if instSel != nil && instSel.ThemeID != nil && *instSel.ThemeID == t.manifest.ID {
		instSettings = decodeSettings(instSel.Settings)
	}
	if wsSel != nil && wsSel.ThemeID != nil && *wsSel.ThemeID == t.manifest.ID {
		wsSettings = decodeSettings(wsSel.Settings)
	}
	settings := effectiveSettings(t.manifest, instSettings, wsSettings)
	// The resolution is served to anonymous callers and feeds the hash and the
	// token CSS, so x-secret values never enter it.
	for _, level := range []string{LevelInstance, LevelWorkspace} {
		if lv, ok := levelSchema(t.manifest, level); ok {
			for name, f := range properties(lv) {
				if secret, _ := f["x-secret"].(bool); secret {
					delete(settings, name)
				}
			}
		}
	}
	r := &Resolved{ThemeID: t.manifest.ID, Source: source, Settings: settings, Modes: t.spec.modeNames()}
	over := tokenOverrides(t.manifest, settings)
	r.TokensCSS = BuildTokensCSS(t.spec.Tokens, over)
	r.Brand = BrandOf(mergeOverrides(t.spec.Tokens, over))
	cssSHA, viewsSHA := "", ""
	if t.spec.CSS != nil {
		cssSHA = t.spec.CSS.SHA256
		r.ThemeCSSURL = AssetsPath + cssSHA + ".css"
	}
	if t.spec.Views != nil {
		viewsSHA = t.spec.Views.SHA256
		r.ViewsURL = AssetsPath + viewsSHA + ".js"
	}
	sj, _ := json.Marshal(settings)
	r.Hash = Hash(t.manifest.ID, t.manifest.Version, r.TokensCSS, cssSHA, viewsSHA, string(sj))
	return r, nil
}

// BrandFor is the Brand of the workspace's effective theme (workspaceID nil:
// the instance theme). It never fails and never returns an empty colour: a
// storage error or a missing theme gives mesh-default, because a letter must
// still go out.
func (s *Service) BrandFor(ctx context.Context, workspaceID *uuid.UUID) Brand {
	r, err := s.Resolve(ctx, workspaceID)
	if err != nil || r == nil {
		return DefaultBrand()
	}
	return r.Brand
}

func defaultResolved() *Resolved {
	r := &Resolved{ThemeID: domain.DefaultThemeID, Source: "default", Settings: map[string]any{}, Modes: []string{"dark", "light"}, Brand: DefaultBrand()}
	r.Hash = Hash(domain.DefaultThemeID)
	return r
}

// Selection is a stored choice as the admin API returns it.
type Selection struct {
	ThemeID   *string        `json:"theme_id"`
	Settings  map[string]any `json:"settings"`
	Version   int            `json:"version"`
	UpdatedAt string         `json:"updated_at,omitempty"`
	// Warnings are contrast findings of the chosen theme with the settings
	// applied; advice only, the choice is saved regardless.
	Warnings []ContrastWarning `json:"contrast_warnings"`
}

func selectionView(sel *domain.ThemeSelection) *Selection {
	if sel == nil {
		return &Selection{Settings: map[string]any{}, Warnings: []ContrastWarning{}}
	}
	return &Selection{ThemeID: sel.ThemeID, Settings: decodeSettings(sel.Settings), Version: sel.Version, UpdatedAt: sel.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"), Warnings: []ContrastWarning{}}
}

// GetSelection returns the stored choice of the instance (workspaceID nil) or of a workspace.
func (s *Service) GetSelection(ctx context.Context, workspaceID *uuid.UUID) (*Selection, error) {
	var sel *domain.ThemeSelection
	var err error
	if workspaceID == nil {
		sel, err = s.Themes.GetInstance(ctx)
	} else {
		sel, err = s.Themes.GetWorkspace(ctx, *workspaceID)
	}
	if err != nil {
		return nil, err
	}
	return selectionView(sel), nil
}

// Select stores a choice: themeID nil means "as the instance" at workspace
// level and "mesh-default" at instance level. Settings are checked against the
// chosen theme's schema for the level; anything outside it is a 422.
func (s *Service) Select(ctx context.Context, workspaceID *uuid.UUID, actor uuid.UUID, themeID *string, settings map[string]any) (*Selection, error) {
	if settings == nil {
		settings = map[string]any{}
	}
	if themeID != nil && *themeID == domain.DefaultThemeID {
		if len(settings) == 0 {
			themeID = nil
		} else if t, err := s.load(ctx, *themeID); err != nil {
			return nil, err
		} else if t == nil {
			// mesh-default carries its own settings schema; the row it is stored against appears with the first save.
			if _, err := s.InstallBuiltin(ctx, actor, *themeID); err != nil {
				return nil, err
			}
		}
	}
	level := LevelInstance
	if workspaceID != nil {
		level = LevelWorkspace
	}
	var warnings []ContrastWarning
	if themeID == nil {
		if len(settings) > 0 {
			return nil, unprocessable(CodeSettingsInvalid, "settings belong to a theme: choose one first", map[string]string{"settings": "no theme chosen"})
		}
	} else {
		t, err := s.load(ctx, *themeID)
		if err != nil {
			return nil, err
		}
		if t == nil {
			return nil, unprocessable(CodeNotInstalled, fmt.Sprintf("theme %q is not installed", *themeID), map[string]string{"theme_id": "not an installed theme"})
		}
		if fields := ValidateSettings(t.manifest, level, settings); len(fields) > 0 {
			return nil, unprocessable(CodeSettingsInvalid, "settings do not match the theme's schema", fields)
		}
		eff := effectiveSettings(t.manifest, nil, settings)
		warnings = CheckContrast(mergeOverrides(t.spec.Tokens, tokenOverrides(t.manifest, eff)))
	}
	raw, _ := json.Marshal(settings)
	chosen := domain.DefaultThemeID
	if themeID != nil {
		chosen = *themeID
	}
	detail := map[string]any{"level": level}
	if workspaceID != nil {
		detail["workspace_id"] = workspaceID.String()
	}
	// The store adds the stored version to the entry and commits both together.
	entry := s.entry(actor, chosen, domain.InstanceJournalThemeSelected, detail)
	var stored *domain.ThemeSelection
	var err error
	if workspaceID == nil {
		stored, err = s.Themes.SetInstance(ctx, themeID, raw, actor, entry)
	} else {
		stored, err = s.Themes.SetWorkspace(ctx, *workspaceID, themeID, raw, actor, entry)
	}
	if err != nil {
		return nil, err
	}
	v := selectionView(stored)
	v.Warnings = nonNil(warnings)
	return v, nil
}

// mergeOverrides applies token overrides to every declared mode, the way
// BuildTokensCSS does, so the contrast check sees what the browser will.
func mergeOverrides(tokens map[string]map[string]string, over map[string]string) map[string]map[string]string {
	if len(over) == 0 {
		return tokens
	}
	out := map[string]map[string]string{}
	for _, mode := range []string{"light", "dark"} {
		m := map[string]string{}
		for k, v := range tokens[mode] {
			m[k] = v
		}
		for k, v := range over {
			m[k] = v
		}
		out[mode] = m
	}
	return out
}

// ErrAssetNotFound is returned by Asset for an unknown hash.
var ErrAssetNotFound = errors.New("theme asset not found")

// Asset opens a bundle file by its content hash. kind is "css" or "js". CSS is
// returned wrapped in the mesh-theme layer; ETag is the hash either way.
func (s *Service) Asset(ctx context.Context, sha, kind string) (data []byte, contentType string, err error) {
	if s.Store == nil {
		return nil, "", apierror.ServiceUnavailable("object storage is not configured")
	}
	ctype := "text/javascript; charset=utf-8"
	if kind == "css" {
		ctype = "text/css; charset=utf-8"
	}
	body, _, gerr := s.Store.GetObject(ctx, assetKey(sha, kind))
	if gerr != nil {
		if errors.Is(gerr, storage.ErrNotFound) {
			return nil, "", ErrAssetNotFound
		}
		return nil, "", gerr
	}
	defer func() { _ = body.Close() }()
	data, err = io.ReadAll(io.LimitReader(body, maxViewsBytes+1))
	if err != nil {
		return nil, "", err
	}
	if kind == "css" {
		data = WrapThemeCSS(data)
	}
	return data, ctype, nil
}

// StatusForError is the HTTP status of a service error (apierror carries its own).
func StatusForError(err error) int {
	var ae *apierror.Error
	if errors.As(err, &ae) {
		return ae.StatusCode()
	}
	return http.StatusInternalServerError
}

// validateBuiltinManifest holds an embedded manifest to the same schema as any
// other. The schema reserves every id containing "mesh" so that no third party
// can pass as the app; mesh-default is the one embedded manifest that carries it,
// so it is checked under a neutral id and returned under its own.
func validateBuiltinManifest(id string, raw []byte) (extensions.Manifest, error) {
	if id != domain.DefaultThemeID {
		return repoinstall.ValidateManifest(raw)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return extensions.Manifest{}, apierror.InternalError("builtin theme manifest is not JSON: " + err.Error())
	}
	doc["id"] = "default-look"
	neutral, _ := json.Marshal(doc)
	if _, err := repoinstall.ValidateManifest(neutral); err != nil {
		return extensions.Manifest{}, err
	}
	return extensions.ParseManifest(raw)
}

func hasRow(rows []domain.InstalledExtension, id string) bool {
	for i := range rows {
		if rows[i].ExtensionID == id {
			return true
		}
	}
	return false
}
