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
[ -n "${MEMDOCK_WRITE_SECRET:-}" ] && [ "${#MEMDOCK_WRITE_SECRET}" -ge 32 ] || { echo "set MEMDOCK_WRITE_SECRET (at least 32 bytes)" >&2; exit 1; }
printf '%s' "$payload" | curl -fsS -H "X-MemDock-Write-Secret: $MEMDOCK_WRITE_SECRET" -H 'Content-Type: application/json' -d @- "$MEMDOCK_URL/api/v1/knowledge-bases"
