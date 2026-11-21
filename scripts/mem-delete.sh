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
[ -n "${MEMDOCK_WRITE_SECRET:-}" ] && [ "${#MEMDOCK_WRITE_SECRET}" -ge 32 ] || { echo "set MEMDOCK_WRITE_SECRET (at least 32 bytes)" >&2; exit 1; }
printf '%s' "$payload" | curl -fsS -X DELETE -H "X-MemDock-Write-Secret: $MEMDOCK_WRITE_SECRET" -H 'Content-Type: application/json' -d @- "$MEMDOCK_URL$path"
