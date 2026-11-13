#!/usr/bin/env bash
# Offline self-test for check-mcp-reference.sh: builds a 12-tool server source and a matching
# catalogue in a temp dir and proves the gate goes red for each failure it exists to catch.
# Nothing here touches the network; the canon fetch path is exercised by the real job.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
gate="$here/check-mcp-reference.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

names=(tool_a tool_b tool_c tool_d tool_e tool_f tool_g tool_h tool_i tool_j tool_k tool_l)

write_server() { # <dir> <tool names...>
    local dir="$1"; shift
    mkdir -p "$dir"
    : > "$dir/server.go"
    for n in "$@"; do echo "    s.AddTool(mcp.NewTool(\"$n\", x), h)" >> "$dir/server.go"; done
    : > "$dir/decision_tool_names.go"
}

write_doc() { # <file> <count> <tool names...>
    local file="$1" count="$2"; shift 2
    {
        echo "The server exposes **${count} MCP tools**."
        echo
        echo "| Category | Tools | Description |"
        echo "|----------|-------|-------------|"
        echo "| All | ${count} | everything |"
        echo
        local i=1
        for n in "$@"; do echo "#### ${i}. \`$n\`"; i=$((i+1)); done
    } > "$file"
}

run() { # <server dir> <doc> -> exit code, output in $tmp/out
    set +e
    MCP_SERVER_GO="$1/server.go" bash "$gate" "$2" "$tmp/no-readme.md" > "$tmp/out" 2>&1
    rc=$?
    set -e
    return 0
}

expect() { # <label> <want rc 0|1> [grep pattern in output]
    local label="$1" want="$2" pat="${3:-}"
    if [[ "$want" == 0 && $rc -ne 0 ]] || [[ "$want" == 1 && $rc -eq 0 ]]; then
        echo "FAIL: $label (exit $rc)"; cat "$tmp/out"; exit 1
    fi
    if [[ -n "$pat" ]] && ! grep -q -- "$pat" "$tmp/out"; then
        echo "FAIL: $label (output lacks '$pat')"; cat "$tmp/out"; exit 1
    fi
    echo "ok: $label"
}

write_server "$tmp/good" "${names[@]}"
write_doc "$tmp/good.md" 12 "${names[@]}"
run "$tmp/good" "$tmp/good.md"; expect "matching catalogue passes" 0

# A tool the server registers but the page does not list (the case that went red in a review).
write_server "$tmp/extra" "${names[@]}" tool_m
run "$tmp/extra" "$tmp/good.md"; expect "undocumented tool fails" 1 "tool_m"

# A documented tool the server does not register.
write_server "$tmp/gone" "${names[@]:0:11}"
run "$tmp/gone" "$tmp/good.md"; expect "phantom tool fails" 1 "does not register"

# The stated total disagrees with the server.
write_doc "$tmp/count.md" 11 "${names[@]}"
run "$tmp/good" "$tmp/count.md"; expect "wrong stated count fails" 1 "exposes"

# No credentials for the canon fetch: refuse, never fall back to another source.
set +e
(unset CI_JOB_TOKEN MCP_CANON_TOKEN MCP_SERVER_GO; bash "$gate" "$tmp/good.md" "$tmp/no-readme.md" > "$tmp/out" 2>&1)
rc=$?
set -e
expect "no credentials refuses" 1 "no credentials"
[[ $rc -eq 2 ]] || { echo "FAIL: a refusal must exit 2, not $rc (the sync job opens an MR on 1 only)"; exit 1; }
echo "ok: a refusal exits 2, a mismatch 1"
run "$tmp/extra" "$tmp/good.md"
[[ $rc -eq 1 ]] || { echo "FAIL: a mismatch must exit 1, not $rc"; exit 1; }

