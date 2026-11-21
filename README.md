# MemDock

English | [简体中文](README.zh-CN.md)

MemDock is a shared long-term memory service for coding agents. It provides a Go/SQLite HTTP API, an MCP Streamable HTTP endpoint, and a web management UI. Agent scripts require explicit interactive confirmation for writes. Production deployment acceptance and mobile access remain outstanding.

## Features

- **Project auto-identification**: Git remotes resolve to projects automatically, so memories are grouped and isolated per project and shared across devices and agents; repositories can be bound/unbound and projects merged.
- **Long-term memory management**: create, update (with revision history), soft-delete, restore, and permanent-delete; each memory carries a title, content, category, tags, source, and timestamps.
- **Full-text search tuned for Chinese**: SQLite FTS5 `trigram` tokenizer with keyword fallback handles Chinese phrases, mixed Chinese/English queries, and technical abbreviations; BM25 ranking, category/tag filters, project-scoped by default, and a small default result set (top 5) to avoid polluting the agent's context.
- **Project libraries and shared libraries**: each project gets a default knowledge base; shared libraries hold cross-project memory with separate read/write authorization.
- **User-driven writes**: the server never summarizes sessions on its own — save/update/delete are triggered explicitly by the user; agents only decide when to recall ("Users decide what to remember; agents decide when to recall").
- **Three access channels**: REST API under `/api/v1`, MCP Streamable HTTP at `/mcp` (memory tools plus `project_list`, `project_lookup`, `project_resolve`, and `project_info`), and Codex/Pi Agent Skills (`mem-save`, `mem-search`).
- **Web management UI**: dashboard, project management (rename, bind/unbind repositories, merge, delete), memory management (search, filter, edit, delete/restore), and system status.
- **Two security modes**: `private` for trusted networks with unauthenticated reads and shared-secret API/MCP writes; `secure` requires HTTPS and the shared secret for all requests except health checks.
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

Private mode is the default and keeps existing API/MCP automation contracts unchanged. Web Console management writes require an administrator session. Before first startup, set `MEMDOCK_ADMIN_PASSWORD` (at least 6 characters); the server stores a password hash in SQLite, and the password can later be changed in the Console. Private API/MCP reads remain unauthenticated, so keep the service on a trusted private network. Set `MEMDOCK_WRITE_SECRET` to a random value of at least 32 bytes for API/MCP writes. Ordinary HTTP does not encrypt passwords, session cookies, or data: use direct HTTPS on MemDock (`MEMDOCK_TLS_CERT`/`MEMDOCK_TLS_KEY`) or a trusted encrypted network; never use HTTP on an untrusted network. Private mode does not support TLS terminated at a reverse proxy; proxy-terminated TLS is supported only in Secure mode.

## Usage Example

A complete round trip in Private mode: start the server, save a decision from an agent session, and recall it in a later session.

**1. Start the server**

