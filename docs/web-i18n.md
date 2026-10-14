# Web i18n: adding strings and languages

Mesh web uses `i18next` + `react-i18next`. `en` is the source and the fallback;
`ru` is the second language. Spec: Mesh Docs "i18n", section 2.3 (card #d807c72f).

## Layout

```
web/src/locales/<lang>/<namespace>.json   one namespace per section of the app
web/src/i18n/config.ts                    supported languages, language resolution
web/src/i18n/index.ts                     i18next init, setLanguage()
web/src/i18n/format.ts                    the ONLY place for date / number / relative-time formatting
web/src/i18n/use-language-sync.ts         users.locale -> UI language, live
```

Keys look like `board.filter.reallyWorking` (nested JSON, namespace = file name,
e.g. `board.json` -> `t("board:filter.reallyWorking")`).

## Add a string

1. Add the key to `locales/en/<ns>.json` (English text).
2. Add the same key to `locales/ru/<ns>.json`.
3. In the component: `const { t } = useTranslation("<ns>"); ... {t("filter.reallyWorking")}`.
   Interpolation: `t("greeting", { name })` with `"greeting": "Hello, {{name}}"`.
   Plurals: `key_one` / `key_few` / `key_many` / `key_other` (Intl.PluralRules rules).

`pnpm test` fails when an `en` key is missing in `ru` (or `ru` has a key `en`
lacks): `src/i18n/__tests__/i18n.test.ts`.

## Add a language

1. `web/src/locales/<code>/` with every namespace of `en`.
2. One entry in `LANGUAGES` (`web/src/i18n/config.ts`) with its Intl tag, and its
   date-fns locale in `format.ts`; a display name under `common.language.<code>` in every language.
3. Widen the server list and CHECK constraints (see L1.1: `internal/domain/locale.go`).

## Dates, numbers, relative time

Use `@/i18n/format` (`formatDate`, `formatDateTime`, `formatTime`,
`formatNumber`, `formatRelativeTime`). Never call `toLocale*String` or
`Intl.NumberFormat/DateTimeFormat` in components: they take the browser's
locale or a hard-coded `en-US` instead of the interface language.
`src/lib/__tests__/pinned-locale-number-format.test.ts` fails on it.
Changing the language remounts the route tree (`App.tsx`), so values already on
screen re-format without a reload.

## CI gate: no Cyrillic outside `locales/ru`

`pnpm check:i18n` (job `test:web`) looks for Cyrillic in `web/src/**/*.{ts,tsx}`,
skipping `locales/`, tests, comments and the transliteration table in
`lib/utils.ts`. Strict: every hit fails. There is no baseline file; the only exemption is the
Cyrillic->Latin table rows in `lib/utils.ts` (`ALLOWLIST` in
`scripts/cyrillic-gate-lib.mjs`). Move a string to `locales/` instead of exempting it.

Red control: `src/__tests__/cyrillic-gate.test.ts` plants a Russian string and
expects exit 1.
