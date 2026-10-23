package themes

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Brand is what a theme looks like outside the app: the browser chrome colour,
// the PWA manifest and the emails. Every field is a #rrggbb colour and is never
// empty — a token the theme does not set (or sets to something unreadable)
// falls back to the mesh-default value for that field, so a theme that sets
// only `primary` still yields a complete Brand.
type Brand struct {
	// ThemeColor is <meta name="theme-color"> in light mode (the primary
	// colour); ThemeColorDark is the same for dark mode.
	ThemeColor     string `json:"theme_color"`
	ThemeColorDark string `json:"theme_color_dark"`
	// BackgroundColor is the manifest's background_color (splash screen).
	BackgroundColor string `json:"background_color"`
	// Light-mode palette for emails, which have no dark variant.
	Primary           string `json:"primary"`
	PrimaryForeground string `json:"primary_foreground"`
	Background        string `json:"background"`
	Foreground        string `json:"foreground"`
	MutedForeground   string `json:"muted_foreground"`
	Border            string `json:"border"`
}

// DefaultBrand is the brand of mesh-default: the values of
// web/src/styles/brandkit.css, kept as the same HSL triplets and converted the
// way a theme's are, so the two cannot drift in rounding.
func DefaultBrand() Brand {
	hex := func(v string) string { h, _ := hslToHex(v); return h }
	return Brand{
		ThemeColor:        hex("180 100% 21%"),
		ThemeColorDark:    hex("172 66% 50%"),
		BackgroundColor:   hex("0 0% 100%"),
		Primary:           hex("180 100% 21%"),
		PrimaryForeground: hex("0 0% 100%"),
		Background:        hex("0 0% 100%"),
		Foreground:        hex("180 23% 8%"),
		MutedForeground:   hex("180 10% 40%"),
		Border:            hex("180 28% 90%"),
	}
}

// BrandOf derives the Brand from a theme's tokens (mode → token → "H S% L%").
func BrandOf(tokens map[string]map[string]string) Brand {
	b := DefaultBrand()
	light, dark := tokens["light"], tokens["dark"]
	// A theme with one mode is applied to both, as BuildTokensCSS does.
	if len(light) == 0 {
		light = dark
	}
	if len(dark) == 0 {
		dark = light
	}
	pick := func(dst *string, set map[string]string, name string) {
		if hex, ok := hslToHex(set[name]); ok {
			*dst = hex
		}
	}
	pick(&b.Primary, light, "primary")
	pick(&b.PrimaryForeground, light, "primary-foreground")
	pick(&b.Background, light, "background")
	pick(&b.Foreground, light, "foreground")
	pick(&b.MutedForeground, light, "muted-foreground")
	pick(&b.Border, light, "border")
	b.ThemeColor = b.Primary
	b.BackgroundColor = b.Background
	pick(&b.ThemeColorDark, dark, "primary")
	// Only a theme that sets neither mode's primary keeps the default dark colour.
	if _, ok := hslToHex(dark["primary"]); !ok && len(light) > 0 {
		b.ThemeColorDark = b.Primary
	}
	return b
}

// hslToHex converts the contract's "H S% L%" colour to #rrggbb.
func hslToHex(v string) (string, bool) {
	if !colorValue.MatchString(strings.TrimSpace(v)) {
		return "", false
	}
	f := strings.Fields(strings.ReplaceAll(strings.TrimSpace(v), "%", ""))
	if len(f) != 3 {
		return "", false
	}
	h, e1 := strconv.ParseFloat(f[0], 64)
	s, e2 := strconv.ParseFloat(f[1], 64)
	l, e3 := strconv.ParseFloat(f[2], 64)
	if e1 != nil || e2 != nil || e3 != nil || s > 100 || l > 100 {
		return "", false
	}
	h = math.Mod(h, 360) / 360
	s, l = s/100, l/100
	var r, g, bl float64
	if s == 0 {
		r, g, bl = l, l, l
	} else {
		q := l + s - l*s
		if l < 0.5 {
			q = l * (1 + s)
		}
		p := 2*l - q
		hue := func(t float64) float64 {
			t = math.Mod(t+1, 1)
			switch {
			case t < 1.0/6:
				return p + (q-p)*6*t
			case t < 0.5:
				return q
			case t < 2.0/3:
				return p + (q-p)*(2.0/3-t)*6
			}
			return p
		}
		r, g, bl = hue(h+1.0/3), hue(h), hue(h-1.0/3)
	}
	c := func(x float64) int { return int(math.Round(x * 255)) }
	return fmt.Sprintf("#%02x%02x%02x", c(r), c(g), c(bl)), true
}
