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
[ -n "${MEMDOCK_WRITE_SECRET:-}" ] && [ "${#MEMDOCK_WRITE_SECRET}" -ge 32 ] || { echo "set MEMDOCK_WRITE_SECRET (at least 32 bytes)" >&2; exit 1; }
printf '%s' "$payload" | curl -fsS -X PATCH -H "X-MemDock-Write-Secret: $MEMDOCK_WRITE_SECRET" -H 'Content-Type: application/json' -d @- "$MEMDOCK_URL$path"
