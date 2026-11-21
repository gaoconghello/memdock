#!/bin/sh
set -eu
if [ "$#" -ne 1 ]; then echo "usage: mem-kb-create.sh NAME" >&2; exit 2; fi
: "${MEMDOCK_URL:?set MEMDOCK_URL}"
name=$1
[ -t 0 ] || { echo "shared library creation requires an interactive terminal" >&2; exit 1; }
printf 'Create shared knowledge base %s? Type y to confirm: ' "$name" >&2
IFS= read -r answer
[ "$answer" = y ] || { echo "cancelled" >&2; exit 1; }
payload=$(jq -nc --arg name "$name" '{name:$name,kind:"shared"}')
[ -n "${MEMDOCK_WRITE_SECRET:-}" ] && [ "${#MEMDOCK_WRITE_SECRET}" -ge 32 ] || { echo "set MEMDOCK_WRITE_SECRET (at least 32 bytes)" >&2; exit 1; }
printf '%s' "$payload" | curl -fsS -H "X-MemDock-Write-Secret: $MEMDOCK_WRITE_SECRET" -H 'Content-Type: application/json' -d @- "$MEMDOCK_URL/api/v1/knowledge-bases"
