package themes

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ThemeLayer is the cascade layer the CSS of a theme is served in (ADR D4):
// declared last, so it can recolour the core's utility classes.
const ThemeLayer = "mesh-theme"

// BuildTokensCSS renders tokens as `:root{…} .dark{…}`. overrides (token →
// value) come from the theme's settings and win over the theme's own values in
// both modes. A theme that declares one mode only is applied to both: there is
// no automatic inversion (ADR D3). No tokens at all gives "" (mesh-default).
func BuildTokensCSS(tokens map[string]map[string]string, overrides map[string]string) string {
	light, dark := tokens["light"], tokens["dark"]
	switch {
	case len(light) > 0 && len(dark) == 0:
		dark = light
	case len(dark) > 0 && len(light) == 0:
		light = dark
	}
	var b strings.Builder
	for _, block := range []struct {
		sel  string
		vals map[string]string
	}{{":root", light}, {".dark", dark}} {
		merged := map[string]string{}
		for k, v := range block.vals {
			merged[k] = v
		}
		for k, v := range overrides {
			merged[k] = v
		}
		if len(merged) == 0 {
			continue
		}
		names := make([]string, 0, len(merged))
		for k := range merged {
			names = append(names, k)
		}
		sort.Strings(names)
		b.WriteString(block.sel + "{")
		for _, k := range names {
			b.WriteString("--" + k + ":" + strings.TrimSpace(merged[k]) + ";")
		}
		b.WriteString("}\n")
	}
	return b.String()
}

// WrapThemeCSS puts a theme's stylesheet into the mesh-theme layer, so rules
// written without a layer cannot slip past the cascade order.
func WrapThemeCSS(css []byte) []byte {
	return []byte("@layer " + ThemeLayer + " {\n" + string(css) + "\n}\n")
}

var (
	cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	cssEscape  = regexp.MustCompile(`\\([0-9a-fA-F]{1,6}[ \t\r\n]?|[^\r\n0-9a-fA-F])`)
	cssURL     = regexp.MustCompile(`(?i)url\(\s*['"]?\s*([^'")\s]*)`)
)

// decodeCSSEscapes resolves \75rl-style escapes so the checks below cannot be
// slipped past by spelling a keyword differently.
func decodeCSSEscapes(s string) string {
	return cssEscape.ReplaceAllStringFunc(s, func(m string) string {
		body := strings.TrimSpace(m[1:])
		if n, err := strconv.ParseInt(body, 16, 32); err == nil && body != "" && isHex(body) {
			return string(rune(n))
		}
		return m[1:]
	})
}

func isHex(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// CheckCSSPolicy enforces the install-time limits of ADR D4: no @import and no
// url() to anywhere but a data: URI or a fragment, so a theme cannot make a
// user's session load anything from another host. Own CSS is otherwise free.
func CheckCSSPolicy(css []byte) error {
	text := decodeCSSEscapes(cssComment.ReplaceAllString(string(css), ""))
	low := strings.ToLower(text)
	switch {
	case strings.Contains(low, "@import"):
		return fmt.Errorf("@import is not allowed in theme CSS")
	case strings.Contains(low, "image-set("), strings.Contains(low, "cross-fade("):
		return fmt.Errorf("image-set() and cross-fade() are not allowed in theme CSS")
	case strings.Contains(low, "</style"), strings.Contains(low, "<script"):
		return fmt.Errorf("markup is not allowed in theme CSS")
	}
	for _, m := range cssURL.FindAllStringSubmatch(text, -1) {
		target := strings.ToLower(m[1])
		if !strings.HasPrefix(target, "data:") && !strings.HasPrefix(target, "#") {
			return fmt.Errorf("url(%s) is not allowed: only data: URIs and fragments", m[1])
		}
	}
	return nil
}

// Hash is the identity of what a resolved theme sends to the browser; it is
// the ETag of GET /theme.
func Hash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(strconv.Itoa(len(p)) + ":"))
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}
