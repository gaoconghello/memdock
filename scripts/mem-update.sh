#!/bin/sh
set -eu
if [ "$#" -lt 3 ] || [ "$#" -gt 4 ]; then echo "usage: mem-update.sh MEMORY_ID TITLE CONTENT [TAGS_CSV]" >&2; exit 2; fi
: "${MEMDOCK_URL:?set MEMDOCK_URL}"
: "${MEMDOCK_PROJECT_ID:?set MEMDOCK_PROJECT_ID for the selected memory project}"
[ -t 0 ] || { echo "mem-update requires an interactive terminal for explicit user confirmation" >&2; exit 1; }
id=$1
title=$2
content=$3
tags_csv=${4:-}
if [ "$#" -eq 4 ]; then
  tags_json=$(jq -cn --arg tags "$tags_csv" 'if $tags=="" then [] else $tags|split(",") end')
else
  tags_json=null
fi
printf 'Update memory %s?\nTitle: %s\nContent:\n%s\nTags: %s\nType y to confirm: ' "$id" "$title" "$content" "$tags_json" >&2
IFS= read -r answer
[ "$answer" = y ] || { echo "cancelled" >&2; exit 1; }
payload=$(jq -nc --arg project_id "$MEMDOCK_PROJECT_ID" --arg title "$title" --arg content "$content" --argjson tags "$tags_json" '{project_id:$project_id,title:$title,content:$content} + (if $tags==null then {} else {tags:$tags} end)')
path="/api/v1/memories/$id"
if [ "${MEMDOCK_MODE:-}" = secure ] || [ -n "${MEMDOCK_READ_TOKEN:-}" ]; then
  script_dir=$(dirname "$0")
  sh "$script_dir/prepare-approval.sh" PATCH "$path" "$payload"
  exit 0
fi
key_file=${MEMDOCK_APPROVAL_KEY_FILE:-${XDG_CONFIG_HOME:-$HOME/.config}/memdock/approval.key}
[ -r "$key_file" ] || { echo "missing per-write approval key file: $key_file" >&2; exit 1; }
key=$(tr -d '\r\n' < "$key_file")
[ "${#key}" -ge 32 ] || { echo "approval key must contain at least 32 bytes" >&2; exit 1; }
timestamp=$(date +%s)
nonce=$(openssl rand -hex 16)
signature=$(printf '%s\n%s\nPATCH\n%s\n%s' "$timestamp" "$nonce" "$path" "$payload" | openssl dgst -sha256 -hmac "$key" | awk '{print $NF}')
printf '%s' "$payload" | curl -fsS -X PATCH -H "Authorization: Bearer ${MEMDOCK_READ_TOKEN:-}" -H 'Content-Type: application/json' -H "X-MemDock-Write-Signature: $timestamp:$nonce:$signature" -d @- "$MEMDOCK_URL$path"
