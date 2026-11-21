#!/bin/sh
set -eu

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then echo "usage: mem-save.sh TITLE CONTENT [TAGS_CSV]" >&2; exit 2; fi
: "${MEMDOCK_URL:?set MEMDOCK_URL}"
secure_mode=false
if [ "${MEMDOCK_MODE:-}" = secure ] || [ -n "${MEMDOCK_READ_TOKEN:-}" ]; then secure_mode=true; fi
if [ ! -t 0 ] && [ "$secure_mode" != true ]; then echo "mem-save requires an interactive terminal for Private-mode confirmation" >&2; exit 1; fi

title=$1
content=$2
tags_json=$(jq -cn --arg tags "${3:-}" 'if $tags=="" then [] else $tags|split(",") end')
repo=$(git rev-parse --show-toplevel 2>/dev/null || true)
if [ -n "$repo" ]; then
  remotes=$(git -C "$repo" remote)
  count=$(printf '%s\n' "$remotes" | sed '/^$/d' | wc -l | tr -d ' ')
  if [ "$count" -gt 1 ] && [ -z "${MEMDOCK_REPOSITORY:-}" ]; then echo "multiple Git remotes; set MEMDOCK_REPOSITORY to the intended remote URL" >&2; exit 1; fi
  remote=${MEMDOCK_REPOSITORY:-$(git -C "$repo" remote get-url origin 2>/dev/null || true)}
  if [ -z "$remote" ] && [ "$count" -eq 1 ]; then remote=$(git -C "$repo" remote get-url "$(printf '%s\n' "$remotes" | head -n1)"); fi
  project_name=${MEMDOCK_PROJECT_NAME:-$(basename "$repo")}
else
  remote=""
  project_name=${MEMDOCK_PROJECT_NAME:-$(basename "$PWD")}
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
if [ "$secure_mode" != true ] && [ -z "$remote" ] && [ -z "$project_id" ]; then
  matches=$(curl -fsS -H "Authorization: Bearer ${MEMDOCK_READ_TOKEN:-}" "$MEMDOCK_URL/api/v1/projects" | jq -c --arg name "$project_name" '[.projects[] | select(.name==$name)]')
  count=$(printf '%s' "$matches" | jq 'length')
  if [ "$count" -gt 0 ]; then
    printf '%s\n' '已有同名项目：' >&2
    printf '%s' "$matches" | jq -r 'to_entries[] | "  \(.key+1)) \(.value.id)"' >&2
    printf '输入编号选择，或输入 n 创建新项目：' >&2
    IFS= read -r choice
    if [ "$choice" != n ]; then
      case "$choice" in ''|*[!0-9]*) echo "invalid project selection" >&2; exit 1;; esac
      [ "$choice" -ge 1 ] && [ "$choice" -le "$count" ] || { echo "invalid project selection" >&2; exit 1; }
      project_id=$(printf '%s' "$matches" | jq -r ".[$((choice-1))].id")
    fi
  fi
fi
# Resolve an existing target without changing server state before confirmation.
if [ "$secure_mode" = true ]; then
  [ -n "${MEMDOCK_READ_TOKEN:-}" ] || { echo "set MEMDOCK_READ_TOKEN for Secure project lookup" >&2; exit 1; }
fi
project=""
if [ -n "$project_id" ] || [ -n "$remote" ]; then
  lookup_payload=$(jq -nc --arg repository "$remote" --arg project_id "$project_id" '{repository:$repository} + (if $project_id=="" then {} else {project_id:$project_id} end)')
  # A selected ID must exist; never replace a failed selection with a new project.
  if [ -n "$project_id" ] || [ "$secure_mode" = true ]; then
    project=$(printf '%s' "$lookup_payload" | curl -fsS -H "Authorization: Bearer ${MEMDOCK_READ_TOKEN:-}" -H 'Content-Type: application/json' -d @- "$MEMDOCK_URL/api/v1/projects/lookup") || exit 1
  else
    # A new Git remote can be initialized after the user confirms the target.
    lookup_file=$(mktemp)
    trap 'rm -f "$lookup_file"' EXIT HUP INT TERM
    status=$(printf '%s' "$lookup_payload" | curl -sS -o "$lookup_file" -w '%{http_code}' -H 'Content-Type: application/json' -d @- "$MEMDOCK_URL/api/v1/projects/lookup") || exit 1
    case "$status" in
      200) project=$(cat "$lookup_file") ;;
      404) ;;
      *) cat "$lookup_file" >&2; echo "project lookup failed ($status)" >&2; exit 1 ;;
    esac
    rm -f "$lookup_file"
    trap - EXIT HUP INT TERM
  fi
