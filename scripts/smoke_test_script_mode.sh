#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKEND_DIR="$ROOT_DIR"
DATA_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/minime-smoke-data.XXXXXX")"
PORT="${MINIME_SMOKE_PORT:-18088}"
BASE_URL="http://127.0.0.1:${PORT}"
ACCESS_TOKEN="${MINIME_SMOKE_ACCESS_TOKEN:-${COGNITO_ACCESS_TOKEN:-}}"
SERVER_LOG="$DATA_ROOT/server.log"
UPLOAD_FILE="$DATA_ROOT/upload-source.png"

cleanup() {
  if [[ -n "${SERVER_PID:-}" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$DATA_ROOT"
}
trap cleanup EXIT

if [[ -z "$ACCESS_TOKEN" ]]; then
  echo "MINIME_SMOKE_ACCESS_TOKEN or COGNITO_ACCESS_TOKEN is required" >&2
  exit 1
fi

python3 - <<'PY' > "$UPLOAD_FILE"
from PIL import Image
import sys

image = Image.new("RGBA", (8, 8), (255, 128, 64, 255))
image.save(sys.stdout.buffer, format="PNG")
PY

(
  cd "$BACKEND_DIR"
  MINIME_PORT="$PORT" \
  MINIME_GENERATOR_MODE=script \
  MINIME_DATA_ROOT="$DATA_ROOT" \
  TONGUE_COGNITO_ISSUER="${MINIME_SMOKE_COGNITO_ISSUER:?MINIME_SMOKE_COGNITO_ISSUER is required}" \
  TONGUE_COGNITO_CLIENT_ID="${MINIME_SMOKE_COGNITO_CLIENT_ID:?MINIME_SMOKE_COGNITO_CLIENT_ID is required}" \
  TONGUE_COGNITO_JWKS_URL="${MINIME_SMOKE_COGNITO_JWKS_URL:-}" \
  MINIME_IMAGE_GENERATOR_SCRIPT="$BACKEND_DIR/testdata/fake_generate_image.py" \
  MINIME_STATE_PIPELINE_SCRIPT="$BACKEND_DIR/testdata/fake_run_specialist_state_pipeline.py" \
  go run ./cmd/minime-server
) >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!

for _ in {1..50}; do
  if curl -fsS "$BASE_URL/v1/minime/sessions" -o /dev/null -H "Authorization: Bearer $ACCESS_TOKEN" -H 'Content-Type: application/json' -d '{}' 2>/dev/null; then
    break
  fi
  sleep 0.2
done

SESSION_JSON="$(curl -fsS -X POST "$BASE_URL/v1/minime/sessions" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{}')"
SESSION_ID="$(printf '%s' "$SESSION_JSON" | jq -r '.session_id')"

wait_for_job() {
  local job_id="$1"
  for _ in {1..100}; do
    local job_json
    job_json="$(curl -fsS "$BASE_URL/v1/minime/jobs/$job_id" \
      -H "Authorization: Bearer $ACCESS_TOKEN")"
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
    sleep 0.1
  done

  printf 'timed out waiting for job %s\n' "$job_id" >&2
  return 1
}

extract_job_id() {
  printf '%s' "$1" | awk 'tolower($0) ~ /^x-minime-job-id:/ {print $2}' | tr -d '\r'
}

curl -fsS -X POST "$BASE_URL/v1/minime/sessions/$SESSION_ID/bootstrap" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{}' >/dev/null

curl -fsS -X POST "$BASE_URL/v1/minime/sessions/$SESSION_ID/photos" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -F "photos=@$UPLOAD_FILE;type=image/png" >/dev/null

CANDIDATES_JSON="$(curl -fsS -X POST "$BASE_URL/v1/minime/sessions/$SESSION_ID/candidates:generate" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  -D - \
  -d '{}')"

CANDIDATE_JOB_ID="$(extract_job_id "$CANDIDATES_JSON")"
wait_for_job "$CANDIDATE_JOB_ID"

CANDIDATES_JSON="$(curl -fsS "$BASE_URL/v1/minime/sessions/$SESSION_ID" \
  -H "Authorization: Bearer $ACCESS_TOKEN")"

printf '%s' "$CANDIDATES_JSON" | jq -e '
  .status == "candidates-generated" and
  (.candidates | length) == 4 and
  (.selected_candidate_id | length) > 0
' >/dev/null

STATES_JSON="$(curl -fsS -X POST "$BASE_URL/v1/minime/sessions/$SESSION_ID/states:generate" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  -D - \
  -d '{"states":["idle-day","working"]}')"

STATE_JOB_ID="$(extract_job_id "$STATES_JSON")"
wait_for_job "$STATE_JOB_ID"

STATES_JSON="$(curl -fsS "$BASE_URL/v1/minime/sessions/$SESSION_ID" \
  -H "Authorization: Bearer $ACCESS_TOKEN")"

printf '%s' "$STATES_JSON" | jq -e '
  .status == "states-generated" and
  (.state_assets | length) == 2 and
  ([.state_assets[].source_image.download_url] | all(. != null)) and
  ([.state_assets[].final_asset.download_url] | all(. != null))
' >/dev/null

SOURCE_URL="$(printf '%s' "$STATES_JSON" | jq -r '.state_assets[0].source_image.download_url')"
FINAL_URL="$(printf '%s' "$STATES_JSON" | jq -r '.state_assets[0].final_asset.download_url')"

curl -fsS "$SOURCE_URL" -H "Authorization: Bearer $ACCESS_TOKEN" >/dev/null
curl -fsS "$FINAL_URL" -H "Authorization: Bearer $ACCESS_TOKEN" >/dev/null

echo "Mini Me script-mode smoke test passed for session $SESSION_ID"
