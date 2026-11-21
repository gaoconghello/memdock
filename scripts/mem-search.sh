#!/bin/sh
set -eu
if [ "$#" -lt 1 ]; then echo "usage: mem-search.sh QUERY" >&2; exit 2; fi
: "${MEMDOCK_URL:?set MEMDOCK_URL}"
repo=$(git rev-parse --show-toplevel 2>/dev/null || true)
if [ -n "$repo" ]; then
  remotes=$(git -C "$repo" remote)
  count=$(printf '%s\n' "$remotes" | sed '/^$/d' | wc -l | tr -d ' ')
  if [ "$count" -gt 1 ] && [ -z "${MEMDOCK_REPOSITORY:-}" ]; then echo "multiple Git remotes; set MEMDOCK_REPOSITORY to the intended remote URL" >&2; exit 1; fi
  remote=${MEMDOCK_REPOSITORY:-$(git -C "$repo" remote get-url origin 2>/dev/null || true)}
  if [ -z "$remote" ] && [ "$count" -eq 1 ]; then remote=$(git -C "$repo" remote get-url "$(printf '%s\n' "$remotes" | head -n1)"); fi
else
  remote=""
fi
binding_path=${repo:-$(pwd -P)}
if command -v sha256sum >/dev/null 2>&1; then
  binding_key=$(printf '%s' "$binding_path" | sha256sum | cut -c1-32)
elif command -v shasum >/dev/null 2>&1; then
  binding_key=$(printf '%s' "$binding_path" | shasum -a 256 | cut -c1-32)
else
  echo "sha256sum or shasum is required for stable local project binding" >&2; exit 1
fi
binding_dir=${XDG_CONFIG_HOME:-$HOME/.config}/memdock/projects
binding_file=$binding_dir/$binding_key
project_id="${MEMDOCK_PROJECT_ID:-}"
if [ -z "$project_id" ] && [ -f "$binding_file" ]; then project_id=$(cat "$binding_file"); fi
if [ -z "$remote" ] && [ -z "$project_id" ]; then
  echo "no local project binding; run mem-save once or set MEMDOCK_PROJECT_ID to select an existing project" >&2
  exit 1
fi
project=$(jq -nc --arg repository "$remote" --arg project_id "$project_id" '{repository:$repository} + (if $project_id=="" then {} else {project_id:$project_id} end)' | curl -fsS -H "Authorization: Bearer ${MEMDOCK_READ_TOKEN:-}" -H 'Content-Type: application/json' -d @- "$MEMDOCK_URL/api/v1/projects/lookup") || { echo "project is not initialized; use an explicit project setup/save action first" >&2; exit 1; }
project_id=$(printf '%s' "$project" | jq -er '.project_id')
kb=${MEMDOCK_KNOWLEDGE_BASE_ID:-}
payload=$(jq -nc --arg project_id "$project_id" --arg knowledge_base_id "$kb" --arg query "$1" '{project_id:$project_id,query:$query,limit:5} + (if $knowledge_base_id=="" then {} else {knowledge_base_id:$knowledge_base_id} end)')
printf '%s' "$payload" | curl -fsS -H "Authorization: Bearer ${MEMDOCK_READ_TOKEN:-}" -H 'Content-Type: application/json' -d @- "$MEMDOCK_URL/api/v1/memories/search"