```sh
: "${MEMDOCK_ADMIN_PASSWORD:?Set a strong administrator password of at least 6 characters first}"
MEMDOCK_MODE=private MEMDOCK_WRITE_SECRET=$(openssl rand -hex 32) \
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

In Secure mode, provide the shared `MEMDOCK_WRITE_SECRET` to the client. Writes occur directly after the script's explicit user confirmation.

## Configuration

All configuration is done through environment variables:

| Variable | Mode | Description |
| --- | --- | --- |
| `MEMDOCK_ADDR` | all | Listen address, default `:8080`. |
| `MEMDOCK_DB_PATH` | all | SQLite database path, default `memdock.db`. |
| `MEMDOCK_MODE` | all | `private` (default) or `secure`. |
| `MEMDOCK_ADMIN_PASSWORD` | private | Initial Private Console administrator password, at least 6 characters; only used when the database has no administrator credential. Inject via deployment environment; do not commit or log it. Password hash and sessions persist in SQLite. |
| `MEMDOCK_WRITE_SECRET` | all | At least 32 bytes. Required in Secure mode for all access except `/healthz`; in Private mode protects API/MCP writes. Send `X-MemDock-Write-Secret`; Bearer or Basic username `memdock` are also accepted. Anyone holding it can read/write in Secure mode. |
| `MEMDOCK_TLS_CERT`, `MEMDOCK_TLS_KEY` | all | When set as a pair, the service serves HTTPS directly. |
| `MEMDOCK_TRUST_PROXY_TLS` | secure | `true` to trust proxy-terminated TLS; must also set `MEMDOCK_TRUST_PROXY_CIDRS` (comma-separated CIDRs). The service verifies that the connection source IP is inside the trusted CIDRs and that `X-Forwarded-Proto` is exactly `https`. The proxy must still strip client-supplied forwarding headers and rewrite them; never let untrusted sources reach the backend port directly. Private mode does not support TLS termination at a proxy. |

The Private Web Console uses a random HttpOnly, SameSite=Strict session cookie and CSRF tokens. Secure mode requires the shared secret for browser access too; use HTTP Basic username `memdock` and the secret as password. Secure browser writes reject cross-origin `Origin` and `Sec-Fetch-Site` requests to mitigate CSRF; non-browser clients without browser-origin headers remain supported. Existing `MEMDOCK_APPROVAL_KEY`, HMAC signatures, separate read/write tokens, ACL files, and Ed25519 approval setup are no longer used.

### Secure mode authentication

Secure startup requires a `MEMDOCK_WRITE_SECRET` of at least 32 bytes and direct TLS certificates or a trusted TLS proxy boundary. `/healthz` is the only unauthenticated route. All REST, MCP, Console, and backup requests require the same secret. A secret holder can perform all operations; project isolation remains data-level, not user ACL-based.

Separate Secure read/write tokens, project ACLs, and Admin tokens have been removed. Configure one `MEMDOCK_WRITE_SECRET` for the single trusted operator/client.

### Secure writes

Secure clients can write directly after explicit user intent; there is no separate approval CLI or per-write signature. Legacy approval request files and `keygen` / `approve-request` commands are unsupported.

Private mode leaves reads unauthenticated and requires the same secret for API/MCP writes; Console writes retain the administrator session.

## Docker / Compose

The repository ships [.env.example](.env.example). Compose reads the root `.env` automatically; configure `MEMDOCK_WRITE_SECRET` (at least 32 bytes) and, for Private Console, an initial `MEMDOCK_ADMIN_PASSWORD`. Never commit credentials.

```sh
# Configure one shared write secret; keep it safe and uncommitted.
umask 077
cp .env.example .env
# Edit .env: set MEMDOCK_ADMIN_PASSWORD to a strong password (12+ characters),
# and set MEMDOCK_WRITE_SECRET to the value produced by:
openssl rand -hex 32
docker compose up -d --build
docker compose ps
docker compose logs -f memdock
```

For Secure mode, set `MEMDOCK_MODE=secure`, configure `MEMDOCK_WRITE_SECRET`, and provide TLS directly or configure a trusted proxy TLS boundary. The old ACL and token configuration is obsolete. When using `docker run --env-file .env` directly, server environment variables in the file pass into the container; Compose-specific port and mount variables still need separate `-p`, `-v`, and similar flags.

By default the service is published only to the host's `127.0.0.1:8080`, the container runs as non-root UID/GID 10001, and SQLite lives in the persistent volume `memdock-data`. For Private mode, use direct TLS configured on MemDock or access its HTTP listener only through a trusted encrypted network such as the Tailnet; do not use `tailscale serve` or another TLS-terminating proxy for the Private Console, because Private mode accepts HTTPS origins/cookies only when TLS reaches the server directly. Secure mode supports a trusted TLS-terminating proxy as described below. Never publish the default HTTP port to the public internet.

### TLS options for Secure mode

Secure may terminate TLS at a trusted reverse proxy: ensure only the proxy reaches the backend, set `MEMDOCK_TRUST_PROXY_TLS=true` and `MEMDOCK_TRUST_PROXY_CIDRS` to the proxy source CIDRs. The proxy must override forwarding headers. Alternatively provide `MEMDOCK_TLS_CERT` and `MEMDOCK_TLS_KEY` for direct TLS.

### Secure setup

Configure `MEMDOCK_MODE=secure`, set a random `MEMDOCK_WRITE_SECRET` of at least 32 bytes, and configure direct TLS or a trusted TLS proxy boundary. Give this secret only to clients authorized for all Secure operations. Secure Web, REST, MCP, and scripts authenticate with the same secret and perform direct writes after explicit user intent. Old approval CLI, token, public-key and ACL configuration is unsupported.



Write scripts require an interactive terminal and explicit `y` confirmation before making changes. MCP clients must attach `X-MemDock-Write-Secret` to `/mcp` requests.

```json
{"project_id":"PROJECT_ID","title":"user-confirmed title","content":"user-confirmed content"}
```



## Agent Skills

`skills/mem-save` and `skills/mem-search` carry skill metadata recognized by Codex/Pi. Pi loads them via `pi --skill /path/to/memdock/skills/mem-search/SKILL.md` (or `mem-save/SKILL.md`). The Codex CLI loads skills from `$CODEX_HOME/skills`, default `~/.codex/skills`; symlink this repository's skill directories:

```sh
codex_skills_dir="${CODEX_HOME:-$HOME/.codex}/skills"
mkdir -p "$codex_skills_dir"
ln -s /path/to/memdock/skills/mem-search "$codex_skills_dir/mem-search"
ln -s /path/to/memdock/skills/mem-save "$codex_skills_dir/mem-save"
```

### Configuring the Skill connection

The scripts talk to the server through `MEMDOCK_URL`; it has no default and must be visible to the agent's shell. Set it to the server's IP/port or hostname:

```sh
# Same machine:
export MEMDOCK_URL=http://127.0.0.1:8080
# LAN / another device (direct `go run` listens on all interfaces; Compose publishes 127.0.0.1 only):
export MEMDOCK_URL=http://192.168.1.10:8080
# Tailscale / domain with HTTPS:
export MEMDOCK_URL=https://memdock.example
```

Launch the agent with `MEMDOCK_URL` and `MEMDOCK_WRITE_SECRET` set (Secure mode uses the secret for reads and writes). Write scripts require interactive `y` confirmation. Optional overrides: `MEMDOCK_PROJECT_ID`, `MEMDOCK_KNOWLEDGE_BASE_ID`, and `MEMDOCK_REPOSITORY`.

## API & Clients

Health check: `GET /healthz`; management UI: `/`; MCP: `/mcp`. Secure requires `X-MemDock-Write-Secret` on all requests except health checks. Private reads remain unauthenticated; API/MCP writes require that header.

### MCP client configuration

Point the MCP client at the Streamable HTTP endpoint `/mcp` on the same server. Nothing needs to be installed client-side — the MemDock server itself is the MCP server. Private reads need no credential; API/MCP writes require `X-MemDock-Write-Secret`. Secure mode requires that header for every route except `/healthz`.

For project identification, the client determines its own Git remote and calls `project_lookup` with `repository` (or an existing `project_id`). Verify the project ID against the current directory binding or explicit user selection; do not guess from names. Private mode reads are unauthenticated and writes require the shared secret. Secure mode requires the same shared secret on all requests.

Pi:

```sh
pi mcp add memdock --url http://127.0.0.1:8080/mcp
# Secure mode also configures X-MemDock-Write-Secret in the MCP client's headers.
```

The command writes the user-level `~/.pi/agent/mcp.json` (all projects); add `-l` to write the project-level `.pi/mcp.json` instead (trusted projects only). Tools appear as `mcp__memdock__memory_search`, `mcp__memdock__memory_save`, and so on; run `pi mcp list` to verify the connection.

For Private-mode writes via MCP, add the write key header in `~/.pi/agent/mcp.json`:

```json
{
  "mcpServers": {
    "memdock": {
      "url": "http://100.79.6.101:18080/mcp",
      "headers": { "X-MemDock-Write-Secret": "<MEMDOCK_WRITE_SECRET>" }
    }
  }
}
```

Codex (`~/.codex/config.toml` for all projects, or `.codex/config.toml` in a trusted project):

```toml
[mcp_servers.memdock]
url = "http://127.0.0.1:8080/mcp"
# Secure mode requires the secret on every request; Private mode requires it for writes.
http_headers = { X-MemDock-Write-Secret = "<MEMDOCK_WRITE_SECRET>" }
```

## SQLite Backup & Restore

A consistent snapshot can be downloaded from `GET /api/v1/backup`. Secure mode requires the shared secret; the endpoint creates a standalone snapshot with SQLite `VACUUM INTO` and runs `integrity_check` before returning it.

Before restoring, save a full snapshot via the backup API, then stop the service and preserve the original database directory (including any WAL/SHM sidecars):

```sh
curl --fail --cacert trusted-ca.pem -H "X-MemDock-Write-Secret: $MEMDOCK_WRITE_SECRET" \
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
