#!/usr/bin/env bash
# Monitor Ascend/pytorch "PyTorch CI Trigger PR" (pytorch_ci_trigger_pr.yml)
# for storms of failed "valid CI" runs, and disable it when a threshold is hit.
#
# Definitions
#   valid CI run: a run of the target workflow that actually executed its
#   build or test jobs (job names matching $VALID_JOB_REGEX with a non-skipped
#   conclusion). Runs that only pass through "extract" (event filtered out)
#   leave the build/test jobs as "skipped" and do not count.
#
# Behavior
#   1. Gate: if the target workflow is not active, exit 0 (nothing to monitor).
#   2. Count valid CI runs whose *completion* time falls inside the lookback
#      window. The window is anchored at this monitoring run's start time
#      (run_started_at) minus WINDOW_HOURS.
#   3. If the count >= threshold: disable the target workflow, then exit 1
#      ON PURPOSE. A failed monitoring run makes GitHub send its built-in
#      scheduled-workflow failure email to the repo owner. While the target
#      workflow remains enabled, every hourly run retries the disable and
#      keeps notifying; once disabled, the gate silences further alerts.
#
# Usage
#   monitor_trigger_pr_ci.sh [--dry-run] [--since ISO8601-UTC]
#                           [--window-hours N] [--threshold N]
#                           [--backscan-hours N]
#
#   --dry-run         skip gate and disable/trip; report counts only (testing)
#   --since           override the window start, e.g. 2026-09-08T20:30:00Z
#
# Required environment
#   GH_READ_TOKEN    token used for all read-only API calls
#   GH_WRITE_TOKEN   token allowed to disable the upstream workflow
#                    (required unless --dry-run)
#
# Optional environment (defaults shown)
#   UPSTREAM_REPO=Ascend/pytorch
#   TARGET_WORKFLOW=pytorch_ci_trigger_pr.yml
#   VALID_JOB_REGEX='^forward / (build|test)'
#
# Exit codes
#   0  idle / below threshold / dry-run / already disabled by another actor
#   1  breaker tripped (deliberate, triggers the failure email) or a real error
#   2  usage error

set -euo pipefail

UPSTREAM_REPO="${UPSTREAM_REPO:-Ascend/pytorch}"
TARGET_WORKFLOW="${TARGET_WORKFLOW:-pytorch_ci_trigger_pr.yml}"
VALID_JOB_REGEX="${VALID_JOB_REGEX:-^forward / (build|test)}"

WINDOW_HOURS="2"
THRESHOLD="3"
BACKSCAN_HOURS="6"
SINCE_OVERRIDE=""
DRY_RUN="false"

usage_error() {
  echo "usage: $0 [--dry-run] [--since ISO] [--window-hours N] [--threshold N] [--backscan-hours N]" >&2
  exit 2
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run)        DRY_RUN="true"; shift ;;
    --since)          [ "$#" -ge 2 ] || usage_error; SINCE_OVERRIDE="$2"; shift 2 ;;
    --window-hours)   [ "$#" -ge 2 ] || usage_error; WINDOW_HOURS="$2"; shift 2 ;;
    --threshold)      [ "$#" -ge 2 ] || usage_error; THRESHOLD="$2"; shift 2 ;;
    --backscan-hours) [ "$#" -ge 2 ] || usage_error; BACKSCAN_HOURS="$2"; shift 2 ;;
    *)                usage_error ;;
  esac
done

for v in "$WINDOW_HOURS" "$THRESHOLD" "$BACKSCAN_HOURS"; do
  case "$v" in
    ''|*[!0-9]*) echo "error: numeric argument expected, got '$v'" >&2; exit 2 ;;
  esac
done

: "${GH_READ_TOKEN:?GH_READ_TOKEN is required}"
if [ "$DRY_RUN" != "true" ]; then
  : "${GH_WRITE_TOKEN:?GH_WRITE_TOKEN is required unless --dry-run}"
fi

gh_read()  { GH_TOKEN="$GH_READ_TOKEN" gh api "$@"; }
gh_write() { GH_TOKEN="$GH_WRITE_TOKEN" gh api "$@"; }

