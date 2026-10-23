package themes_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/themes"
	"github.com/entire-vc/evc-mesh/internal/themes/themestest"
)

var errDown = errors.New("store unreachable")

// journalLog keeps the entries Service writes; the rest of the interface is unused.
type journalLog struct {
	repository.InstanceExtensionPolicyRepository
	entries []*domain.InstanceExtensionJournalEntry
}

func (j *journalLog) AppendJournal(_ context.Context, e *domain.InstanceExtensionJournalEntry) error {
	j.entries = append(j.entries, e)
	return nil
}

func strp(s string) *string { return &s }

func TestJournal_RecordsInstallAndSelection(t *testing.T) {
	svc, inst, _, _ := newService()
	log := &journalLog{}
	svc.Journal = log
	actor := uuid.New()
	ctx := context.Background()

	_, err := svc.InstallBuiltin(ctx, actor, "graphite")
	require.NoError(t, err)
	_, err = svc.Select(ctx, nil, actor, strp("graphite"), nil)
	require.NoError(t, err)
	ws := uuid.New()
	_, err = svc.Select(ctx, &ws, actor, strp("graphite"), nil)
	require.NoError(t, err)

	require.Len(t, log.entries, 3)
	require.Equal(t, domain.InstanceJournalInstalled, log.entries[0].Action)
	require.Equal(t, domain.InstanceJournalThemeSelected, log.entries[1].Action)
	require.Contains(t, string(log.entries[2].Details), ws.String(), "the workspace choice names the workspace")
	require.Equal(t, actor, *log.entries[2].ActorID)
	require.Contains(t, inst.Rows, "graphite")
}

func TestPrepareInstall_Refusals(t *testing.T) {
	css := []byte(".a{color:red}")

	t.Run("no object store", func(t *testing.T) {
		svc, _, _, _ := newService()
		svc.Store = nil
		err := svc.PrepareInstall(context.Background(), parse(t, themestest.Manifest{CSS: css}.JSON()), filesOf(map[string][]byte{"theme.css": css}))
		require.Equal(t, http.StatusServiceUnavailable, apiErr(t, err).StatusCode())
	})
	t.Run("file cannot be fetched", func(t *testing.T) {
		svc, _, _, store := newService()
		err := svc.PrepareInstall(context.Background(), parse(t, themestest.Manifest{CSS: css}.JSON()), filesOf(nil))
		require.ErrorContains(t, err, "no such file theme.css")
		require.Empty(t, store.Objects, "nothing stored for a theme that failed")
	})
	t.Run("empty file", func(t *testing.T) {
		svc, _, _, _ := newService()
		err := svc.PrepareInstall(context.Background(), parse(t, themestest.Manifest{CSS: css}.JSON()), filesOf(map[string][]byte{"theme.css": {}}))
		require.Equal(t, http.StatusUnprocessableEntity, apiErr(t, err).StatusCode())
	})
	t.Run("object store rejects the upload", func(t *testing.T) {
		svc, _, _, store := newService()
		store.Err = errDown
		err := svc.PrepareInstall(context.Background(), parse(t, themestest.Manifest{CSS: css}.JSON()), filesOf(map[string][]byte{"theme.css": css}))
		require.Equal(t, http.StatusInternalServerError, apiErr(t, err).StatusCode())
		require.ErrorContains(t, err, errDown.Error())
	})
	t.Run("a manifest that is not a theme", func(t *testing.T) {
		svc, _, _, _ := newService()
		err := svc.PrepareInstall(context.Background(), parse(t, themestest.Manifest{Tokens: map[string]map[string]string{"light": {"brand-pink": "1 2% 3%"}}}.JSON()), filesOf(nil))
		require.Error(t, err)
	})
}

func TestInstallBuiltin_StoreFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("database down", func(t *testing.T) {
		svc, inst, _, _ := newService()
		inst.Err = errDown
		_, err := svc.InstallBuiltin(ctx, uuid.New(), "graphite")
		require.ErrorIs(t, err, errDown)
	})
}

func TestList_SkipsUnusableRowsAndReportsStoreErrors(t *testing.T) {
	ctx := context.Background()
	svc, inst, _, _ := newService()
	inst.Add("good", themestest.Manifest{ID: "good"}.JSON())
	inst.Add("blocked", themestest.Manifest{ID: "blocked"}.JSON())
	inst.Rows["blocked"].Blocked = true
	inst.Add("garbage", []byte(`{not json`))

	got, err := svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "good", got[0].ID)

	inst.Err = errDown
	_, err = svc.List(ctx)
	require.ErrorIs(t, err, errDown)
}

