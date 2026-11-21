#!/bin/sh
set -eu
: "${MEMDOCK_URL:?set MEMDOCK_URL}"
: "${MEMDOCK_PROJECT_ID:?set MEMDOCK_PROJECT_ID (select the current project explicitly)}"
url="$MEMDOCK_URL/api/v1/projects/$MEMDOCK_PROJECT_ID/memories"
if [ -n "${MEMDOCK_TAG:-}" ]; then
  tag=$(jq -rn --arg value "$MEMDOCK_TAG" '$value|@uri')
  url="$url?tag=$tag"
fi
curl -fsS -H "X-MemDock-Write-Secret: ${MEMDOCK_WRITE_SECRET:-}" "$url"
