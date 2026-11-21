#!/bin/sh
set -eu
if [ "$#" -ne 2 ]; then echo "usage: mem-project-rename.sh PROJECT_ID NAME" >&2; exit 2; fi
: "${MEMDOCK_URL:?set MEMDOCK_URL}"
project_id=$1
name=$2
[ -t 0 ] || { echo "project rename requires an interactive terminal" >&2; exit 1; }
printf 'Rename project %s to %s? Type y to confirm: ' "$project_id" "$name" >&2
IFS= read -r answer
[ "$answer" = y ] || { echo "cancelled" >&2; exit 1; }
payload=$(jq -nc --arg name "$name" '{name:$name}')
path="/api/v1/projects/$project_id"
if [ "${MEMDOCK_MODE:-}" = secure ] || [ -n "${MEMDOCK_READ_TOKEN:-}" ]; then
  script_dir=$(dirname "$0")
  sh "$script_dir/prepare-approval.sh" PATCH "$path" "$payload"
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
signature=$(printf '%s\n%s\nPATCH\n%s\n%s' "$timestamp" "$nonce" "$path" "$payload" | openssl dgst -sha256 -hmac "$key" | awk '{print $NF}')
printf '%s' "$payload" | curl -fsS -X PATCH -H "Authorization: Bearer ${MEMDOCK_READ_TOKEN:-}" -H 'Content-Type: application/json' -H "X-MemDock-Write-Signature: $timestamp:$nonce:$signature" -d @- "$MEMDOCK_URL$path"