fi
if [ "$secure_mode" = true ] && [ -z "$project" ]; then
  echo "no project binding; select an authorized project before saving" >&2; exit 1
fi
if [ -n "$project" ]; then
  project_id=$(printf '%s' "$project" | jq -er '.project_id')
  target_name=$(printf '%s' "$project" | jq -er '.name')
  target_id=$project_id
else
  target_name=$project_name
  target_id='(new project)'
fi
printf 'Directory: %s\nTarget project: %s\nProject ID: %s\nKnowledge base: %s\nTitle: %s\nContent:\n%s\nTags: %s\n' "$binding_path" "$target_name" "$target_id" "${MEMDOCK_KNOWLEDGE_BASE_ID:-(project default)}" "$title" "$content" "$tags_json" >&2
if [ -t 0 ]; then
  printf 'Save this memory? Type y to confirm: ' >&2
  IFS= read -r answer
  [ "$answer" = y ] || { echo "cancelled" >&2; exit 1; }
else
  echo "Secure mode: preparing an approval request only; independent approval is required." >&2
fi
if [ "$secure_mode" = true ]; then
  payload=$(jq -nc --arg project_id "$project_id" --arg knowledge_base_id "${MEMDOCK_KNOWLEDGE_BASE_ID:-}" --arg title "$title" --arg content "$content" --argjson tags "$tags_json" '{project_id:$project_id,title:$title,content:$content,tags:$tags} + (if $knowledge_base_id=="" then {} else {knowledge_base_id:$knowledge_base_id} end)')
  script_dir=$(dirname "$0")
  sh "$script_dir/prepare-approval.sh" POST /api/v1/memories "$payload"
  exit 0
fi

key=${MEMDOCK_APPROVAL_KEY:-}
if [ -z "$key" ]; then
  key_file=${MEMDOCK_APPROVAL_KEY_FILE:-${XDG_CONFIG_HOME:-$HOME/.config}/memdock/approval.key}
  [ -r "$key_file" ] || { echo "set MEMDOCK_APPROVAL_KEY or provide per-write approval key file: $key_file" >&2; exit 1; }
  key=$(tr -d '\r\n' < "$key_file")
fi
[ "${#key}" -ge 32 ] || { echo "approval key must contain at least 32 bytes" >&2; exit 1; }
request_signature() {
  timestamp=$(date +%s)
  nonce=$(openssl rand -hex 16)
  signature=$(printf '%s\n%s\n%s\n%s\n%s' "$timestamp" "$nonce" "$1" "$2" "$3" | openssl dgst -sha256 -hmac "$key" | awk '{print $NF}')
  signature_header="$timestamp:$nonce:$signature"
}

resolve_payload=$(jq -nc --arg repository "$remote" --arg name "$project_name" --arg project_id "$project_id" '{repository:$repository,name:$name} + (if $project_id=="" then {} else {project_id:$project_id} end)')
request_signature POST /api/v1/projects/resolve "$resolve_payload"
project=$(printf '%s' "$resolve_payload" | curl -fsS -H 'Content-Type: application/json' -H "X-MemDock-Write-Signature: $signature_header" -d @- "$MEMDOCK_URL/api/v1/projects/resolve")
project_id=$(printf '%s' "$project" | jq -er '.project_id')
if [ -z "${MEMDOCK_PROJECT_ID:-}" ]; then
  mkdir -p "$binding_dir"
  printf '%s' "$project_id" > "$binding_file"
fi
kb=${MEMDOCK_KNOWLEDGE_BASE_ID:-}
payload=$(jq -nc --arg project_id "$project_id" --arg knowledge_base_id "$kb" --arg title "$title" --arg content "$content" --argjson tags "$tags_json" '{project_id:$project_id,title:$title,content:$content,tags:$tags} + (if $knowledge_base_id=="" then {} else {knowledge_base_id:$knowledge_base_id} end)')
request_signature POST /api/v1/memories "$payload"
printf '%s' "$payload" | curl -fsS -H 'Content-Type: application/json' -H "X-MemDock-Write-Signature: $signature_header" -d @- "$MEMDOCK_URL/api/v1/memories"
