import i18n from "@/i18n";

/**
 * A `misc` namespace string for code outside React (API client, stores).
 *
 * `en` is returned until the lazy `misc` chunk has loaded: these messages can
 * surface on /login, /register and /accept-invite, which render before
 * AppLayout has loaded the app namespaces. Keep `en` equal to
 * locales/en/misc.json (src/lib/__tests__/misc-t.test.ts checks it).
 */
export function miscT(key: string, en: string, values?: Record<string, unknown>): string {
  return i18n.t(`misc:${key}`, { ...values, defaultValue: en });
}
