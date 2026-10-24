package themes

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// MinTextContrast is the WCAG AA floor for body text.
const MinTextContrast = 4.5

// contrastPairs are the foreground/background token pairs of the contract
// (token-contract-v1, "Проверка контраста").
var contrastPairs = [][2]string{
	{"foreground", "background"},
	{"card-foreground", "card"},
	{"popover-foreground", "popover"},
	{"primary-foreground", "primary"},
	{"secondary-foreground", "secondary"},
	{"muted-foreground", "muted"},
	{"accent-foreground", "accent"},
	{"destructive-foreground", "destructive"},
	{"success-foreground", "success"},
	{"warning-foreground", "warning"},
	{"info-foreground", "info"},
	{"sidebar-foreground", "sidebar-background"},
	{"sidebar-primary-foreground", "sidebar-primary"},
	{"sidebar-accent-foreground", "sidebar-accent"},
}

// ContrastWarning is one pair below the AA floor. It is advice, never a
// refusal: a theme's own style is its author's responsibility.
type ContrastWarning struct {
	Mode  string  `json:"mode"`
	Pair  string  `json:"pair"`
	Ratio float64 `json:"ratio"`
	Min   float64 `json:"min"`
}

func (w ContrastWarning) String() string {
	return fmt.Sprintf("%s: %s %.2f:1 (< %.1f:1)", w.Mode, w.Pair, w.Ratio, w.Min)
}

// defaultPrimaryPair is what mesh-default resolves primary-foreground and
// primary to per mode (web/src/styles/brandkit.css). A theme or an accent
// setting that sets only one side is judged against the other side's default,
// the way the browser renders it.
var defaultPrimaryPair = map[string]map[string]string{
	"light": {"primary": "180 100% 21%", "primary-foreground": "0 0% 100%"},
	"dark":  {"primary": "172 66% 50%", "primary-foreground": "180 45% 4%"},
}

// CheckContrast measures every pair of which the theme sets at least one token
// in a mode. For the primary pair a side the theme leaves to mesh-default is
// taken from that default; any other pair with a missing side cannot be judged
// here and is skipped (the default is held to the floor by the web tests).
func CheckContrast(tokens map[string]map[string]string) []ContrastWarning {
	var out []ContrastWarning
	modes := make([]string, 0, len(tokens))
	for mode := range tokens {
		modes = append(modes, mode)
	}
	sort.Strings(modes)
	for _, mode := range modes {
		set := tokens[mode]
		for _, p := range contrastPairs {
			fg, fok := set[p[0]]
			bg, bok := set[p[1]]
			if def, ok := defaultPrimaryPair[mode]; ok && (fok || bok) {
				if p[0] == "primary-foreground" {
					if !fok {
						fg, fok = def[p[0]], true
					}
					if !bok {
						bg, bok = def[p[1]], true
					}
				}
			}
			if !fok || !bok {
				continue
			}
			a, aok := parseHSL(fg)
			b, bok2 := parseHSL(bg)
			if !aok || !bok2 {
				continue
			}
			if r := contrastRatio(a, b); r < MinTextContrast {
				out = append(out, ContrastWarning{Mode: mode, Pair: p[0] + "/" + p[1], Ratio: math.Round(r*100) / 100, Min: MinTextContrast})
			}
		}
	}
	return out
}

type hsl struct{ h, s, l float64 }

// parseHSL reads "H S% L%" (brandkit.css notation).
func parseHSL(v string) (hsl, bool) {
	f := strings.Fields(strings.ReplaceAll(v, "%", ""))
	if len(f) != 3 {
		return hsl{}, false
	}
	var n [3]float64
	for i := range f {
		x, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return hsl{}, false
		}
		n[i] = x
	}
	return hsl{n[0], n[1] / 100, n[2] / 100}, true
}

func (c hsl) rgb() [3]float64 {
	k := (1 - math.Abs(2*c.l-1)) * c.s
	x := k * (1 - math.Abs(math.Mod(c.h/60, 2)-1))
	m := c.l - k/2
	var r [3]float64
	switch int(math.Floor(c.h/60)) % 6 {
	case 0:
		r = [3]float64{k, x, 0}
	case 1:
		r = [3]float64{x, k, 0}
	case 2:
		r = [3]float64{0, k, x}
	case 3:
		r = [3]float64{0, x, k}
	case 4:
		r = [3]float64{x, 0, k}
	default:
		r = [3]float64{k, 0, x}
	}
	return [3]float64{r[0] + m, r[1] + m, r[2] + m}
}

func (c hsl) luminance() float64 {
	var lin [3]float64
	for i, v := range c.rgb() {
		if v <= 0.04045 {
			lin[i] = v / 12.92
		} else {
			lin[i] = math.Pow((v+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*lin[0] + 0.7152*lin[1] + 0.0722*lin[2]
}

func contrastRatio(a, b hsl) float64 {
	la, lb := a.luminance(), b.luminance()
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}