summary() {
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    printf '%s\n' "$1" >> "$GITHUB_STEP_SUMMARY"
  fi
}

# ---- 1. Gate: only monitor while the upstream workflow is active ----------
if [ "$DRY_RUN" != "true" ]; then
  STATE=$(gh_read "repos/${UPSTREAM_REPO}/actions/workflows/${TARGET_WORKFLOW}" --jq .state)
  summary "## Monitor Trigger PR CI"
  summary "- target: [${UPSTREAM_REPO}/${TARGET_WORKFLOW}](https://github.com/${UPSTREAM_REPO}/actions/workflows/${TARGET_WORKFLOW})"
  summary "- upstream workflow state: \`${STATE}\`"
  if [ "$STATE" != "active" ]; then
    echo "SKIP: ${UPSTREAM_REPO}/${TARGET_WORKFLOW} is '${STATE}' — monitoring idle."
    summary "- SKIP: workflow is \`${STATE}\`, monitoring idle, nothing counted."
    exit 0
  fi
else
  summary "## Monitor Trigger PR CI (dry-run)"
fi

# ---- 2. Window anchored at this monitoring run's start --------------------
if [ -n "$SINCE_OVERRIDE" ]; then
  SINCE="$SINCE_OVERRIDE"
else
  : "${GITHUB_RUN_ID:?need --since or GITHUB_RUN_ID to anchor the window}"
  : "${GITHUB_REPOSITORY:?need --since or GITHUB_REPOSITORY to anchor the window}"
  ANCHOR=$(gh_read "repos/${GITHUB_REPOSITORY}/actions/runs/${GITHUB_RUN_ID}" --jq .run_started_at \
            | sed -E 's/\.[0-9]+Z$/Z/')
  SINCE=$(date -u -d "${ANCHOR} -${WINDOW_HOURS} hours" '+%Y-%m-%dT%H:%M:%SZ')
fi
# A run created before this horizon cannot have completed inside the window
# (margin must exceed the longest possible build+test run); stops pagination.
BACKSCAN_UNTIL=$(date -u -d "${SINCE} -${BACKSCAN_HOURS} hours" '+%Y-%m-%dT%H:%M:%SZ')

summary "- window: failures **completed** since \`${SINCE}\` (${WINDOW_HOURS}h lookback, completion-time semantics)"

# ---- 3. Page through target workflow runs, keep failures in window -------
export SINCE
FAILURES='[]'
PAGE=1
while :; do
  RESP=$(gh_read "repos/${UPSTREAM_REPO}/actions/workflows/${TARGET_WORKFLOW}/runs?per_page=100&page=${PAGE}")
  RUNS_ON_PAGE=$(jq '.workflow_runs | length' <<<"$RESP")
  if [ "$RUNS_ON_PAGE" -eq 0 ]; then
    break
  fi
  BATCH=$(jq '[.workflow_runs[]
    | select(.status == "completed" and .conclusion == "failure" and .updated_at >= env.SINCE)]' <<<"$RESP")
  FAILURES=$(jq -s '.[0] + .[1]' <(printf '%s' "$FAILURES") <(printf '%s' "$BATCH"))
  OLDEST=$(jq -r '.workflow_runs | last | .created_at' <<<"$RESP")
  if [[ "$OLDEST" < "$BACKSCAN_UNTIL" ]]; then
    break
  fi
  PAGE=$((PAGE + 1))
done

TOTAL_FAILURES=$(jq 'length' <<<"$FAILURES")
echo "failure runs completed since ${SINCE}: ${TOTAL_FAILURES}"

