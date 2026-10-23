// Package themestest holds in-memory stand-ins for the stores behind
// themes.Service and a builder of theme manifests, shared by the themes and
// handler tests. The stores are real data structures, not call recorders.
package themestest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/storage"
)

// SHA is the hex sha256 of b.
func SHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Installed is an in-memory repository.InstalledExtensionRepository.
type Installed struct {
	mu   sync.Mutex
	Rows map[string]*domain.InstalledExtension
	// Err, when set, fails every call: the store is unreachable.
	Err error
}

func NewInstalled() *Installed { return &Installed{Rows: map[string]*domain.InstalledExtension{}} }

func (s *Installed) UpsertBuiltin(_ context.Context, e *domain.InstalledExtension) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	c := *e
	s.Rows[e.ExtensionID] = &c
	return nil
}

func (s *Installed) Get(_ context.Context, id string) (*domain.InstalledExtension, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return nil, s.Err
	}
	if r, ok := s.Rows[id]; ok {
		c := *r
		return &c, nil
	}
	return nil, nil
}

func (s *Installed) List(context.Context) ([]domain.InstalledExtension, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return nil, s.Err
	}
	ids := make([]string, 0, len(s.Rows))
	for id := range s.Rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]domain.InstalledExtension, 0, len(ids))
	for _, id := range ids {
		out = append(out, *s.Rows[id])
	}
	return out, nil
}

// Add stores a theme manifest as an installed row.
func (s *Installed) Add(id string, manifest []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Rows[id] = &domain.InstalledExtension{ExtensionID: id, Version: "1.0.0", Manifest: manifest, Source: domain.ExtensionSourceRepo, TrustLevel: domain.ExtensionTrustCustom}
}

// Themes is an in-memory repository.ThemeRepository.
type Themes struct {
	mu        sync.Mutex
	Instance  *domain.ThemeSelection
	Workspace map[uuid.UUID]*domain.ThemeSelection
	// Err, when set, fails every call: the store is unreachable.
	Err error
}

func NewThemes() *Themes { return &Themes{Workspace: map[uuid.UUID]*domain.ThemeSelection{}} }

func next(prev *domain.ThemeSelection, id *string, settings json.RawMessage, by uuid.UUID) *domain.ThemeSelection {
	v := 1
	if prev != nil {
		v = prev.Version + 1
	}
	return &domain.ThemeSelection{ThemeID: id, Settings: settings, Version: v, UpdatedBy: &by, UpdatedAt: time.Now()}
}

func (t *Themes) GetInstance(context.Context) (*domain.ThemeSelection, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Err != nil {
		return nil, t.Err
	}
	return t.Instance, nil
}

func (t *Themes) SetInstance(_ context.Context, id *string, settings json.RawMessage, by uuid.UUID) (*domain.ThemeSelection, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Err != nil {
		return nil, t.Err
	}
	t.Instance = next(t.Instance, id, settings, by)
	return t.Instance, nil
}

func (t *Themes) GetWorkspace(_ context.Context, ws uuid.UUID) (*domain.ThemeSelection, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Err != nil {
		return nil, t.Err
	}
	return t.Workspace[ws], nil
}

func (t *Themes) SetWorkspace(_ context.Context, ws uuid.UUID, id *string, settings json.RawMessage, by uuid.UUID) (*domain.ThemeSelection, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Err != nil {
		return nil, t.Err
	}
	t.Workspace[ws] = next(t.Workspace[ws], id, settings, by)
	return t.Workspace[ws], nil
}

// Store is an in-memory object store.
type Store struct {
	mu      sync.Mutex
	Objects map[string][]byte
	// Err, when set, fails every call: the object store is unreachable.
	Err error
}

func NewStore() *Store { return &Store{Objects: map[string][]byte{}} }

func (s *Store) Upload(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	s.Objects[key] = b
	return nil
}

func (s *Store) GetObject(_ context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return nil, storage.ObjectInfo{}, s.Err
	}
	b, ok := s.Objects[key]
	if !ok {
		return nil, storage.ObjectInfo{}, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), storage.ObjectInfo{Size: int64(len(b))}, nil
}

// Manifest describes a theme manifest to build.
type Manifest struct {
	ID       string
	Version  string
	Tokens   map[string]map[string]string // mode → token → value; nil means tokens-only with a default primary
	CSS      []byte                       // stylesheet bytes; nil means none
	Views    []byte                       // views bundle bytes; nil means none
	Slots    map[string]string            // slot → ^N; defaults to board.card when Views is set
	Settings map[string]any               // manifest "settings" block, optional
	Schemas  map[string]any               // manifest "schemas" block (targets of a settings $ref), optional
	// ReportBundleSHA overrides the bundle hash in the contract report (a stale report).
	ReportBundleSHA string
}

// JSON renders the manifest.
func (m Manifest) JSON() []byte {
	if m.ID == "" {
		m.ID = "graphite"
	}
	if m.Version == "" {
		m.Version = "1.0.0"
	}
	if m.Tokens == nil {
		m.Tokens = map[string]map[string]string{"light": {"primary": "222 47% 20%"}, "dark": {"primary": "210 40% 96%"}}
	}
	modes := []string{}
	for _, mode := range []string{"light", "dark"} {
		if _, ok := m.Tokens[mode]; ok {
			modes = append(modes, mode)
		}
	}
	th := map[string]any{"tokens_contract": "1", "modes": modes, "tokens": m.Tokens}
	if m.CSS != nil {
		th["css"] = map[string]any{"path": "theme.css", "sha256": SHA(m.CSS)}
	}
	if m.Views != nil {
		slots := m.Slots
		if slots == nil {
			slots = map[string]string{"board.card": "^1"}
		}
		passed := map[string]string{}
		for slot := range slots {
			passed[slot] = "1.0.0"
		}
		reportSHA := m.ReportBundleSHA
		if reportSHA == "" {
			reportSHA = SHA(m.Views)
		}
		th["views"] = map[string]any{"bundle": "views.js", "sha256": SHA(m.Views)}
		th["slots"] = slots
		th["contract_report"] = map[string]any{"sdk_version": "1.0.0", "bundle_sha256": reportSHA, "passed": passed}
	}
	doc := map[string]any{
		"schema": 1, "id": m.ID, "name": "Test theme", "version": m.Version, "kind": "theme", "runtime": "builtin",
		"mesh_api": ">=1 <2", "publisher": "example", "description": "A theme for tests", "category": "appearance",
		"levels": map[string]bool{"instance": true, "workspace": true, "project": false}, "capabilities": map[string]any{},
		"theme": th,
	}
	if m.Settings != nil {
		doc["settings"] = m.Settings
	}
	if m.Schemas != nil {
		doc["schemas"] = m.Schemas
	}
	out, _ := json.Marshal(doc)
	return out
}

// ColourSettings is a settings block with one token-bound field per level.
func ColourSettings() map[string]any {
	field := map[string]any{"type": "string", "x-token": "primary", "default": "222 47% 20%"}
	return map[string]any{
		"instance":  map[string]any{"properties": map[string]any{"accent_colour": field}},
		"workspace": map[string]any{"properties": map[string]any{"accent_colour": field, "dense": map[string]any{"type": "boolean"}}},
	}
}
