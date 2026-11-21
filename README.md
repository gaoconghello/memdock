# MemDock

English | [简体中文](README.zh-CN.md)

MemDock is a shared long-term memory service for coding agents. It provides a Go/SQLite HTTP API, an MCP Streamable HTTP endpoint, and a web management UI. Retrieval through local Pi/Codex skills, Secure proposal generation, and post-CLI-approval retrieval have been verified; per-write human confirmation, identity isolation, mobile access, and production deployment acceptance are still outstanding.

## Features

- **Project auto-identification**: Git remotes resolve to projects automatically, so memories are grouped and isolated per project and shared across devices and agents; repositories can be bound/unbound and projects merged.
- **Long-term memory management**: create, update (with revision history), soft-delete, restore, and permanent-delete; each memory carries a title, content, category, tags, source, and timestamps.
- **Full-text search tuned for Chinese**: SQLite FTS5 `trigram` tokenizer with keyword fallback handles Chinese phrases, mixed Chinese/English queries, and technical abbreviations; BM25 ranking, category/tag filters, project-scoped by default, and a small default result set (top 5) to avoid polluting the agent's context.
- **Project libraries and shared libraries**: each project gets a default knowledge base; shared libraries hold cross-project memory with separate read/write authorization.
- **User-driven writes**: the server never summarizes sessions on its own — save/update/delete are triggered explicitly by the user; agents only decide when to recall ("Users decide what to remember; agents decide when to recall").
- **Three access channels**: REST API under `/api/v1`, MCP Streamable HTTP at `/mcp` (tools: `memory_search`, `memory_save`, `memory_list`, `memory_update`, `memory_delete`, `project_info`), and Codex/Pi Agent Skills (`mem-save`, `mem-search`).
- **Web management UI**: dashboard, project management (rename, bind/unbind repositories, merge, delete), memory management (search, filter, edit, delete/restore), and system status.
- **Two security modes**: `private` for trusted networks with HMAC write-integrity protection; `secure` with tokens + project ACL, read-only agent tokens, and an independent Ed25519 approval CLI for writes.
- **Lightweight single-service deployment**: one Go binary + SQLite, no external database, no embedding/AI model service; Docker/Compose with persistent volume, health check, and built-in backup/restore.

## Quick Start

Requires Go 1.24+:

```sh
MEMDOCK_MODE=private MEMDOCK_DB_PATH=./data/memdock.db go run ./cmd/server
```

Once running:

- Health check: `GET /healthz` (no authentication)
- Management UI: `http://localhost:8080/`
- MCP endpoint: `http://localhost:8080/mcp`

Private mode is the default and requires no further configuration, but it provides no application-layer read authentication — keep it on a trusted private network.

## Usage Example

A complete round trip in Private mode: start the server, save a decision from an agent session, and recall it in a later session.

**1. Start the server**

```sh
MEMDOCK_MODE=private MEMDOCK_APPROVAL_KEY=$(openssl rand -hex 32) \
MEMDOCK_DB_PATH=./data/memdock.db go run ./cmd/server
```

**2. Save a memory (user explicitly triggers it)**

In a Pi/Codex session inside a Git repository, tell the agent what to remember (e.g. "记住刚才确定的登录方案"); the agent runs the `mem-save` skill, which calls:

```sh
MEMDOCK_URL=http://localhost:8080 ./scripts/mem-save.sh \
  "登录鉴权方案" \
  "项目采用 JWT 身份认证，刷新令牌存于 Redis，有效期 7 天。" \
  "auth,jwt"
```

The script shows a preview and asks for `y` in the terminal; the project is resolved automatically from the Git remote, and the server returns:

```json
{"id":"mem_xxx","knowledge_base_id":"kb_xxx","title":"登录鉴权方案","created_at":"2026-..."}
```

Scripts require `curl` and `jq`.

**3. Recall it later (agent can do this automatically)**

In a new session — or from another device/agent using the same repository — the `mem-search` skill retrieves context:

```sh
MEMDOCK_URL=http://localhost:8080 ./scripts/mem-search.sh "JWT 身份认证"
```

```json
{
  "project_id": "prj_xxx",
  "results": [
    {
      "id": "mem_xxx",
      "title": "登录鉴权方案",
      "content": "项目采用 JWT 身份认证，刷新令牌存于 Redis，有效期 7 天。",
      "category": "",
      "knowledge_base_id": "kb_xxx",
      "knowledge_base_name": "默认项目库",
      "knowledge_base_type": "project",
      "updated_at": "2026-..."
    }
  ]
}
```

**4. REST API / Web UI equivalents**

