#!/bin/sh
set -eu
if [ "$#" -lt 2 ]; then echo "usage: mem-kb-create.sh project|shared NAME [PROJECT_ID]" >&2; exit 2; fi
: "${MEMDOCK_URL:?set MEMDOCK_URL}"
kind=$1 name=$2 project_id=${3:-}
[ "$kind" = project ] || [ "$kind" = shared ] || { echo "kind must be project or shared" >&2; exit 2; }
[ "$kind" != project ] || [ -n "$project_id" ] || { echo "project_id is required for a project library" >&2; exit 2; }
[ "$kind" != shared ] || [ -z "$project_id" ] || { echo "shared libraries do not take project_id" >&2; exit 2; }
[ -t 0 ] || { echo "library creation requires an interactive terminal" >&2; exit 1; }
printf 'Create %s knowledge base %s? Type y to confirm: ' "$kind" "$name" >&2
IFS= read -r answer
[ "$answer" = y ] || { echo "cancelled" >&2; exit 1; }
payload=$(jq -nc --arg kind "$kind" --arg name "$name" --arg project_id "$project_id" '{kind:$kind,name:$name} + (if $project_id=="" then {} else {project_id:$project_id} end)')
if [ "${MEMDOCK_MODE:-}" = secure ] || [ -n "${MEMDOCK_READ_TOKEN:-}" ]; then
  script_dir=$(dirname "$0")
  sh "$script_dir/prepare-approval.sh" POST /api/v1/knowledge-bases "$payload"
  exit 0
fi
key=${MEMDOCK_APPROVAL_KEY:-}
if [ -z "$key" ]; then
  key_file=${MEMDOCK_APPROVAL_KEY_FILE:-${XDG_CONFIG_HOME:-$HOME/.config}/memdock/approval.key}
  [ -r "$key_file" ] || { echo "set MEMDOCK_APPROVAL_KEY or provide per-write approval key file: $key_file" >&2; exit 1; }
  key=$(tr -d '\r\n' < "$key_file")
fi
[ "${#key}" -ge 32 ] || { echo "approval key must contain at least 32 bytes" >&2; exit 1; }
timestamp=$(date +%s)
nonce=$(openssl rand -hex 16)
signature=$(printf '%s\n%s\nPOST\n/api/v1/knowledge-bases\n%s' "$timestamp" "$nonce" "$payload" | openssl dgst -sha256 -hmac "$key" | awk '{print $NF}')
printf '%s' "$payload" | curl -fsS -H "Authorization: Bearer ${MEMDOCK_READ_TOKEN:-}" -H 'Content-Type: application/json' -H "X-MemDock-Write-Signature: $timestamp:$nonce:$signature" -d @- "$MEMDOCK_URL/api/v1/knowledge-bases"
