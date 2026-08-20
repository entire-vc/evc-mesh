// Produces pkg/mdoc/testdata/frontend_anchors.json by running the REAL
// web/src/lib/doc-comments/anchor.ts over a set of selections.
//
// ## Why this exists
//
// pkg/mdoc/anchor.go and web/src/lib/doc-comments/anchor.ts resolve the same
// quotes to the same anchors, and nothing in either toolchain will tell you when
// they stop doing so. Two implementations that drift put an agent's comment and a
// human's comment on the same sentence in different places, and nobody notices
// until somebody sees two highlights where there should be one.
//
// So the numbers the Go test asserts are not written by hand from the same
// understanding that wrote the Go code — they come out of the TypeScript, and are
// checked in. A Go test that reproduces them is evidence. A Go test full of
// numbers a Go author chose is not.
//
// ## Running it
//
//	npx tsc --outDir /tmp/anchorjs --module CommonJS --moduleResolution bundler \
//	    --target ES2022 web/src/lib/doc-comments/anchor.ts web/src/lib/doc-comments/offsets.ts
//	node scripts/gen-anchor-fixture.mjs > pkg/mdoc/testdata/frontend_anchors.json
//
// anchor.ts imports its anchor shape from "@/types" as a TYPE only, so it does
// not survive compilation and needs no module at runtime.
//
// ## When the output changes
//
// A diff in the fixture after regenerating means the two implementations now
// disagree about something. That is a decision — which of them is right — and not
// a re-baseline.

import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";

const ANCHOR_JS = process.env.ANCHOR_JS ?? "/tmp/anchorjs/anchor.js";
const ANCHOR_TS = process.env.ANCHOR_TS ?? "web/src/lib/doc-comments/anchor.ts";
const OFFSETS_TS = process.env.OFFSETS_TS ?? "web/src/lib/doc-comments/offsets.ts";

const { buildAnchorFromSelection } = createRequire(import.meta.url)(ANCHOR_JS);

const RU = [
  "# Регламент дежурства",
  "",
  "Дежурный инженер отвечает за приём инцидентов в рабочие часы и за то, чтобы",
  "каждое обращение получило ответ в течение пятнадцати минут. Если инцидент",
  "затрагивает продакшн, дежурный обязан немедленно поднять эскалацию и позвать",
  "владельца сервиса, не дожидаясь окончания диагностики.",
  "",
  "## Эскалация",
  "",
  "Эскалация считается открытой с момента публикации сообщения в канале инцидентов.",
  "Владелец сервиса подтверждает получение, после чего дежурный передаёт контекст",
  "и остаётся на связи до закрытия инцидента.",
  "",
  "## Постмортем",
  "",
  "Владелец сервиса пишет постмортем в течение двух рабочих дней и приносит его",
  "на еженедельный разбор команды.",
].join("\n");

const cases = [
  {
    name: "plain_english",
    note: "Plain prose: the markdown and the rendered text are the same string.",
    source:
      "# Runbook\n\nThe deploy gate refuses a release when the migration check has not run.\nRe-run it from the pipeline and try again.\n",
    select: "the migration check has not run",
  },
  {
    name: "cyrillic_deep",
    note:
      "The mandatory one. A Russian phrase far enough into the document that its " +
      "byte offset and its character offset are visibly different numbers.",
    source: RU,
    select: "дежурный обязан немедленно поднять эскалацию",
  },
  {
    name: "cyrillic_repeated_phrase",
    note:
      "The same Russian phrase twice; only the surrounding context tells them " +
      "apart. This one is the SECOND occurrence.",
    source: RU,
    select: "Владелец сервиса",
    after: "## Постмортем",
  },
  {
    name: "markup_bold",
    note:
      "The selection crosses inline markup, so the rendered text and the markdown " +
      "are different strings and the tolerant scan is what places it.",
    source: "Set **PUBLIC_API_URL** before building, or the bundle points at localhost.\n",
    rendered: "Set PUBLIC_API_URL before building, or the bundle points at localhost.\n",
    select: "PUBLIC_API_URL before building",
  },
  {
    name: "markup_link",
    note: "The selection crosses a markdown link, whose tail is skipped as one unit.",
    source: "See the [deploy guide](https://example.com/deploy) for the rollback order.\n",
    rendered: "See the deploy guide for the rollback order.\n",
    select: "deploy guide for the rollback",
  },
  {
    name: "at_document_start",
    note: "Quote at offset 0, so the anchor has no prefix at all.",
    source: "Rollback is always safe here.\n\nThe migration is additive.\n",
    select: "Rollback is always safe",
  },
  {
    name: "at_document_end",
    note: "Quote running to the last character, so the anchor has no suffix.",
    source: "The migration is additive.\n\nRollback is always safe here.",
    select: "Rollback is always safe here.",
  },
  {
    name: "emoji_before_quote",
    note:
      "An astral-plane character sits before the quote: two UTF-16 units, four " +
      "bytes, one rune. Every conversion in the chain has to agree.",
    source: "Status: 🚀 shipped to production on Tuesday.\n",
    select: "shipped to production",
  },
];

const out = [];
for (const c of cases) {
  const rendered = c.rendered ?? c.source;
  // `after` picks a later occurrence of a repeated phrase.
  const from = c.after ? rendered.indexOf(c.after) : 0;
  if (from < 0) throw new Error(`${c.name}: the "after" marker is not in the rendered text`);
  const at = rendered.indexOf(c.select, from);
  if (at < 0) throw new Error(`${c.name}: selection not present in rendered text`);
  const span = { start: at, end: at + c.select.length };

  const built = buildAnchorFromSelection(c.source, rendered, span);
  if (!built) throw new Error(`${c.name}: the frontend built no anchor`);
  if (built.unplaceable) throw new Error(`${c.name}: the frontend could not place the quote`);

  out.push({
    name: c.name,
    note: c.note,
    source: c.source,
    // True when the markdown and what the reader sees are the same string. Only
    // then are the frontend's context strings comparable with the server's, which
    // reads them out of the markdown.
    same_space: rendered === c.source,
    selection: c.select,
    // How many times the quote occurs verbatim in the markdown. More than one
    // means the context strings are the only thing that placed it.
    occurrences: c.source.split(c.select).length - 1,
    anchor: built.anchor,
  });
}

const sha = (p) => createHash("sha256").update(readFileSync(p)).digest("hex");

console.log(
  JSON.stringify(
    {
      _readme:
        "Generated by running web/src/lib/doc-comments/anchor.ts (buildAnchorFromSelection) " +
        "over each selection. The offsets here are the frontend's, not the Go " +
        "implementation's, which is the only reason comparing them proves anything. " +
        "Regenerate whenever anchor.ts changes; a diff here is a drift between the two " +
        "implementations and needs a decision, not a re-baseline.",
      _source_sha256: {
        [ANCHOR_TS]: sha(ANCHOR_TS),
        [OFFSETS_TS]: sha(OFFSETS_TS),
      },
      cases: out,
    },
    null,
    2,
  ),
);
