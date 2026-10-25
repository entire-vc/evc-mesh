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

if grep -n 'raw.githubusercontent' "$gate" > /dev/null; then
    echo "FAIL: the gate still names the GitHub showcase as a source"; exit 1
fi
echo "ok: no showcase source in the gate"
echo "OK: check-mcp-reference.sh self-test passed."
