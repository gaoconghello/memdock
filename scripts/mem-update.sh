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
[ -n "${MEMDOCK_WRITE_SECRET:-}" ] && [ "${#MEMDOCK_WRITE_SECRET}" -ge 32 ] || { echo "set MEMDOCK_WRITE_SECRET (at least 32 bytes)" >&2; exit 1; }
printf '%s' "$payload" | curl -fsS -X PATCH -H "X-MemDock-Write-Secret: $MEMDOCK_WRITE_SECRET" -H 'Content-Type: application/json' -d @- "$MEMDOCK_URL$path"