func TestWarnings(t *testing.T) {
	ctx := context.Background()
	svc, inst, _, _ := newService()
	inst.Add("low", themestest.Manifest{ID: "low", Tokens: map[string]map[string]string{
		"light": {"background": "0 0% 100%", "foreground": "0 0% 95%"},
	}}.JSON())
	w, err := svc.Warnings(ctx, "low")
	require.NoError(t, err)
	require.NotEmpty(t, w, "near-white text on white is flagged")
	require.NotEmpty(t, w[0].String(), "a warning prints")

	w, err = svc.Warnings(ctx, "missing")
	require.NoError(t, err)
	require.Nil(t, w, "not installed: nothing to report")

	inst.Err = errDown
	_, err = svc.Warnings(ctx, "low")
	require.ErrorIs(t, err, errDown)
}

func TestResolve_StoreErrors(t *testing.T) {
	ctx := context.Background()
	ws := uuid.New()
	t.Run("selection store down", func(t *testing.T) {
		svc, _, th, _ := newService()
		th.Err = errDown
		_, err := svc.Resolve(ctx, &ws)
		require.ErrorIs(t, err, errDown, "workspace lookup")
		_, err = svc.Resolve(ctx, nil)
		require.ErrorIs(t, err, errDown, "instance lookup")
	})
	t.Run("theme table down after the choice was read", func(t *testing.T) {
		svc, inst, _, _ := newService()
		inst.Add("graphite", themestest.Manifest{}.JSON())
		_, err := svc.Select(ctx, nil, uuid.New(), strp("graphite"), nil)
		require.NoError(t, err)
		_, err = svc.Select(ctx, &ws, uuid.New(), strp("graphite"), nil)
		require.NoError(t, err)
		inst.Err = errDown
		_, err = svc.Resolve(ctx, &ws)
		require.ErrorIs(t, err, errDown, "workspace choice")
		_, err = svc.Resolve(ctx, nil)
		require.ErrorIs(t, err, errDown, "instance choice")
	})
}

func TestGetSelection(t *testing.T) {
	ctx := context.Background()
	svc, inst, th, _ := newService()
	ws := uuid.New()
	inst.Add("graphite", themestest.Manifest{}.JSON())

	sel, err := svc.GetSelection(ctx, nil)
	require.NoError(t, err)
	require.Nil(t, sel.ThemeID, "no choice yet")

	_, err = svc.Select(ctx, nil, uuid.New(), strp("graphite"), nil)
	require.NoError(t, err)
	sel, err = svc.GetSelection(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "graphite", *sel.ThemeID)
	require.Equal(t, 1, sel.Version)

	th.Err = errDown
	_, err = svc.GetSelection(ctx, nil)
	require.ErrorIs(t, err, errDown)
	_, err = svc.GetSelection(ctx, &ws)
	require.ErrorIs(t, err, errDown)
}

func TestSelect_Refusals(t *testing.T) {
	ctx := context.Background()
	actor := uuid.New()
	ws := uuid.New()

	t.Run("settings without a theme", func(t *testing.T) {
		svc, _, _, _ := newService()
		_, err := svc.Select(ctx, nil, actor, nil, map[string]any{"accent_colour": "1 2% 3%"})
		require.Equal(t, http.StatusUnprocessableEntity, apiErr(t, err).StatusCode())
	})
	t.Run("mesh-default clears the choice", func(t *testing.T) {
		svc, inst, _, _ := newService()
		inst.Add("graphite", themestest.Manifest{}.JSON())
		_, err := svc.Select(ctx, nil, actor, strp("graphite"), nil)
		require.NoError(t, err)
		sel, err := svc.Select(ctx, nil, actor, strp(domain.DefaultThemeID), nil)
		require.NoError(t, err)
		require.Nil(t, sel.ThemeID)
	})
	t.Run("theme not installed", func(t *testing.T) {
		svc, _, _, _ := newService()
		_, err := svc.Select(ctx, &ws, actor, strp("ghost"), nil)
		require.Equal(t, http.StatusUnprocessableEntity, apiErr(t, err).StatusCode())
	})
	t.Run("installed table down", func(t *testing.T) {
		svc, inst, _, _ := newService()
		inst.Err = errDown
		_, err := svc.Select(ctx, nil, actor, strp("graphite"), nil)
		require.ErrorIs(t, err, errDown)
	})
	t.Run("selection store down", func(t *testing.T) {
		svc, inst, th, _ := newService()
		inst.Add("graphite", themestest.Manifest{}.JSON())
		th.Err = errDown
		_, err := svc.Select(ctx, nil, actor, strp("graphite"), nil)
		require.ErrorIs(t, err, errDown, "instance")
		_, err = svc.Select(ctx, &ws, actor, strp("graphite"), nil)
		require.ErrorIs(t, err, errDown, "workspace")
	})
}

