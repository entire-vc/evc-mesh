package extensions

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

func TestLocaleFromAcceptLanguage(t *testing.T) {
	cases := map[string]string{
		"":                        "en",
		"en":                      "en",
		"ru-RU,ru;q=0.9,en;q=0.8": "ru",
		"de-DE,de;q=0.9,ru;q=0.5": "ru",
		"de, fr;q=0.8":            "en",
		"en;q=0.3, ru;q=0.9":      "ru",
		"ru;q=0, en":              "en",
		"*":                       "en",
	}
	for in, want := range cases {
		if got := LocaleFromAcceptLanguage(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestLocalizeJSON(t *testing.T) {
	var v any
	_ = json.Unmarshal([]byte(`{"manifest":{"title":"Token","title_i18n":{"ru":"Токен"},"a":[{"description":"d","description_i18n":{"ru":"о"}}],"x":{"title":"Plain","title_i18n":{"ru":"Обычный"}}}}`), &v)
	en, _ := json.Marshal(LocalizeJSON(clone(v), "en"))
	if string(en) != `{"manifest":{"a":[{"description":"d"}],"title":"Token","x":{"title":"Plain"}}}` {
		t.Fatalf("en: %s", en)
	}
	ru, _ := json.Marshal(LocalizeJSON(clone(v), "ru"))
	if string(ru) != `{"manifest":{"a":[{"description":"о"}],"title":"Токен","x":{"title":"Обычный"}}}` {
		t.Fatalf("ru: %s", ru)
	}
}

func clone(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// Builtin manifests must show no Cyrillic in an English interface.
func TestBuiltinManifestsEnglishWithoutCyrillic(t *testing.T) {
	cyr := regexp.MustCompile(`[\x{0400}-\x{04FF}]`)
	for _, name := range []string{"github", "runner"} {
		raw, err := os.ReadFile(name + "/mesh-extension.json")
		if err != nil {
			t.Fatal(err)
		}
		var m any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		en, _ := json.Marshal(LocalizeJSON(map[string]any{"manifest": m}, "en"))
		if cyr.Match(en) {
			t.Errorf("%s: Cyrillic in en manifest: %s", name, en)
		}
	}
}

func TestLocalizeManifestTextRU(t *testing.T) {
	v := map[string]any{"id": "github", "description": "en", "manifest": map[string]any{"id": "github", "description": "en", "capabilities": map[string]any{"network": map[string]any{"reasoning": "en"}}}}
	out, _ := json.Marshal(LocalizeJSON(v, "ru"))
	if !regexp.MustCompile(`[\x{0400}-\x{04FF}]`).Match(out) || regexp.MustCompile(`"en"`).Match(out) {
		t.Fatalf("ru not applied: %s", out)
	}
}

// User settings in a response must never be rewritten by manifest i18n.
func TestLocalizeJSONLeavesSettingsAlone(t *testing.T) {
	in := `{"connection":{"settings":{"label":"a","label_i18n":{"ru":"б"}}},"manifest":{"title":"T","title_i18n":{"ru":"Т"}}}`
	var v any
	_ = json.Unmarshal([]byte(in), &v)
	out, _ := json.Marshal(LocalizeJSON(v, "ru"))
	want := `{"connection":{"settings":{"label":"a","label_i18n":{"ru":"б"}}},"manifest":{"title":"Т"}}`
	if string(out) != want {
		t.Fatalf("got %s", out)
	}
}
