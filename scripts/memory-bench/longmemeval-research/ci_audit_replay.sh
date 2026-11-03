#!/usr/bin/env bash
# Replays the CURRENT fixture_audit.py on the stored artifacts of one finished full-500 job.
# No stack, no API keys, no paid calls: it only downloads the job artifacts (CI_JOB_TOKEN),
# checks their sha256 against pins, downloads the pinned public dataset and re-runs the audit.
# Output (full500-replay/receipt.json + fixture-audit.json, fixed names) is a separate source job; it never claims the original job ran the corrected audit.
set -euo pipefail
SRC_JOB="${LME_REPLAY_JOB:-533465}"
[[ "$SRC_JOB" =~ ^[1-9][0-9]{0,9}$ ]] || { echo "LME_REPLAY_JOB must be a numeric job id"; exit 1; }
RAW_SHA="7cb24b509e1ac6eaa60c7be2f1d5b483025e4ae80dafcf68cee0f62942166f81"
IDS_SHA="53c8dba6450caa40ca2d223dc6c488345e14d7cd844feba7756579f7849ba33b"
JUDGED_SHA="160c30ea3005edcce5b58d4c13404252bf315463e4254152218a61a0687c1aaf"
DATASET_SHA256="d6f21ea9d60a0d56f34a05b609c79c88a451d2ae03597821ea3d5a9678c3a442"
ROOT="$PWD"; LME="$ROOT/scripts/memory-bench/longmemeval-research"; OUT="$ROOT/full500-replay"; W=/tmp/replay
mkdir -p "$OUT" "$W"

curl -sfL --header "JOB-TOKEN: $CI_JOB_TOKEN" -o "$W/a.zip" "$CI_API_V4_URL/projects/$CI_PROJECT_ID/jobs/$SRC_JOB/artifacts"
python3 -m zipfile -e "$W/a.zip" "$W/src"
S="$W/src/full500-out"
{ echo "$RAW_SHA  $S/full500-raw.jsonl"; echo "$IDS_SHA  $S/store_ids.jsonl"; echo "$JUDGED_SHA  $S/full500-judged.jsonl"; } | sha256sum -c -

# Same dataset and audit pins as the full benchmark minus torch/sentence-transformers: only the dataset download and audit run here.
pip install -q "mcp>=1.0,<2" "openai>=1.0,<2" "httpx>=0.28,<1" "tqdm>=4.66,<5" "backoff>=2.2,<3" "numpy>=1.24,<3"
cd "$LME"
python3 scripts/fixture_audit.py --selftest
python3 scripts/run_benchmark.py download --variant s
echo "$DATASET_SHA256  data/longmemeval_s_cleaned.json" | sha256sum -c -
# The replay's product is the audit RESULT. A failing audit (invalid questions) is a finding recorded in
# fixture-audit.json and audit-exit.txt, not a replay failure; integrity failures above still abort.
set +e
python3 scripts/fixture_audit.py --dataset data/longmemeval_s_cleaned.json --ids-log "$S/store_ids.jsonl" \
  --run-log "$S/run.log" --results "$S/full500-raw.jsonl" --out "$OUT/fixture-audit.json" | tee "$OUT/fixture-audit-totals.txt"
echo "${PIPESTATUS[0]}" > "$OUT/audit-exit.txt"
set -e
test -s "$OUT/fixture-audit.json"
python3 scripts/score_by_category.py "$S/full500-judged.jsonl" | tee "$OUT/score.txt"
export S OUT SRC_JOB DATASET_SHA256
python3 - <<'PY'
import hashlib, json, os, pathlib
S = pathlib.Path(os.environ["S"])
sha = lambda n: hashlib.sha256((S / n).read_bytes()).hexdigest()
# Fixed receipt schema read by the historical-CI replay extractor; every hash is recomputed here
# and the extractor re-checks it against the authenticated original archive.
json.dump({"version": 1, "audit_source_sha": os.environ["CI_COMMIT_SHA"],
           "original_source_sha": "1b98ffcbe3f1f90eff86ed0f233452fdaa804c3e", "original_pipeline_id": 25419, "original_job_id": int(os.environ["SRC_JOB"]),
           "dataset_sha256": os.environ["DATASET_SHA256"],
           "input_sha256": {n: sha(n) for n in ("full500-raw.jsonl", "full500-judged.jsonl", "store_ids.jsonl", "run.log")}},
          open(os.path.join(os.environ["OUT"], "receipt.json"), "w"), indent=1)
PY
