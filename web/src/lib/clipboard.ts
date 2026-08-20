/**
 * Copy text to the clipboard, everywhere the app actually runs.
 *
 * `navigator.clipboard` is undefined on a non-secure origin, and a self-hosted
 * Mesh is routinely reached over plain http, so the deprecated fallback is not
 * legacy politeness — it is the path that runs on those instances.
 */
export async function copyText(value: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(value);
    return;
  } catch {
    // fall through
  }
  const textArea = document.createElement("textarea");
  textArea.value = value;
  // Off-screen rather than hidden: a display:none textarea cannot be selected.
  textArea.style.position = "fixed";
  textArea.style.opacity = "0";
  document.body.appendChild(textArea);
  textArea.select();
  try {
    document.execCommand("copy");
  } finally {
    document.body.removeChild(textArea);
  }
}
