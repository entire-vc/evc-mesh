/**
 * Ink measurement for rail glyphs — the one definition, shared (`#13ff4803`).
 *
 * Two callers need this: `scripts/assert-rail-icon-size.mjs` (the guard that
 * refuses a mark sized unlike its neighbours) and
 * `web/scripts/local-stack/measure-rail-ink.mjs` (the evidence run that reports
 * every glyph in the real collapsed rail). A guard and the evidence for it
 * disagreeing about how a measurement is taken is worse than having only one
 * of them, so the arithmetic lives here once.
 *
 * WHAT "INK" MEANS AND WHY THE <svg> BOX IS NOT ENOUGH
 * An <svg> centred by flexbox is centred by construction — measuring its
 * element box can only confirm the layout engine works. What a person sees is
 * the shape painted inside it, and two svgs of identical box size paint very
 * differently-sized shapes when they fill their viewBoxes to different
 * extents: this app's workspace mark spans its full viewBox width, while
 * lucide draws inside a 20-of-24 design box. Three prior measurement passes
 * over this rail reported box geometry, found it flawless (it is), and missed
 * a mark a quarter wider than every neighbour.
 *
 * Stroked shapes paint half their stroke width OUTSIDE the geometric bbox, so
 * that half is added back — without it every lucide icon under-reports by one
 * stroke width and the filled mark does not, which would bias the exact
 * comparison this exists to make.
 *
 * This function is passed to Playwright's `page.evaluate`, which serialises it
 * and runs it in the browser. It therefore must stay SELF-CONTAINED: no
 * imports, no closure variables, no helpers from this module. That constraint
 * is the reason it is written as one function rather than split up.
 */

/**
 * @param {SVGSVGElement} svg
 * @returns {{w:number,h:number,cx:number,cy:number}|null} ink box in page px,
 *   or null when the svg paints nothing drawable.
 */
export function inkBox(svg) {
  const s = svg.getBoundingClientRect();
  let x0 = Infinity;
  let y0 = Infinity;
  let x1 = -Infinity;
  let y1 = -Infinity;
  for (const g of svg.querySelectorAll(
    "path,circle,rect,line,polyline,polygon,ellipse",
  )) {
    let b;
    try {
      b = g.getBBox();
    } catch {
      continue;
    }
    if (!b || (!b.width && !b.height)) continue;
    const cs = getComputedStyle(g);
    const pad =
      cs.stroke && cs.stroke !== "none" ? (parseFloat(cs.strokeWidth) || 0) / 2 : 0;
    x0 = Math.min(x0, b.x - pad);
    y0 = Math.min(y0, b.y - pad);
    x1 = Math.max(x1, b.x + b.width + pad);
    y1 = Math.max(y1, b.y + b.height + pad);
  }
  if (x0 === Infinity) return null;
  const vb = svg.viewBox.baseVal;
  const vbW = vb && vb.width ? vb.width : s.width;
  const vbH = vb && vb.height ? vb.height : s.height;
  // preserveAspectRatio defaults to "meet": uniform scale, letterboxed.
  const k = Math.min(s.width / vbW, s.height / vbH);
  const drawnW = vbW * k;
  const drawnH = vbH * k;
  const offX = s.left + (s.width - drawnW) / 2 - (vb ? vb.x : 0) * k;
  const offY = s.top + (s.height - drawnH) / 2 - (vb ? vb.y : 0) * k;
  const px0 = offX + x0 * k;
  const px1 = offX + x1 * k;
  const py0 = offY + y0 * k;
  const py1 = offY + y1 * k;
  return {
    w: +(px1 - px0).toFixed(2),
    h: +(py1 - py0).toFixed(2),
    cx: +((px0 + px1) / 2).toFixed(2),
    cy: +((py0 + py1) / 2).toFixed(2),
  };
}

/**
 * Optical size of an ink box: the geometric mean of its two axes.
 *
 * Single-axis comparison cannot work here. The workspace mark is wide and flat
 * (aspect ~1.4:1) while lucide icons are square, so matching its width leaves
 * it too short and matching its height leaves it too wide. The geometric mean
 * is the size a viewer reads for a shape of either proportion, which is the
 * quantity the rail actually needs to hold constant.
 */
export function opticalSize(ink) {
  return Math.sqrt(ink.w * ink.h);
}

/**
 * How a caller ships `inkBox` into the page.
 *
 * `page.evaluate(fn, arg)` serialises `fn` but not its module scope, so an
 * imported helper is not visible inside the browser callback. Passing the
 * source text as an argument and rebuilding it there is what keeps both
 * callers on one definition instead of each pasting its own copy — the copy
 * being the failure mode this module exists to prevent.
 *
 * Caller:  page.evaluate(({ inkSrc }) => {
 *            const inkBox = new Function("return " + inkSrc)();
 *            ...
 *          }, { inkSrc: inkBoxSource() });
 */
export function inkBoxSource() {
  return inkBox.toString();
}
