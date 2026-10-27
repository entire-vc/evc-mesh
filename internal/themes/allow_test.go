package themes_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions/exttest"
	"github.com/entire-vc/evc-mesh/internal/themes/themestest"
)

// The administrator's allow-list holds for themes too: a denied theme cannot
// be chosen and is not painted, the stored choice is kept, and allowing it
// again brings it back. The standard theme is never refused.
func TestAllowList_Themes(t *testing.T) {
	ctx := context.Background()
	svc, inst, _, _ := newService()
	allow := &exttest.Allow{}
	svc.Allow = allow
	actor, ws := uuid.New(), uuid.New()
	inst.Add("graphite", themestest.Manifest{}.JSON())

	_, err := svc.Select(ctx, &ws, actor, strp("graphite"), nil)
	require.NoError(t, err)

	allow.Deny("graphite")
	res, err := svc.Resolve(ctx, &ws)
	require.NoError(t, err)
	require.Equal(t, domain.DefaultThemeID, res.ThemeID, "a denied theme is not painted")

	_, err = svc.Select(ctx, nil, actor, strp("graphite"), nil)
	ae := apiErr(t, err)
	require.Equal(t, http.StatusForbidden, ae.StatusCode())
	require.Equal(t, "extension_not_allowed", ae.ErrorCode)

	sel, err := svc.GetSelection(ctx, &ws)
	require.NoError(t, err)
	require.Equal(t, "graphite", *sel.ThemeID, "the stored choice is kept")

	allow.Permit("graphite")
	res, err = svc.Resolve(ctx, &ws)
	require.NoError(t, err)
	require.Equal(t, "graphite", res.ThemeID, "allowed again, the choice is back")

	allow.Deny("graphite")
	allow.Deny(domain.DefaultThemeID)
	_, err = svc.Select(ctx, &ws, actor, nil, nil)
	require.NoError(t, err, "resetting to the standard theme is always allowed")
	_, err = svc.Select(ctx, &ws, actor, strp(domain.DefaultThemeID), nil)
	require.NoError(t, err)
}
