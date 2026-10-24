package themestest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/storage"
)

var errDown = errors.New("down")

func TestInstalled_RoundTripAndOrder(t *testing.T) {
	ctx := context.Background()
	s := NewInstalled()
	s.Add("b", []byte(`{}`))
	require.NoError(t, s.UpsertBuiltin(ctx, &domain.InstalledExtension{ExtensionID: "a", Version: "2.0.0"}))

	got, err := s.Get(ctx, "a")
	require.NoError(t, err)
	require.Equal(t, "2.0.0", got.Version)
	got.Version = "changed by the caller"
	again, _ := s.Get(ctx, "a")
	require.Equal(t, "2.0.0", again.Version, "callers get copies, not the stored row")

	missing, err := s.Get(ctx, "zzz")
	require.NoError(t, err)
	require.Nil(t, missing)

	list, err := s.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, []string{"a", "b"}, []string{list[0].ExtensionID, list[1].ExtensionID}, "sorted by id")
	require.Equal(t, domain.ExtensionSourceRepo, list[1].Source)

	s.Err = errDown
	require.ErrorIs(t, s.UpsertBuiltin(ctx, &domain.InstalledExtension{ExtensionID: "c"}), errDown)
	_, err = s.Get(ctx, "a")
	require.ErrorIs(t, err, errDown)
	_, err = s.List(ctx)
	require.ErrorIs(t, err, errDown)
	require.NotContains(t, s.Rows, "c", "a failed write stores nothing")
}

func TestThemes_VersionsAndErrors(t *testing.T) {
	ctx := context.Background()
	th := NewThemes()
	by, ws, id := uuid.New(), uuid.New(), "graphite"

	got, err := th.GetInstance(ctx)
	require.NoError(t, err)
	require.Nil(t, got)

	first, err := th.SetInstance(ctx, &id, json.RawMessage(`{}`), by, nil)
	require.NoError(t, err)
	second, err := th.SetInstance(ctx, nil, json.RawMessage(`{}`), by, nil)
	require.NoError(t, err)
	require.Equal(t, []int{1, 2}, []int{first.Version, second.Version})
	require.Nil(t, second.ThemeID)

	w1, err := th.SetWorkspace(ctx, ws, &id, nil, by, nil)
	require.NoError(t, err)
	w2, err := th.SetWorkspace(ctx, ws, &id, nil, by, nil)
	require.NoError(t, err)
	require.Equal(t, []int{1, 2}, []int{w1.Version, w2.Version})
	read, err := th.GetWorkspace(ctx, ws)
	require.NoError(t, err)
	require.Equal(t, 2, read.Version)
	other, err := th.GetWorkspace(ctx, uuid.New())
	require.NoError(t, err)
	require.Nil(t, other, "another workspace has no choice")

	th.Err = errDown
	_, err = th.GetInstance(ctx)
	require.ErrorIs(t, err, errDown)
	_, err = th.SetInstance(ctx, &id, nil, by, nil)
	require.ErrorIs(t, err, errDown)
	_, err = th.GetWorkspace(ctx, ws)
	require.ErrorIs(t, err, errDown)
	_, err = th.SetWorkspace(ctx, ws, &id, nil, by, nil)
	require.ErrorIs(t, err, errDown)
	th.Err = nil
	read, _ = th.GetWorkspace(ctx, ws)
	require.Equal(t, 2, read.Version, "failed writes did not bump the version")
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errDown }

func TestStore_UploadGet(t *testing.T) {
	ctx := context.Background()
	s := NewStore()
	require.NoError(t, s.Upload(ctx, "k", bytes.NewReader([]byte("body")), 4, "text/plain"))

	rc, info, err := s.GetObject(ctx, "k")
	require.NoError(t, err)
	b, _ := io.ReadAll(rc)
	require.Equal(t, "body", string(b))
	require.EqualValues(t, 4, info.Size)

	_, _, err = s.GetObject(ctx, "nope")
	require.ErrorIs(t, err, storage.ErrNotFound)

	require.ErrorIs(t, s.Upload(ctx, "bad", failingReader{}, 1, ""), errDown, "a broken body is reported")
	require.NotContains(t, s.Objects, "bad")

	s.Err = errDown
	require.ErrorIs(t, s.Upload(ctx, "k2", bytes.NewReader([]byte("x")), 1, ""), errDown)
	_, _, err = s.GetObject(ctx, "k")
	require.ErrorIs(t, err, errDown)
	require.NotContains(t, s.Objects, "k2")
}