```sh
curl -s -X POST http://localhost:8080/api/v1/memories/search \
  -H 'Content-Type: application/json' \
  -d '{"repository":"https://github.com/you/your-repo.git","query":"JWT 身份认证","limit":5}'
```

Or open `http://localhost:8080/` to browse, edit, and delete memories in the browser.

In Secure mode steps are identical except that write scripts/MCP tools produce an approval request file instead of writing directly; an independent approver runs `memdock-server approve-request` (see the Secure sections below).

## Configuration

All configuration is done through environment variables:

| Variable | Mode | Description |
| --- | --- | --- |
| `MEMDOCK_ADDR` | all | Listen address, default `:8080`. |
| `MEMDOCK_DB_PATH` | all | SQLite database path, default `memdock.db`. |
| `MEMDOCK_MODE` | all | `private` (default) or `secure`. |
| `MEMDOCK_APPROVAL_KEY` | private | HMAC key for write endpoints, at least 32 bytes; protects request integrity and prevents replay. It is not user authorization. |
| `MEMDOCK_READ_TOKEN` | secure | Required. At least 32 bytes; read operations only. |
| `MEMDOCK_WRITE_TOKEN` | secure | Required. At least 32 bytes and must differ from the read token; held only by the approval operator, authenticates write requests. |
| `MEMDOCK_APPROVAL_PUBLIC_KEY` | secure | Required. Hex-encoded Ed25519 32-byte public key. The service never holds the approval private key. |
| `MEMDOCK_PROJECT_ACL_FILE` | secure | Required. Project ACL JSON file; credentials are registered only as SHA-256 digests. Missing, unlisted, or unauthorized projects are denied by default. |
| `MEMDOCK_TLS_CERT`, `MEMDOCK_TLS_KEY` | all | When set as a pair, the service serves HTTPS directly. |
| `MEMDOCK_TRUST_PROXY_TLS` | secure | `true` to trust proxy-terminated TLS; must also set `MEMDOCK_TRUST_PROXY_CIDRS` (comma-separated CIDRs). The service verifies that the connection source IP is inside the trusted CIDRs and that `X-Forwarded-Proto` is exactly `https`. The proxy must still strip client-supplied forwarding headers and rewrite them; never let untrusted sources reach the backend port directly. |

### Secure mode authorization model

Secure startup fails if the read/write credentials, public key, project ACL, or TLS/trusted-proxy configuration is missing. `/healthz` needs no authentication; all other API/MCP/Web access uses a Bearer token, or Web Basic password matching the token SHA-256 registered in the ACL.

ACL `role` is `read`, `write`, or `admin`. Project permissions for read/write come only from the IDs explicitly listed under `projects` (an empty list grants no projects); the shared library is authorized separately via `shared_read` / `shared_write`. Project creation, global counts in status, and full database backup are available to `admin` only. Unauthorized projects are rejected; project/knowledge-base listings return only authorized items. Read tokens are enforced read-only, and write tokens must also be explicitly granted project scope in the ACL. Changing the ACL file requires a service restart. Private mode remains full-service access within a trusted network with no project identity ACL.

### Secure ACL example

Replace tokens with real random values of at least 32 bytes; store only the SHA-256 digests in `config/project-acl.json` with file permissions `0600`:

```json
{
  "credentials": [
    {"token_sha256":"<READ_TOKEN_SHA256>","role":"read","projects":["prj_example"],"shared_read":true},
    {"token_sha256":"<WRITE_TOKEN_SHA256>","role":"write","projects":["prj_example"],"shared_read":true,"shared_write":true},
    {"token_sha256":"<ADMIN_TOKEN_SHA256>","role":"admin"}
  ]
}
```

Digest example: `printf %s "$TOKEN" | sha256sum | cut -d ' ' -f1`. `MEMDOCK_READ_TOKEN` and `MEMDOCK_WRITE_TOKEN` must map to the `read` / `write` credentials in the ACL; Admin uses a separate credential that is never given to the Agent. New projects are created by the Admin using `MEMDOCK_APPROVER_TOKEN` (the approval CLI defaults to `MEMDOCK_WRITE_TOKEN`), then the project ID is added to the grants of the tokens that need access and the service is restarted; shared-library and full-backup permissions are configured separately.

### Secure write approval flow