# ---- 4. Classify failures: valid CI = actually executed build/test --------
VALID_COUNT=0
VALID_ROWS=""
while IFS=$'\t' read -r RUN_ID RUN_NAME RUN_URL RUN_UPDATED; do
  [ -n "${RUN_ID:-}" ] || continue
  RAN_BUILD_OR_TEST=$(gh_read "repos/${UPSTREAM_REPO}/actions/runs/${RUN_ID}/jobs?per_page=100" \
    | jq -r --arg re "$VALID_JOB_REGEX" \
        '[.jobs[]
          | select((.name | test($re)) and .conclusion != "skipped" and .conclusion != null)]
         | length > 0')
  if [ "$RAN_BUILD_OR_TEST" = "true" ]; then
    VALID_COUNT=$((VALID_COUNT + 1))
    VALID_ROWS="${VALID_ROWS}| ${RUN_NAME} | ${RUN_UPDATED} | [run ${RUN_ID}](${RUN_URL}) |
"
    echo "valid CI failure: ${RUN_NAME} (completed ${RUN_UPDATED}) ${RUN_URL}"
  else
    echo "extract-only failure (ignored): ${RUN_NAME} (completed ${RUN_UPDATED}) ${RUN_URL}"
  fi
done < <(jq -r '.[] | [.id, .name, .html_url, .updated_at] | @tsv' <<<"$FAILURES")

summary "### Valid CI failures in window"
summary "- failure runs scanned: ${TOTAL_FAILURES}"
summary "- valid CI failures (executed build/test): **${VALID_COUNT}** / threshold ${THRESHOLD}"
if [ -n "$VALID_ROWS" ]; then
  summary "| run | completed at | link |
|---|---|---|
${VALID_ROWS}"
fi

# ---- 5. Decide -------------------------------------------------------------
if [ "$VALID_COUNT" -lt "$THRESHOLD" ]; then
  echo "OK: ${VALID_COUNT} valid CI failure(s) < threshold ${THRESHOLD} — no action."
  exit 0
fi

if [ "$DRY_RUN" = "true" ]; then
  echo "DRY-RUN: threshold met (${VALID_COUNT} >= ${THRESHOLD}) — would disable ${UPSTREAM_REPO}/${TARGET_WORKFLOW} and exit 1 (email alert)."
  exit 0
fi

# ---- 6. Trip: disable upstream, then fail on purpose for the email --------
if ! gh_write -X PUT "repos/${UPSTREAM_REPO}/actions/workflows/${TARGET_WORKFLOW}/disable" --silent; then
  # PUT disable returns 403 when the workflow is already disabled (not
  # idempotent); if another actor just disabled it, treat as done, no alert.
  RACE_STATE=$(gh_read "repos/${UPSTREAM_REPO}/actions/workflows/${TARGET_WORKFLOW}" --jq .state)
  if [ "$RACE_STATE" = "disabled_manually" ]; then
    echo "workflow already disabled_manually (raced with another actor) — treating as done, no alert"
    summary "- breaker condition met but workflow was already disabled by another actor; no alert sent"
    exit 0
  fi
  echo "::error::failed to disable ${UPSTREAM_REPO}/${TARGET_WORKFLOW} (state=${RACE_STATE}); will retry next hour"
  exit 1
fi

NEW_STATE=$(gh_read "repos/${UPSTREAM_REPO}/actions/workflows/${TARGET_WORKFLOW}" --jq .state)
if [ "$NEW_STATE" != "disabled_manually" ]; then
  echo "::error::disable verification failed: state=${NEW_STATE}, expected disabled_manually; will retry next hour"
  exit 1
fi

summary "### Breaker tripped"
summary "- disabled \`${UPSTREAM_REPO}/${TARGET_WORKFLOW}\` after **${VALID_COUNT}** valid CI failures since \`${SINCE}\` (threshold ${THRESHOLD})"
summary "- re-enable with: \`gh api -X PUT repos/${UPSTREAM_REPO}/actions/workflows/${TARGET_WORKFLOW}/enable\` (or the \"Enable workflow\" button on the workflow page)"

echo "::error::Breaker tripped: ${VALID_COUNT} valid CI failures since ${SINCE} (threshold ${THRESHOLD}). Disabled ${UPSTREAM_REPO}/${TARGET_WORKFLOW}. This monitoring run fails on purpose so GitHub sends the failure email. Re-enable: gh api -X PUT repos/${UPSTREAM_REPO}/actions/workflows/${TARGET_WORKFLOW}/enable"
exit 1