func TestSHA(t *testing.T) {
	require.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", SHA(nil), "sha256 of nothing")
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

func TestManifest_JSON(t *testing.T) {
	t.Run("tokens only", func(t *testing.T) {
		m := decode(t, Manifest{}.JSON())
		require.Equal(t, "graphite", m["id"])
		require.Equal(t, "theme", m["kind"])
		th := m["theme"].(map[string]any)
		require.NotContains(t, th, "css")
		require.NotContains(t, th, "views")
		require.NotContains(t, m, "settings")
		require.Equal(t, []any{"light", "dark"}, th["modes"])
	})
	t.Run("one mode", func(t *testing.T) {
		m := decode(t, Manifest{ID: "dark-only", Version: "3.1.0", Tokens: map[string]map[string]string{"dark": {"primary": "1 2% 3%"}}}.JSON())
		require.Equal(t, "dark-only", m["id"])
		require.Equal(t, "3.1.0", m["version"])
		require.Equal(t, []any{"dark"}, m["theme"].(map[string]any)["modes"])
	})
	t.Run("css and views carry their hashes", func(t *testing.T) {
		css, views := []byte(".a{}"), []byte("export {}")
		th := decode(t, Manifest{CSS: css, Views: views, Settings: ColourSettings()}.JSON())["theme"].(map[string]any)
		require.Equal(t, SHA(css), th["css"].(map[string]any)["sha256"])
		require.Equal(t, SHA(views), th["views"].(map[string]any)["sha256"])
		require.Equal(t, SHA(views), th["contract_report"].(map[string]any)["bundle_sha256"])
		require.Equal(t, map[string]any{"board.card": "^1"}, th["slots"], "default slot")
	})
	t.Run("a stale report and custom slots", func(t *testing.T) {
		stale := "b"
		th := decode(t, Manifest{Views: []byte("v"), ReportBundleSHA: stale, Slots: map[string]string{"task.card": "^2"}}.JSON())["theme"].(map[string]any)
		require.Equal(t, stale, th["contract_report"].(map[string]any)["bundle_sha256"])
		require.Equal(t, "1.0.0", th["contract_report"].(map[string]any)["passed"].(map[string]any)["task.card"])
	})
	t.Run("settings block", func(t *testing.T) {
		m := decode(t, Manifest{Settings: ColourSettings()}.JSON())
		s := m["settings"].(map[string]any)
		require.Contains(t, s, "instance")
		require.Contains(t, s["workspace"].(map[string]any)["properties"], "dense")
	})
}

func TestThemes_DeleteTheme(t *testing.T) {
	ctx, by, ws, other := context.Background(), uuid.New(), uuid.New(), uuid.New()
	id, keep := "graphite", "other"
	inst, th := NewInstalled(), NewThemes()
	th.Installed = inst
	inst.Add(id, []byte(`{}`))
	inst.Add(keep, []byte(`{}`))
	_, _ = th.SetInstance(ctx, &id, json.RawMessage(`{"a":1}`), by, nil)
	_, _ = th.SetWorkspace(ctx, ws, &id, json.RawMessage(`{"a":1}`), by, nil)
	_, _ = th.SetWorkspace(ctx, other, &keep, json.RawMessage(`{"a":1}`), by, nil)

	ok, err := th.DeleteTheme(ctx, id, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotContains(t, inst.Rows, id)
	require.Contains(t, inst.Rows, keep)
	require.Nil(t, th.Instance.ThemeID)
	require.JSONEq(t, `{}`, string(th.Workspace[ws].Settings))
	require.Equal(t, keep, *th.Workspace[other].ThemeID, "control: another theme's selection stays")

	ok, err = th.DeleteTheme(ctx, id, nil)
	require.NoError(t, err)
	require.False(t, ok, "already gone")

	th.OnDelete = func(string) bool { return true }
	ok, err = th.DeleteTheme(ctx, id, nil)
	require.NoError(t, err)
	require.True(t, ok, "OnDelete decides when the rows live elsewhere")

	th.Err = errDown
	_, err = th.DeleteTheme(ctx, id, nil)
	require.ErrorIs(t, err, errDown)
}

func entry(action string) *domain.InstanceExtensionJournalEntry {
	return &domain.InstanceExtensionJournalEntry{ExtensionID: "a", Action: action, Details: json.RawMessage(`{"k":"v"}`)}
}

func TestThemes_JournalCommitsWithTheMutation(t *testing.T) {
	ctx, by := context.Background(), uuid.New()
	id, ws := "a", uuid.New()
	th := NewThemes()

	_, err := th.SetInstance(ctx, &id, json.RawMessage(`{}`), by, entry("theme_selected"))
	require.NoError(t, err)
	_, err = th.SetWorkspace(ctx, ws, &id, json.RawMessage(`{}`), by, entry("theme_selected"))
	require.NoError(t, err)
	require.Len(t, th.Journal, 2)
	var d map[string]any
	require.NoError(t, json.Unmarshal(th.Journal[0].Details, &d))
	require.EqualValues(t, 1, d["version"], "the entry carries the stored version")
	require.Equal(t, "v", d["k"])

	_, err = th.SetInstance(ctx, &id, nil, by, nil)
	require.NoError(t, err, "no entry: nothing journaled")
	require.Len(t, th.Journal, 2)

	th.JournalErr = errDown
	before := th.Instance.Version
	_, err = th.SetInstance(ctx, nil, nil, by, entry("theme_selected"))
	require.ErrorIs(t, err, errDown)
	require.Equal(t, before, th.Instance.Version, "a failed journal write leaves the choice alone")
	_, err = th.SetWorkspace(ctx, ws, nil, nil, by, entry("theme_selected"))
	require.ErrorIs(t, err, errDown)
	require.NotNil(t, th.Workspace[ws].ThemeID)
	_, err = th.SetInstance(ctx, nil, nil, by, nil)
	require.NoError(t, err, "control: without an entry the journal is not consulted")
}

func TestThemes_InstallBuiltinAndDeleteJournal(t *testing.T) {
	ctx := context.Background()
	inst := NewInstalled()
	th := NewThemes()
	th.Installed = inst
	ext := &domain.InstalledExtension{ExtensionID: "a", Version: "1.0.0"}

	th.JournalErr = errDown
	require.ErrorIs(t, th.InstallBuiltin(ctx, ext, entry("installed")), errDown)
	require.NotContains(t, inst.Rows, "a", "nothing installed when the journal fails")
	th.JournalErr = nil
	require.NoError(t, th.InstallBuiltin(ctx, ext, entry("installed")))
	require.Contains(t, inst.Rows, "a")
	require.Len(t, th.Journal, 1)

	th.JournalErr = errDown
	ok, err := th.DeleteTheme(ctx, "a", entry("theme_deleted"))
	require.ErrorIs(t, err, errDown)
	require.False(t, ok)
	require.Contains(t, inst.Rows, "a", "still installed")
	th.JournalErr = nil
	ok, err = th.DeleteTheme(ctx, "a", entry("theme_deleted"))
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, th.Journal, 2)
	ok, err = th.DeleteTheme(ctx, "a", entry("theme_deleted"))
	require.NoError(t, err)
	require.False(t, ok)
	require.Len(t, th.Journal, 2, "nothing deleted, nothing recorded")

	var stood []string
	th2 := NewThemes()
	th2.OnInstall = func(e *domain.InstalledExtension) error { stood = append(stood, e.ExtensionID); return nil }
	require.NoError(t, th2.InstallBuiltin(ctx, ext, nil))
	require.Equal(t, []string{"a"}, stood, "OnInstall stands in for Installed")
	th2.OnInstall = func(*domain.InstalledExtension) error { return errDown }
	require.ErrorIs(t, th2.InstallBuiltin(ctx, ext, nil), errDown)
	th2.Err = errDown
	require.ErrorIs(t, th2.InstallBuiltin(ctx, ext, nil), errDown)
	require.NoError(t, NewThemes().InstallBuiltin(ctx, ext, nil), "no installed store at all")
}
