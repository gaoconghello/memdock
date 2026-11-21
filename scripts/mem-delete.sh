#!/bin/sh
set -eu
if [ "$#" -ne 1 ]; then echo "usage: mem-delete.sh MEMORY_ID" >&2; exit 2; fi
: "${MEMDOCK_URL:?set MEMDOCK_URL}"
: "${MEMDOCK_PROJECT_ID:?set MEMDOCK_PROJECT_ID for the selected memory project}"
[ -t 0 ] || { echo "mem-delete requires an interactive terminal for explicit user confirmation" >&2; exit 1; }
id=$1
printf 'Soft-delete memory %s? Type y to confirm: ' "$id" >&2
IFS= read -r answer
[ "$answer" = y ] || { echo "cancelled" >&2; exit 1; }
payload=$(jq -nc --arg project_id "$MEMDOCK_PROJECT_ID" '{project_id:$project_id}')
path="/api/v1/memories/$id"
if [ "${MEMDOCK_MODE:-}" = secure ] || [ -n "${MEMDOCK_READ_TOKEN:-}" ]; then
  script_dir=$(dirname "$0")
  sh "$script_dir/prepare-approval.sh" DELETE "$path" "$payload"
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
signature=$(printf '%s\n%s\nDELETE\n%s\n%s' "$timestamp" "$nonce" "$path" "$payload" | openssl dgst -sha256 -hmac "$key" | awk '{print $NF}')
printf '%s' "$payload" | curl -fsS -X DELETE -H "Authorization: Bearer ${MEMDOCK_READ_TOKEN:-}" -H 'Content-Type: application/json' -H "X-MemDock-Write-Signature: $timestamp:$nonce:$signature" -d @- "$MEMDOCK_URL$path"
