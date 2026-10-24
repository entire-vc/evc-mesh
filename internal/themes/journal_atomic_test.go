package themes_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/themes"
	"github.com/entire-vc/evc-mesh/internal/themes/themestest"
)

// A journal write that fails must fail the operation and leave its state as it
// was: no install, choice or delete may report success without its record.
func TestJournalFailureFailsTheMutation(t *testing.T) {
	ctx, actor := context.Background(), uuid.New()
	errJournal := errors.New("journal down")
	manifest := themestest.Manifest{Settings: themestest.ColourSettings()}.JSON()
	newJournaled := func() (*themes.Service, *themestest.Installed, *themestest.Themes) {
		svc, inst, th, _ := newService()
		svc.Journal = journalOnSwitch{}
		th.JournalErr = errJournal
		return svc, inst, th
	}

	t.Run("select", func(t *testing.T) {
		svc, inst, th := newJournaled()
		inst.Add("graphite", manifest)
		id := "graphite"
		_, err := svc.Select(ctx, nil, actor, &id, map[string]any{})
		require.ErrorIs(t, err, errJournal)
		require.Nil(t, th.Instance, "the choice is not stored")
		th.JournalErr = nil
		_, err = svc.Select(ctx, nil, actor, &id, map[string]any{})
		require.NoError(t, err, "a retry after the journal is back succeeds")
		require.Len(t, th.Journal, 1)
		require.Equal(t, domain.InstanceJournalThemeSelected, th.Journal[0].Action)
		var d map[string]any
		require.NoError(t, json.Unmarshal(th.Journal[0].Details, &d))
		require.EqualValues(t, th.Instance.Version, d["version"], "the entry carries the stored version")
	})
	t.Run("delete", func(t *testing.T) {
		svc, inst, th := newJournaled()
		inst.Add("graphite", manifest)
		require.ErrorIs(t, svc.Delete(ctx, actor, "graphite"), errJournal)
		require.Contains(t, inst.Rows, "graphite", "the theme is still installed")
		th.JournalErr = nil
		require.NoError(t, svc.Delete(ctx, actor, "graphite"), "the retry is not a 404")
		require.Len(t, th.Journal, 1)
		require.Equal(t, domain.InstanceJournalThemeDeleted, th.Journal[0].Action)
	})
	t.Run("install builtin", func(t *testing.T) {
		svc, inst, th := newJournaled()
		id := themes.BuiltinIDs()[0]
		_, err := svc.InstallBuiltin(ctx, actor, id)
		require.ErrorIs(t, err, errJournal)
		require.NotContains(t, inst.Rows, id, "nothing is installed")
		th.JournalErr = nil
		_, err = svc.InstallBuiltin(ctx, actor, id)
		require.NoError(t, err)
		require.Contains(t, inst.Rows, id)
		require.Len(t, th.Journal, 1)
	})
	t.Run("journal off", func(t *testing.T) {
		svc, inst, th, _ := newService()
		th.JournalErr = errJournal // never consulted: no entry is built without a journal
		inst.Add("graphite", manifest)
		require.NoError(t, svc.Delete(ctx, actor, "graphite"))
		require.Empty(t, th.Journal)
	})
}