func TestAsset_Refusals(t *testing.T) {
	ctx := context.Background()
	js := []byte("export default 1")
	t.Run("no object store", func(t *testing.T) {
		svc, _, _, _ := newService()
		svc.Store = nil
		_, _, err := svc.Asset(ctx, themestest.SHA(js), "js")
		require.Equal(t, http.StatusServiceUnavailable, apiErr(t, err).StatusCode())
	})
	t.Run("object store down", func(t *testing.T) {
		svc, _, _, store := newService()
		store.Err = errDown
		_, _, err := svc.Asset(ctx, themestest.SHA(js), "js")
		require.ErrorIs(t, err, errDown)
	})
	t.Run("script is served as is", func(t *testing.T) {
		svc, _, _, store := newService()
		store.Objects["themes/"+themestest.SHA(js)+".js"] = js
		data, ctype, err := svc.Asset(ctx, themestest.SHA(js), "js")
		require.NoError(t, err)
		require.Equal(t, js, data)
		require.True(t, strings.HasPrefix(ctype, "text/javascript"))
	})
}

func TestStatusForError(t *testing.T) {
	require.Equal(t, http.StatusInternalServerError, themes.StatusForError(errDown))
	svc, _, _, _ := newService()
	_, err := svc.InstallBuiltin(context.Background(), uuid.New(), "ghost")
	require.Equal(t, http.StatusNotFound, themes.StatusForError(err), "an apierror keeps its own status")
}

func TestSelect_KeywordsNextToARefApply(t *testing.T) {
	ctx, actor, id := context.Background(), uuid.New(), "graphite"
	svc, inst, _, _ := newService()
	field := map[string]any{"type": "string", "x-token": "primary"}
	inst.Add("graphite", themestest.Manifest{
		Schemas: map[string]any{"colours": map[string]any{"properties": map[string]any{"accent_colour": field}}},
		Settings: map[string]any{
			// "required" sits next to the reference, "dense" extends the referenced fields.
			"instance": map[string]any{"$ref": "#/schemas/colours", "required": []string{"accent_colour"},
				"properties": map[string]any{"dense": map[string]any{"type": "boolean"}}},
		},
	}.JSON())

	_, err := svc.Select(ctx, nil, actor, &id, map[string]any{})
	require.Equal(t, http.StatusUnprocessableEntity, apiErr(t, err).StatusCode(), "the sibling `required` is enforced")

	_, err = svc.Select(ctx, nil, actor, &id, map[string]any{"accent_colour": "1 2% 3%", "dense": true})
	require.NoError(t, err, "referenced and sibling fields are both accepted")

	_, err = svc.Select(ctx, nil, actor, &id, map[string]any{"accent_colour": "1 2% 3%", "other": 1})
	require.Equal(t, http.StatusUnprocessableEntity, apiErr(t, err).StatusCode(), "a field neither names is still refused")
}

func TestSelect_RefAndSiblingConstraintsAccumulate(t *testing.T) {
	ctx, actor, id := context.Background(), uuid.New(), "graphite"
	svc, inst, _, _ := newService()
	str := map[string]any{"type": "string"}
	inst.Add("graphite", themestest.Manifest{
		Schemas: map[string]any{"base": map[string]any{
			"required":      []string{"base"},
			"maxProperties": 3,
			"properties":    map[string]any{"base": str, "accent_colour": map[string]any{"type": "string", "x-token": "primary"}},
		}},
		Settings: map[string]any{
			// Both sides say `required` and `maxProperties`: neither may replace the other.
			"instance": map[string]any{"$ref": "#/schemas/base", "required": []string{"accent_colour"}, "maxProperties": 2},
		},
	}.JSON())

	_, err := svc.Select(ctx, nil, actor, &id, map[string]any{"accent_colour": "1 2% 3%"})
	require.Equal(t, http.StatusUnprocessableEntity, apiErr(t, err).StatusCode(), "the referenced `required: base` still holds")

	_, err = svc.Select(ctx, nil, actor, &id, map[string]any{"base": "x"})
	require.Equal(t, http.StatusUnprocessableEntity, apiErr(t, err).StatusCode(), "the sibling `required: accent_colour` holds too")

	_, err = svc.Select(ctx, nil, actor, &id, map[string]any{"base": "x", "accent_colour": "1 2% 3%"})
	require.NoError(t, err, "control: both required fields present")
}