# ── the writer (scripts/sync-mcp-reference.py) turns each mismatch into a page the gate passes.
writer="$here/sync-mcp-reference.py"
sync_case() { # <label> <server dir> <doc> : write, then the gate must pass on the result
    local label="$1" srv="$2" doc="$3"
    rm -rf "$tmp/src"
    set +e; MCP_SERVER_GO="$srv/server.go" MCP_REF_SOURCE_OUT="$tmp/src" bash "$gate" "$doc" "$tmp/no-readme.md" > /dev/null 2>&1; set -e
    python3 -I "$writer" "$tmp/src" "$doc" "$tmp/no-readme.md" > /dev/null
    run "$srv" "$doc"; expect "writer: $label" 0
}
printf '\ts.addTool(mcpsdk.NewTool("tool_z",\n\t\tmcpsdk.WithDescription("Does "+\n\t\t\t"m | n."),\n\t\tmcpsdk.WithBoolean("flag", mcpsdk.Description("A flag")),\n\t\tmcpsdk.WithString("id", mcpsdk.Required(), mcpsdk.Description("Target id")),\n\t), h)\n' >> "$tmp/extra/server.go"
for d in add remove; do
    write_doc "$tmp/$d.md" 12 "${names[@]}"
    # the real page's shape: "## Tools", one "### <category> (N tools)", the entries, "## Error Handling"
    awk '/^#### / && !h { print "## Tools"; print ""; print "### All (12 tools)"; print ""; h=1 } { print }' "$tmp/$d.md" > "$tmp/$d.tmp"
    { cat "$tmp/$d.tmp"; echo; echo "## Error Handling"; } > "$tmp/$d.md"
done
sync_case "a new tool gets a generated entry" "$tmp/extra" "$tmp/add.md"
grep -q '^#### 14\. `tool_z`$' "$tmp/add.md" && grep -q '^Does m | n\.$' "$tmp/add.md" &&
    grep -q '^| `id` | string | \*\*Yes\*\* | -- | Target id |$' "$tmp/add.md" &&
    grep -q '^| `flag` | boolean | No | -- | A flag |$' "$tmp/add.md" ||
    { echo "FAIL: the generated entry lacks the description or the parameters"; cat "$tmp/add.md"; exit 1; }
cp "$tmp/add.md" "$tmp/add2.md"; sync_case "a second run changes nothing" "$tmp/extra" "$tmp/add2.md"
cmp -s "$tmp/add.md" "$tmp/add2.md" || { echo "FAIL: the writer is not idempotent"; diff "$tmp/add.md" "$tmp/add2.md"; exit 1; }
sync_case "a removed tool loses its entry" "$tmp/gone" "$tmp/remove.md"
grep -q '`tool_l`' "$tmp/remove.md" && { echo "FAIL: tool_l entry kept"; exit 1; }
# The removed tool was the only one in its category: heading and table row go, the rest stays.
{
    echo "The server exposes **12 MCP tools**."; echo
    echo "| Category | Tools | Description |"; echo "|----------|-------|-------------|"
    echo "| Main | 11 | most |"; echo "| Solo | 1 | one |"; echo "| Tail | 0 | none |"; echo
    echo "## Tools"; echo; echo "### Main (11 tools)"; echo
    i=1; for n in "${names[@]:0:11}"; do printf '#### %d. `%s`\n\nText %s.\n\n' "$i" "$n" "$n"; i=$((i+1)); done
    echo "### Solo (1 tools)"; echo; echo '#### 12. `tool_l`'; echo; echo "Text tool_l."; echo
    echo "### Tail (0 tools)"; echo; echo "Kept prose."; echo
    echo "## Error Handling"; echo; echo "Errors."
} > "$tmp/solo.md"
sync_case "removing a category's last tool drops its heading and row" "$tmp/gone" "$tmp/solo.md"
grep -q 'Solo' "$tmp/solo.md" && { echo "FAIL: the emptied category's heading or row was kept"; cat "$tmp/solo.md"; exit 1; }
for want in '### Main (11 tools)' '| Main | 11 | most |' '### Tail (0 tools)' 'Kept prose.' '## Error Handling' 'Text tool_k.'; do
    grep -qF -- "$want" "$tmp/solo.md" || { echo "FAIL: '$want' lost"; cat "$tmp/solo.md"; exit 1; }
done
echo "ok: an emptied category goes, everything after it stays"

if grep -n 'raw.githubusercontent' "$gate" > /dev/null; then
    echo "FAIL: the gate still names the GitHub showcase as a source"; exit 1
fi
echo "ok: no showcase source in the gate"
echo "OK: check-mcp-reference.sh self-test passed."
