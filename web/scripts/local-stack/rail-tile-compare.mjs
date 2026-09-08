import { chromium } from "@playwright/test";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";

const WEB = "/Users/entirevc/DevProjects/evc-mesh-verne/web";
const assets = path.join(WEB, "dist/assets");
const cssFile = readdirSync(assets).filter(f => f.endsWith(".css")).sort((a,b)=>{
  const sa = readFileSync(path.join(assets,a)).length;
  const sb = readFileSync(path.join(assets,b)).length;
  return sb-sa;
})[0];
const css = readFileSync(path.join(assets, cssFile), "utf8");

const { cn } = await import(path.join(WEB, "src/lib/cn.ts"));
const recipes = await import(path.join(WEB, "src/components/layout/rail-icon-classes.ts"));

// three recipes: pre-MR!899 original (bare letter, transparent at rest),
// MR!899 v2 (teal fill, the one Pavel rejected), and the new fix (live).
function preMR899Parts(isActive) {
  return [
    "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
    isActive ? "bg-sidebar-accent text-sidebar-primary" : "",
  ];
}
function mr899v2Parts(isActive) {
  // the shipped-then-rejected recipe: workspaceLogoContainerParts(variant:collapsed, isLoaded:false, className: old state classes)
  return [
    "flex shrink-0 items-center justify-center overflow-hidden text-primary-foreground",
    "h-8 w-8 rounded-lg",
    "bg-sidebar-primary",
    [
      "hover:bg-sidebar-accent hover:text-sidebar-primary",
      isActive ? "bg-sidebar-accent text-sidebar-primary" : "",
    ].filter(Boolean).join(" "),
  ];
}

const RECIPES = [
  { label: "pre-MR!899 («было все ок»)", fn: preMR899Parts },
  { label: "MR!899 v2 (Pavel отверг 20:26)", fn: mr899v2Parts },
  { label: "новый фикс (#119078b0)", fn: recipes.projectRailIconParts },
];

const browser = await chromium.launch({ executablePath: process.env.RAIL_CONTRAST_CHROMIUM });
const results = [];
for (const dark of [false, true]) {
  const page = await browser.newPage({ viewport: { width: 900, height: 260 } });
  const rows = RECIPES.map(({label, fn}) => {
    const restClass = cn(fn(false));
    const activeClass = cn(fn(true));
    return `<div style="display:flex;align-items:center;gap:16px;margin-bottom:18px;">
      <div style="width:230px;font:13px sans-serif;color:${dark ? '#ccc' : '#333'}">${label}</div>
      <div class="${restClass}"><span class="${recipes.PROJECT_RAIL_ICON_LABEL}">M</span></div>
      <div class="${restClass}"><span class="${recipes.PROJECT_RAIL_ICON_LABEL}">T</span></div>
      <div class="${activeClass}"><span class="${recipes.PROJECT_RAIL_ICON_LABEL}">A</span></div>
      <div style="font:12px sans-serif;color:${dark?'#999':'#777'}">(rest, rest, active)</div>
    </div>`;
  }).join("\n");
  await page.setContent(
    `<!doctype html><html class="${dark ? 'dark' : ''}"><head><style>${css}</style></head>` +
    `<body style="margin:0"><div class="bg-sidebar" style="padding:24px 24px 6px">${rows}</div></body></html>`,
    { waitUntil: "load" },
  );
  const out = `/tmp/rail-shots-verne/rail-tile-compare-${dark ? "dark" : "light"}.png`;
  await page.screenshot({ path: out });
  results.push(out);
  await page.close();
}
await browser.close();
console.log(JSON.stringify(results, null, 2));
