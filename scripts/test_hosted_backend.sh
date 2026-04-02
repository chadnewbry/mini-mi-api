#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${MINIME_BASE_URL:-${1:-}}"
DEVICE_TOKEN="${MINIME_DEVICE_TOKEN:-${2:-}}"
UPLOAD_FILE="${MINIME_TEST_UPLOAD_FILE:-}"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/mini-mi-hosted-test.XXXXXX")"

cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

if [[ -z "$BASE_URL" ]]; then
  echo "usage: MINIME_BASE_URL=https://... MINIME_DEVICE_TOKEN=... $0" >&2
  exit 1
fi

if [[ -z "$DEVICE_TOKEN" ]]; then
  echo "MINIME_DEVICE_TOKEN is required" >&2
  exit 1
fi

if [[ -z "$UPLOAD_FILE" ]]; then
  UPLOAD_FILE="$TMP_DIR/upload-source.png"
  python3 - <<'PY' > "$UPLOAD_FILE"
from PIL import Image
import sys

image = Image.new("RGBA", (8, 8), (255, 128, 64, 255))
image.save(sys.stdout.buffer, format="PNG")
PY
fi

wait_for_job() {
  local job_id="$1"
  for _ in {1..300}; do
    local job_json
    job_json="$(curl -fsS "$BASE_URL/v1/minime/jobs/$job_id" \
      -H "Authorization: Bearer $DEVICE_TOKEN")"
    local status
    status="$(printf '%s' "$job_json" | jq -r '.status')"
    case "$status" in
      completed)
        return 0
        ;;
      failed)
        printf 'job %s failed: %s\n' "$job_id" "$(printf '%s' "$job_json" | jq -r '.error // .summary // "unknown error"')" >&2
        return 1
        ;;
    esac
    sleep 1
  done

  printf 'timed out waiting for job %s\n' "$job_id" >&2
  return 1
}

extract_job_id() {
  printf '%s' "$1" | awk 'tolower($0) ~ /^x-minime-job-id:/ {print $2}' | tr -d '\r'
}

curl -fsS "$BASE_URL/healthz" >/dev/null

SESSION_JSON="$(curl -fsS -X POST "$BASE_URL/v1/minime/sessions" \
  -H "Authorization: Bearer $DEVICE_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{}')"
SESSION_ID="$(printf '%s' "$SESSION_JSON" | jq -r '.session_id')"

curl -fsS -X POST "$BASE_URL/v1/minime/sessions/$SESSION_ID/bootstrap" \
  -H "Authorization: Bearer $DEVICE_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{}' >/dev/null

curl -fsS -X POST "$BASE_URL/v1/minime/sessions/$SESSION_ID/photos" \
  -H "Authorization: Bearer $DEVICE_TOKEN" \
  -F "photos=@$UPLOAD_FILE;type=image/png" >/dev/null

CANDIDATES_RESPONSE="$(curl -fsS -X POST "$BASE_URL/v1/minime/sessions/$SESSION_ID/candidates:generate" \
  -H "Authorization: Bearer $DEVICE_TOKEN" \
  -H 'Content-Type: application/json' \
  -D - \
  -d '{}')"
CANDIDATE_JOB_ID="$(extract_job_id "$CANDIDATES_RESPONSE")"
wait_for_job "$CANDIDATE_JOB_ID"

SESSION_JSON="$(curl -fsS "$BASE_URL/v1/minime/sessions/$SESSION_ID" \
  -H "Authorization: Bearer $DEVICE_TOKEN")"

printf '%s' "$SESSION_JSON" | jq -e '
  .status == "candidates-generated" and
  (.candidates | length) >= 1 and
  (.selected_candidate_id | length) > 0
' >/dev/null

STATES_RESPONSE="$(curl -fsS -X POST "$BASE_URL/v1/minime/sessions/$SESSION_ID/states:generate" \
  -H "Authorization: Bearer $DEVICE_TOKEN" \
  -H 'Content-Type: application/json' \
  -D - \
  -d '{"states":["idle-day","working"]}')"
STATE_JOB_ID="$(extract_job_id "$STATES_RESPONSE")"
wait_for_job "$STATE_JOB_ID"

SESSION_JSON="$(curl -fsS "$BASE_URL/v1/minime/sessions/$SESSION_ID" \
  -H "Authorization: Bearer $DEVICE_TOKEN")"

printf '%s' "$SESSION_JSON" | jq -e '
  .status == "states-generated" and
  (.state_assets | length) == 2 and
  ([.state_assets[].source_image.download_url] | all(. != null)) and
  ([.state_assets[].final_asset.download_url] | all(. != null))
' >/dev/null

echo "Hosted Mini Me backend test passed for session $SESSION_ID"