Secure writes are generated by the Web UI, the write skill script, and MCP tools as `{"method":"POST","path":"/api/v1/...","body":{...}}` approval request files; they never modify data directly. The Agent hands the file to an independent approver over a trusted channel; the approver runs `memdock-server approve-request REQUEST.json` in an OS account/terminal the Agent cannot control. The CLI shows the full request body and requires typing `approve` in a TTY, then signs and submits the HTTPS request directly with the Ed25519 private key and the approver's credentials. Ordinary project writes use the ACL-authorized `MEMDOCK_WRITE_TOKEN`; Admin project initialization uses `MEMDOCK_APPROVER_TOKEN`. The legacy form `approve METHOD /api/v1/path BODY.json` still works. Private key permissions are `0600`; the server holds only the public key. **If the Agent shares a UID with the approver, can access the approval private key/write token, controls the approval terminal, or controls the Docker daemon, the isolation boundary is void.**

The Secure Web/Skill/MCP approval-file chain provides executable request generation and independent CLI approval; handoff across real, distinct OS identities/terminals still requires deployment acceptance. Private mode keeps the HMAC scheme (integrity/replay protection only, not strict approval).

## Docker / Compose

The repository ships [.env.example](.env.example) and an [ACL example](config/project-acl.example.json). Compose reads the root `.env` automatically; copy the examples and fill in real values, and never commit credentials. Private mode needs no ACL file.

```sh
# Private mode example: generate host-local HMAC config; keep it safe and uncommitted.
umask 077
cp .env.example .env
# Edit .env and set MEMDOCK_APPROVAL_KEY to the value produced by:
openssl rand -hex 32
docker compose up -d --build
docker compose ps
docker compose logs -f memdock
```

Secure mode additionally copies `config/project-acl.example.json` to `config/project-acl.json`, replaces the digests and project IDs, and completes the token, public key, and TLS configuration as described below. The placeholders in the examples must not be used to start the service. When using `docker run --env-file .env` directly, server environment variables in the file pass into the container; Compose-specific port and mount variables still need separate `-p`, `-v`, and similar flags.

By default the service is published only to the host's `127.0.0.1:8080`, the container runs as non-root UID/GID 10001, and SQLite lives in the persistent volume `memdock-data`. For a Tailnet deployment, use `tailscale serve` to expose the local port as trusted HTTPS; do not publish the default HTTP port to the public internet.

### TLS options for Secure mode

Secure can terminate TLS at a trusted reverse proxy: ensure the network only lets the proxy reach the backend, set `MEMDOCK_MODE=secure`, the read/write tokens, the public key, the project ACL file, `MEMDOCK_TRUST_PROXY_TLS=true`, and `MEMDOCK_TRUST_PROXY_CIDRS` matching the proxy's actual source addresses. Any source that spoofs `X-Forwarded-Proto: https` is still rejected because its source IP is untrusted; a trusted proxy must override the forwarding headers submitted by clients. Alternatively, provide a certificate/key so the service serves TLS itself: Compose mounts `${MEMDOCK_TLS_DIR:-./certs}` read-only at `/certs`, and `.env` sets `MEMDOCK_TLS_CERT=/certs/fullchain.pem` and `MEMDOCK_TLS_KEY=/certs/privkey.pem`. The certificate and private key must be readable by container UID 10001 and not modifiable by the Agent.

### Secure identity setup (performed by the deployment operator)

1. Run `memdock-server keygen /secure/path/approval.key` under an **OS account different from the Agent's**; save the Ed25519 public key it prints into the server's `MEMDOCK_APPROVAL_PUBLIC_KEY`. Keep the private key only in the approver's account, created with `0600`.
2. Generate `MEMDOCK_READ_TOKEN` and `MEMDOCK_WRITE_TOKEN`, each at least 32 random bytes and different from each other; generate a separate Admin token. Compute the SHA-256 of each token and grant explicit role, project IDs, and shared permissions in `config/project-acl.json`. Set the ACL file to `0600`; Secure Compose mounts `./config` read-only and reads `/config/project-acl.json`. `.env` sets `MEMDOCK_MODE=secure`, the read/write tokens, the public key, and TLS settings, or the trusted-proxy CIDRs. Secure does not use Private mode's HMAC `MEMDOCK_APPROVAL_KEY`. The Agent holds only its `read` token; the Admin token is never given to the Agent. Never allow the Agent to access the Docker daemon or deployment files containing credentials.
3. Web, the Secure write skill script, and MCP write tools generate approval request JSON instead of writing directly. The Agent hands the file to the approver over a trusted channel; the approver runs in their own interactive terminal:

```sh
MEMDOCK_URL=https://memdock.example \
MEMDOCK_WRITE_TOKEN='approver-only write token' \
MEMDOCK_APPROVAL_PRIVATE_KEY_FILE=/secure/path/approval.key \
./memdock-server approve-request request.json
```

