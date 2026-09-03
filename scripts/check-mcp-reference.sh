#!/usr/bin/env bash
# docs/mcp-reference.md is the public catalogue of the MCP tools, but the tools live in a
# DIFFERENT repository (evc-mesh-mcp). Nothing in this repo's CI can notice when a tool is
# added there, so the page drifted to 20 undocumented tools plus 4 documented tools the
# server never registered (Mesh #1f0ee22c).
#
# This compares both directions:
#   1. every "#### N. `name`" heading in the page names a tool the server registers
#   2. every tool the server registers has a heading
# and checks that the stated counts match. It fails on a mismatch in either direction.
#
# It also checks the numbers README.md states about the same catalogue — the badge is
# exactly the kind of figure that goes stale unwatched, and it was the fourth mutually
# inconsistent MCP-tool count in this repo when this check was written.
#
# The server source is fetched over HTTPS from the PUBLIC GitHub mirror of evc-mesh-mcp,
# because that needs no credentials from a CI runner. The mirror trails the GitLab canon by
# up to ~10 minutes (systemd timer, see CLAUDE-workflow.md §0z), so a tool added to canon in
# the last few minutes may not be visible here yet. For a docs catalogue that lag is
# acceptable; for anything where it is not, point MCP_SERVER_GO at a canon checkout.
#
# Set MCP_SERVER_GO to a local checkout's internal/mcp/server.go to run offline.
#
# Usage:  ./scripts/check-mcp-reference.sh [path/to/mcp-reference.md] [path/to/README.md]
set -euo pipefail

DOC="${1:-docs/mcp-reference.md}"
README="${2:-README.md}"
SERVER_URL="${MCP_SERVER_URL:-https://raw.githubusercontent.com/entire-vc/evc-mesh-mcp/main/internal/mcp/server.go}"

[[ -f "$DOC" ]] || { echo "ERROR: $DOC not found." >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# ── the server's tool list ───────────────────────────────────────────────────
if [[ -n "${MCP_SERVER_GO:-}" ]]; then
    [[ -f "$MCP_SERVER_GO" ]] || { echo "ERROR: MCP_SERVER_GO='$MCP_SERVER_GO' not found." >&2; exit 1; }
    cp "$MCP_SERVER_GO" "$tmp/server.go"
elif ! curl -fsSL --max-time 30 "$SERVER_URL" -o "$tmp/server.go"; then
    # Not reachable is not "nothing to check" — refuse rather than pass silently.
    echo "ERROR: could not fetch the MCP server source from $SERVER_URL." >&2
    echo "       Set MCP_SERVER_GO=/path/to/evc-mesh-mcp/internal/mcp/server.go to run offline." >&2
    exit 1
fi

# `|| true`: no match makes grep exit 1, which under `pipefail` would kill the script
# before it could say why. The count check below is what turns "no tools found" into a
# named refusal rather than a bare exit code.
grep -oE 'NewTool\("[a-z_]+"' "$tmp/server.go" | sed 's/.*"\(.*\)"/\1/' | sort -u > "$tmp/registered" || true
n_registered=$(wc -l < "$tmp/registered" | tr -d ' ')

# A source we fetched but could not parse is the same failure as one we could not fetch.
if [[ "$n_registered" -lt 10 ]]; then
    echo "ERROR: found only $n_registered tools in the server source — the file is not what" >&2
    echo "       this check expects. Refusing rather than reporting a spurious mismatch." >&2
    exit 1
fi

# ── the page's catalogue ─────────────────────────────────────────────────────
grep -oE '^#### [0-9]+\. `[a-z_]+`' "$DOC" | sed 's/.*`\(.*\)`/\1/' | sort > "$tmp/documented_dup" || true
sort -u "$tmp/documented_dup" > "$tmp/documented"
n_documented=$(wc -l < "$tmp/documented" | tr -d ' ')
n_headings=$(wc -l < "$tmp/documented_dup" | tr -d ' ')

fail=0

if [[ "$n_headings" -ne "$n_documented" ]]; then
    echo "FAIL: $DOC documents the same tool twice:" >&2
    uniq -d "$tmp/documented_dup" | sed 's/^/  /' >&2
    fail=1
fi

missing=$(comm -13 "$tmp/documented" "$tmp/registered")
if [[ -n "$missing" ]]; then
    echo "FAIL: registered by the server, but not documented in $DOC:" >&2
    echo "$missing" | sed 's/^/  /' >&2
    fail=1
fi

phantom=$(comm -23 "$tmp/documented" "$tmp/registered")
if [[ -n "$phantom" ]]; then
    echo "FAIL: documented in $DOC, but the server does not register it:" >&2
    echo "$phantom" | sed 's/^/  /' >&2
    fail=1
fi

# ── the numbers the page states about itself ─────────────────────────────────
if ! grep -qF "exposes **${n_registered} MCP tools**" "$DOC"; then
    echo "FAIL: the overview does not say 'exposes **${n_registered} MCP tools**'." >&2
    echo "      Server registers $n_registered. Fix the sentence in $DOC." >&2
    fail=1
fi

# The per-category table must add up to the same number.
table_sum=$(awk '
    /^\| Category \| Tools \| Description \|/ { in_table=1; next }
    in_table && /^\|-/                        { next }
    in_table && /^\|/                         { gsub(/ /,"",$3); sum+=$3; next }
    in_table                                  { exit }
    END { print sum+0 }
' FS='|' "$DOC")
if [[ "$table_sum" -ne "$n_registered" ]]; then
    echo "FAIL: the per-category table adds up to $table_sum, server registers $n_registered." >&2
    fail=1
fi

# ── the numbers README.md states about the same catalogue ────────────────────
# Optional file, but if it is there it must not contradict the page it links to.
if [[ -f "$README" ]]; then
    badge=$(grep -oE 'badge/MCP-[0-9]+_tools' "$README" | head -1 | grep -oE '[0-9]+' || true)
    if [[ -n "$badge" && "$badge" -ne "$n_registered" ]]; then
        echo "FAIL: the $README badge says $badge tools, server registers $n_registered." >&2
        fail=1
    fi
    # Only sentences that CLAIM the current total. Deliberately narrow: the README also
    # says the removed duplicate server "was 12 tools behind", which is history, not a
    # claim, and a gate that flags it would be switched off rather than obeyed.
    while read -r stale; do
        [[ -z "$stale" ]] && continue
        echo "FAIL: $README claims a current tool count that is not $n_registered:" >&2
        echo "  $stale" >&2
        fail=1
    done < <(grep -nEi '(with|exposes|all|catalogue of) [0-9]+ (MCP )?tools|[0-9]+ of the [0-9]+ tools' "$README" \
             | grep -vEi "(with|exposes|all|catalogue of) ${n_registered} (MCP )?tools" || true)
fi

if [[ "$fail" -ne 0 ]]; then
    echo >&2
    echo "The MCP tools live in evc-mesh-mcp; this page is this repo's copy of their catalogue." >&2
    echo "A tool added or removed there is a change to $DOC as well." >&2
    exit 1
fi

echo "OK: $DOC documents all $n_registered registered tools, no extras, counts agree."
[[ -f "$README" ]] && echo "OK: $README agrees on $n_registered."
