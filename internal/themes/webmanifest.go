package themes

import "encoding/json"

// WebManifest renders the PWA manifest of the instance (the file the browser
// reads at /manifest.json) with the colours of the theme. The product name and
// icons are not themed: the name is fixed on purpose (no white-label), and the
// icon files are static assets of the build.
func WebManifest(b Brand) []byte {
	type icon struct {
		Src     string `json:"src"`
		Sizes   string `json:"sizes"`
		Type    string `json:"type"`
		Purpose string `json:"purpose"`
	}
	out, _ := json.MarshalIndent(struct {
		Name            string `json:"name"`
		ShortName       string `json:"short_name"`
		Description     string `json:"description"`
		StartURL        string `json:"start_url"`
		Display         string `json:"display"`
		BackgroundColor string `json:"background_color"`
		ThemeColor      string `json:"theme_color"`
		Icons           []icon `json:"icons"`
	}{
		Name:            "Entire VC Mesh",
		ShortName:       "Mesh",
		Description:     "Task management for humans and AI agents",
		StartURL:        "/",
		Display:         "standalone",
		BackgroundColor: b.BackgroundColor,
		ThemeColor:      b.ThemeColor,
		Icons: []icon{
			{"/icon-192.png", "192x192", "image/png", "any"},
			{"/icon-512.png", "512x512", "image/png", "any"},
			{"/icon-maskable-192.png", "192x192", "image/png", "maskable"},
			{"/icon-maskable-512.png", "512x512", "image/png", "maskable"},
		},
	}, "", "  ")
	return append(out, '\n')
}