The CLI displays the full JSON to be submitted (terminal control/bidirectional text is escaped) and requires typing `approve` in the terminal before sending a one-time request. The signature binds the HTTP method, path, and body, is valid for about one minute, and the server rejects replays; the final REST write also checks the approval token's ACL scope on the target project/knowledge base. New projects are created by the Admin and added to an authorized grant, then the service is restarted. For Admin operations, set `MEMDOCK_APPROVER_TOKEN='Admin token'` (it overrides the write token). Never hand the private key, any approval token, or the approver's terminal to the Agent.

Secure Web downloads approval request files; Secure `mem-save` generates a `0600` request file after the user explicitly triggers it, and also asks for `y` when a TTY is present — without a TTY it only produces a request pending independent approval; the mem-update/delete/kb-create/project-rename scripts write request files after interactive confirmation; MCP write tools return a structured `approval_request`. Request files must be handed to an independent approver over a trusted channel. Generating a file does not mean it was approved or saved.

```json
{"project_id":"PROJECT_ID","title":"user-confirmed title","content":"user-confirmed content"}
```

To create a new project/bind a repository, the Admin prepares `{"method":"POST","path":"/api/v1/projects/resolve","body":{"name":"project name","repository":"https://example/repo.git"}}`, sets `MEMDOCK_APPROVER_TOKEN`, and runs `approve-request project.json`; then add the new project ID to the needed identity grants and restart the service. Agents cannot access the new project before the ACL is updated.

## Agent Skills

`skills/mem-save` and `skills/mem-search` carry skill metadata recognized by Codex/Pi. Pi loads them via `pi --skill /path/to/memdock/skills/mem-search/SKILL.md` (or `mem-save/SKILL.md`). The Codex CLI loads skills from `$CODEX_HOME/skills`, default `~/.codex/skills`; symlink this repository's skill directories:

```sh
codex_skills_dir="${CODEX_HOME:-$HOME/.codex}/skills"
mkdir -p "$codex_skills_dir"
ln -s /path/to/memdock/skills/mem-search "$codex_skills_dir/mem-search"
ln -s /path/to/memdock/skills/mem-save "$codex_skills_dir/mem-save"
```

## API & Clients

Health check: `GET /healthz`; management UI: `/`; MCP: `/mcp`. In Secure mode, write operations may only be initiated by the independent approval CLI; regular Agent clients hold only the read token. Agent read scripts can use the `MEMDOCK_READ_TOKEN` environment variable; MCP clients must configure the same Bearer token for `/mcp` requests. In Private mode the scripts still use HMAC, which does not provide strict authorization. Runtime identity separation depends on deployment accounts, file permissions, terminals, and Docker daemon permissions, and must be accepted separately.

## SQLite Backup & Restore

With an Admin identity, a consistent snapshot can be downloaded: `GET /api/v1/backup`. Save the response file using HTTPS and the Admin Bearer token (or browser Basic auth); the endpoint creates a standalone snapshot with SQLite `VACUUM INTO` and runs `integrity_check` before returning it.

Before restoring, save a full snapshot via the backup API, then stop the service and preserve the original database directory (including any WAL/SHM sidecars):

```sh
curl --fail --cacert trusted-ca.pem -H "Authorization: Bearer $MEMDOCK_READ_TOKEN" \
  https://memdock.example/api/v1/backup -o ./before-restore.sqlite
docker compose stop memdock
docker compose cp memdock:/data ./before-restore-data
# Put the backup to restore somewhere the container can read, then:
docker compose run --rm -v "$PWD:/restore:ro" memdock restore /restore/backup.sqlite /data/memdock.db
docker compose up -d memdock
```

`restore` runs `VACUUM INTO` on the source database inside a `0700` staging directory in the target directory, validates SQLite integrity, MemDock tables/columns/indexes/triggers, and the supported schema version before installing. It requires the service to be stopped; the old main database, `-wal`, and `-shm` files are first moved aside in the same directory, installation failure rolls back, and success cleans up the old files, so an old WAL is never applied to a new main database. After restoring, check container health, project/memory data, and logs; backup files must be stored encrypted as sensitive data and restore drills must be performed regularly. Whether the Compose commands can access the named volume with a given Docker Compose version should be confirmed in the deployment environment; the same image can also be invoked on the host with the data volume mounted directly.

## Verification

```sh
go test ./...
go vet ./...
go build ./cmd/server
git diff --check
```

AMD64/ARM64 image builds are available via:

```sh
docker buildx build --platform linux/amd64,linux/arm64 -t memdock:local .
```

Stage 4 verification environment, uncovered items, and security authorization blockers for this repository are recorded in `docs/tasks/04-部署安全与交付.md` and `docs/待确认问题.md`. Image builds, Tailscale multi-device, proxy TLS, backup/restore production drills, and user acceptance are not considered deployment-accepted until verified.
