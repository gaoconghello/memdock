#!/bin/sh
set -eu
if [ "$#" -ne 3 ]; then echo "usage: prepare-approval.sh METHOD /api/v1/path BODY_JSON" >&2; exit 2; fi
method=$1
path=$2
body=$3
case "$method" in POST|PATCH|DELETE) ;; *) echo "unsupported approval method" >&2; exit 2;; esac
case "$path" in /api/v1/*) ;; *) echo "approval path must be local /api/v1/" >&2; exit 2;; esac
case "$path" in *\?*|*\#*|*..*) echo "approval path must not contain query, fragment, or traversal" >&2; exit 2;; esac
command -v jq >/dev/null 2>&1 || { echo "jq is required to prepare approval request" >&2; exit 1; }
umask 077
if [ -n "${MEMDOCK_APPROVAL_REQUEST_FILE:-}" ]; then
  file=$MEMDOCK_APPROVAL_REQUEST_FILE
  [ ! -e "$file" ] || { echo "refusing to overwrite existing request file: $file" >&2; exit 1; }
else
  file=$(mktemp "${TMPDIR:-/tmp}/memdock-approval.XXXXXX.json")
fi
printf '%s' "$body" | jq -e . >/dev/null || { echo "body must be valid JSON" >&2; exit 1; }
jq -n --arg method "$method" --arg path "$path" --argjson body "$body" '{method:$method,path:$path,body:$body}' > "$file"
chmod 600 "$file"
printf 'Approval request prepared: %s\nTransfer it via a trusted channel to the independent approver, then run: memdock-server approve-request %s\n' "$file" "$file" >&2
