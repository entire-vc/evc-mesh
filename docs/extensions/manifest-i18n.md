# Manifest text localization

Extension manifests are written in English. Other languages (today: `ru`) are
resolved by the API per request; clients always receive plain strings.

## Settings fields

Any text key inside the settings schema (`title`, `description`, `x-help`)
may have a sibling `<key>_i18n` object keyed by language:

```json
"token": {
  "type": "string",
  "title": "Token",
  "title_i18n": { "ru": "Токен" },
  "x-help": "Personal access token with api scope.",
  "x-help_i18n": { "ru": "Персональный токен со scope api." },
  "x-secret": true
}
```

The plain key is the English text and the fallback for every language without
an entry.

## Top-level description and network reasoning

The frozen manifest v1 schema has no `_i18n` sibling for the top-level
`description` and `capabilities.network.reasoning`. For builtin extensions
their Russian text lives in `manifestTextRU` in
`internal/extensions/localize.go`, keyed by extension id.

## Resolution

`extensionJSON` (`internal/handler/extension_locale.go`) picks the language
from `Accept-Language` (the web client sends the UI language) and calls
`extensions.LocalizeJSON`, which rewrites only `manifest` subtrees: each
`<key>_i18n` replaces `<key>` with the entry for that language and is dropped.
User settings in the same response are never touched.

## Guard

`TestBuiltinManifestsEnglishWithoutCyrillic` and
`TestExtensionList_AcceptLanguageEN_NoCyrillic` load every shipped
`internal/extensions/*/mesh-extension.json`: the English response must contain
no Cyrillic, and each builtin must have a Russian description in `ru`. A new
builtin extension without Russian text fails them.
