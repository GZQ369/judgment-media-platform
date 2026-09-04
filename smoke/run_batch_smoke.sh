#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:8088}"
REQ_FILE="${1:-smoke/batch_products_request.json}"
RESP_FILE="${RESP_FILE:-tmp/batch_smoke_response.json}"

mkdir -p "$(dirname "$RESP_FILE")"

echo "Checking health: $BASE_URL/health"
curl -fsS "$BASE_URL/health" >/dev/null

echo "Running batch request: $REQ_FILE"
curl -fsS \
  -X POST "$BASE_URL/api/v1/batches/run" \
  -H "Content-Type: application/json" \
  --data-binary "@$REQ_FILE" \
  > "$RESP_FILE"

if command -v python >/dev/null 2>&1; then
  JSON_PARSER=python
elif command -v python3 >/dev/null 2>&1; then
  JSON_PARSER=python3
else
  JSON_PARSER=""
fi

if [ -n "$JSON_PARSER" ]; then
  "$JSON_PARSER" - "$RESP_FILE" <<'PY'
import json
import sys

path = sys.argv[1]
with open(path, "r", encoding="utf-8") as f:
    data = json.load(f)

print("batch_id:", data.get("id"))
print("status:", data.get("status"))
print("report_path:", data.get("report_path"))
print("report_json_path:", data.get("report_json_path"))
for item in data.get("items", []):
    print(
        "item:",
        item.get("index"),
        item.get("product_title"),
        "project_id=" + str(item.get("project_id")),
        "status=" + str(item.get("status")),
        "publish_package=" + str(item.get("publish_package_path")),
    )
PY
elif command -v powershell.exe >/dev/null 2>&1 || command -v powershell >/dev/null 2>&1; then
  PS_BIN="powershell.exe"
  if ! command -v powershell.exe >/dev/null 2>&1; then
    PS_BIN="powershell"
  fi
  PS_RESP_FILE="$RESP_FILE"
  if command -v cygpath >/dev/null 2>&1; then
    PS_RESP_FILE="$(cygpath -w "$RESP_FILE")"
  fi
  PS_HELPER="$(dirname "$RESP_FILE")/parse_batch_smoke_response.ps1"
  cat > "$PS_HELPER" <<'PS1'
param([string]$Path)

$data = Get-Content -Raw -Encoding UTF8 $Path | ConvertFrom-Json
Write-Host "batch_id:" $data.id
Write-Host "status:" $data.status
Write-Host "report_path:" $data.report_path
Write-Host "report_json_path:" $data.report_json_path
foreach ($item in $data.items) {
  Write-Host "item:" $item.index $item.product_title "project_id=$($item.project_id)" "status=$($item.status)" "publish_package=$($item.publish_package_path)"
}
PS1
  PS_HELPER_RUN="$PS_HELPER"
  if command -v cygpath >/dev/null 2>&1; then
    PS_HELPER_RUN="$(cygpath -w "$PS_HELPER")"
  fi
  "$PS_BIN" -NoProfile -ExecutionPolicy Bypass -File "$PS_HELPER_RUN" "$PS_RESP_FILE"
  rm -f "$PS_HELPER"
else
  echo "No python/python3/powershell JSON parser found; raw response saved to $RESP_FILE" >&2
fi

echo "Saved response to $RESP_FILE"
