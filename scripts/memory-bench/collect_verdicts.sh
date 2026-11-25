# Source from the branch gate after the mandatory preflight/contract proof.
# Finish every measurement before reporting a blocking quality verdict.
BENCH_JOB_RC=0
BENCH_VERDICT_RC=0

record_bench_verdict() {
  local label=$1 rc=$2
  case "$rc" in
    0) echo "BENCH CHECK $label: PASS rc=0" ;;
    1)
      echo "BENCH CHECK $label: REGRESSION rc=1"
      BENCH_VERDICT_RC=1
      if [ "$BENCH_JOB_RC" = 0 ] || [ "$BENCH_JOB_RC" = 2 ]; then BENCH_JOB_RC=1; fi
      ;;
    2)
      echo "BENCH CHECK $label: INCONCLUSIVE rc=2 (blocking: no valid measurement)"
      [ "$BENCH_VERDICT_RC" = 1 ] || BENCH_VERDICT_RC=2
      [ "$BENCH_JOB_RC" != 0 ] || BENCH_JOB_RC=2
      ;;
    *)
      echo "BENCH CHECK $label: EXECUTION_ERROR rc=$rc (blocking)"
      [ "$BENCH_VERDICT_RC" = 1 ] || BENCH_VERDICT_RC=2
      if [ "$BENCH_JOB_RC" = 0 ] || [ "$BENCH_JOB_RC" = 1 ] || [ "$BENCH_JOB_RC" = 2 ]; then BENCH_JOB_RC=$rc; fi
      ;;
  esac
  return 0
}

run_bench_check() {
  local label=$1 log=$2
  shift 2
  local -a statuses
  set +e
  "$@" 2>&1 | tee "$log"
  statuses=("${PIPESTATUS[@]}")
  set -e
  BENCH_CHECK_RC=${statuses[0]}
  record_bench_verdict "$label" "$BENCH_CHECK_RC"
  if [ "${statuses[1]}" != 0 ]; then
    record_bench_verdict "$label-log" 74
  fi
}

finish_bench_checks() {
  case "$BENCH_VERDICT_RC" in
    0) echo "BENCH SUMMARY: PASS job_rc=$BENCH_JOB_RC" ;;
    1) echo "BENCH SUMMARY: REGRESSION job_rc=$BENCH_JOB_RC" ;;
    2) echo "BENCH SUMMARY: INCONCLUSIVE job_rc=$BENCH_JOB_RC" ;;
  esac
  return "$BENCH_JOB_RC"
}
