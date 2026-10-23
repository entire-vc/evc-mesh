package themes

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildTokensCSS(t *testing.T) {
	both := map[string]map[string]string{
		"light": {"primary": "1 2% 3%", "background": "0 0% 100%"},
		"dark":  {"primary": "4 5% 6%"},
	}
	require.Equal(t, ":root{--background:0 0% 100%;--primary:1 2% 3%;}\n.dark{--primary:4 5% 6%;}\n", BuildTokensCSS(both, nil))

	// One mode declared: it serves both, no inversion is invented.
	one := map[string]map[string]string{"dark": {"primary": "4 5% 6%"}}
	require.Equal(t, ":root{--primary:4 5% 6%;}\n.dark{--primary:4 5% 6%;}\n", BuildTokensCSS(one, nil))

	require.Equal(t, ":root{--primary:9 9% 9%;}\n.dark{--primary:9 9% 9%;}\n", BuildTokensCSS(one, map[string]string{"primary": "9 9% 9%"}), "settings win")
	require.Empty(t, BuildTokensCSS(nil, nil), "no tokens means mesh-default")
	require.Equal(t, BuildTokensCSS(both, nil), BuildTokensCSS(both, nil), "deterministic")
}

func TestCheckContrast_WCAG(t *testing.T) {
	// Known ratios: black on white is 21:1, white on white 1:1.
	require.InDelta(t, 21.0, contrastRatio(hsl{0, 0, 0}, hsl{0, 0, 1}), 0.01)
	require.InDelta(t, 1.0, contrastRatio(hsl{0, 0, 1}, hsl{0, 0, 1}), 0.001)

	// The brandkit regression the TS helper documents: --accent text on --accent
	// fill at 2.82:1 in light. Here a mid grey on a lighter grey must warn.
	low := map[string]map[string]string{"light": {"foreground": "0 0% 60%", "background": "0 0% 70%"}}
	w := CheckContrast(low)
	require.Len(t, w, 1)
	require.Equal(t, "foreground/background", w[0].Pair)
	require.Equal(t, "light", w[0].Mode)
	require.Less(t, w[0].Ratio, MinTextContrast)

	// Control: a passing pair gives no warning, and a pair with one side unset is skipped.
	ok := map[string]map[string]string{"light": {"foreground": "0 0% 5%", "background": "0 0% 100%", "primary": "0 0% 100%"}}
	require.Empty(t, CheckContrast(ok))
	// 4.5:1 is the floor: just under warns, at the floor passes.
	for l := 40.0; l < 60; l += 0.5 {
		set := map[string]map[string]string{"light": {"foreground": "0 0% " + trimFloat(l) + "%", "background": "0 0% 100%"}}
		got := len(CheckContrast(set)) == 1
		ratio := contrastRatio(hsl{0, 0, l / 100}, hsl{0, 0, 1})
		require.Equal(t, ratio < MinTextContrast, got, "l=%v ratio=%v", l, ratio)
	}
}

func trimFloat(f float64) string {
	if f == math.Trunc(f) {
		return itoa(int(f))
	}
	return itoa(int(f)) + ".5"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func TestCheckContrast_IsAdviceNotRefusal(t *testing.T) {
	// A theme with failing pairs still parses: contrast never blocks.
	low := map[string]map[string]string{"light": {"foreground": "0 0% 60%", "background": "0 0% 70%"}}
	require.NotEmpty(t, CheckContrast(low))
	for _, s := range []Spec{{TokensContract: "1", Modes: []string{"light"}, Tokens: low}} {
		require.Empty(t, s.Check())
	}
}
