package extensions

import (
	"sort"
	"strconv"
	"strings"
)

// DefaultLocale is the language of the plain "title"/"description" values in a
// manifest; every other language lives in a sibling "<key>_i18n" object.
const DefaultLocale = "en"

var supportedLocales = map[string]bool{"en": true, "ru": true}

// LocaleFromAcceptLanguage picks the first supported primary language of an
// Accept-Language header (q-weighted), or DefaultLocale.
func LocaleFromAcceptLanguage(header string) string {
	type cand struct {
		lang string
		q    float64
		pos  int
	}
	var cands []cand
	for i, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		tag := strings.ToLower(strings.TrimSpace(fields[0]))
		if tag == "" {
			continue
		}
		q := 1.0
		for _, f := range fields[1:] {
			f = strings.TrimSpace(f)
			if strings.HasPrefix(f, "q=") {
				if v, err := strconv.ParseFloat(f[2:], 64); err == nil {
					q = v
				}
			}
		}
		if q <= 0 {
			continue
		}
		primary, _, _ := strings.Cut(tag, "-")
		cands = append(cands, cand{primary, q, i})
	}
	sort.SliceStable(cands, func(a, b int) bool { return cands[a].q > cands[b].q })
	for _, c := range cands {
		if supportedLocales[c.lang] {
			return c.lang
		}
	}
	return DefaultLocale
}

// LocalizeJSON resolves manifest text in a decoded API response for lang.
// Only the "manifest" subtrees are touched: inside one, every "<key>_i18n"
// object replaces "<key>" with the entry for lang (when present) and is
// dropped, so clients only ever see the resolved string. Everything else in
// the response (user settings, connection data) is returned as is.
func LocalizeJSON(v any, lang string) any {
	return localizeJSON(v, lang, false)
}

func localizeJSON(v any, lang string, inManifest bool) any {
	switch t := v.(type) {
	case map[string]any:
		if _, has := t["manifest"]; has || inManifest {
			localizeManifestText(t, lang)
		}
		for k, val := range t {
			if inManifest {
				if base, ok := strings.CutSuffix(k, "_i18n"); ok {
					if m, isMap := val.(map[string]any); isMap {
						// A string, or a whole object for a map of labels
						// ("x-options_i18n": {"ru": {"api": "API"}}).
						switch tr := m[lang].(type) {
						case string:
							if tr != "" {
								t[base] = tr
							}
						case map[string]any:
							if len(tr) > 0 {
								t[base] = tr
							}
						}
					}
					delete(t, k)
					continue
				}
			}
			t[k] = localizeJSON(val, lang, inManifest || k == "manifest")
		}
		return t
	case []any:
		for i := range t {
			t[i] = localizeJSON(t[i], lang, inManifest)
		}
		return t
	}
	return v
}

// manifestTextRU holds the Russian text of manifest fields the frozen
// manifest v1 schema gives no "_i18n" sibling for (top-level description,
// network reasoning). Field titles use "title_i18n" inside the manifest.
var manifestTextRU = map[string]struct{ Description, Reasoning string }{
	"github": {
		Description: "Привязка pull request'ов GitHub к задачам: вебхук и проверка статуса PR",
		Reasoning:   "проверка статуса PR через API GitHub",
	},
	"team-relay": {
		Description: "Синхронизация Mesh Docs с шарами Team Relay: подключение к релею и привязка проекта к шаре",
		Reasoning:   "синк документов и артефактов с релеем по адресу из настроек",
	},
	"gitlab": {
		Description: "Привязка merge request'ов GitLab к задачам: вебхук, статус MR и пайплайнов",
		Reasoning:   "проверка статуса MR и пайплайнов через API GitLab инстанса",
	},
	"spark": {
		Description: "Каталог ассетов Spark: поиск, популярное, установка агентов в воркспейс",
	},
	"telegram": {
		Description: "Telegram-уведомления: бот воркспейса, привязка людей командой /start, доставка событий",
	},
	"agent-chat": {
		Description: "Чат с агентом: личные треды пользователя с агентом проекта, которого обслуживает раннер воркспейса",
	},
	"runner": {
		Description: "Runner: каталог ресурсов запуска агентов — пулы, аккаунты, профили, привязки и контроллеры; допуск и резервирование",
	},
}

// localizeManifestText applies manifestTextRU to a manifest object or to a
// catalog summary ({id, description, manifest}) when lang is "ru".
func localizeManifestText(m map[string]any, lang string) {
	id, _ := m["id"].(string)
	tr, ok := manifestTextRU[id]
	if !ok || lang != "ru" {
		return
	}
	if tr.Description != "" {
		if _, has := m["description"].(string); has {
			m["description"] = tr.Description
		}
	}
	if tr.Reasoning != "" {
		caps, _ := m["capabilities"].(map[string]any)
		if net, _ := caps["network"].(map[string]any); net != nil {
			net["reasoning"] = tr.Reasoning
		}
	}
}
