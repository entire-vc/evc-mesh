import "@testing-library/jest-dom";

// jsdom does not implement matchMedia at all — any component that reads a
// breakpoint via `window.matchMedia` (e.g. `useIsMobile`) throws on mount
// under every test that never touches it directly. Default to "not matching"
// (desktop / no narrow-viewport override) so existing tests, written before
// any component called matchMedia, keep exercising the same lg+ layout they
// always did without needing to know this exists. A test that specifically
// needs the narrow-viewport branch replaces `window.matchMedia` itself before
// rendering — this default only has to not crash them, not anticipate them.
if (typeof window !== "undefined" && !window.matchMedia) {
  window.matchMedia = (query: string): MediaQueryList => {
    const target = new EventTarget();
    return Object.assign(target, {
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {}, // deprecated API some older callers still probe for
      removeListener: () => {},
      addEventListener: target.addEventListener.bind(target),
      removeEventListener: target.removeEventListener.bind(target),
      dispatchEvent: target.dispatchEvent.bind(target),
    }) as MediaQueryList;
  };
}
