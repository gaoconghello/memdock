# MemDock

[English](README.md) | 简体中文

MemDock 是面向 Coding Agent 的共享长期记忆服务，提供 Go/SQLite API、MCP Streamable HTTP 和 Web 管理页面。Agent 写入脚本要求交互确认；生产部署验收和移动设备访问仍未完成。

## 功能特性

- **项目自动识别**：通过 Git 仓库远程地址自动识别并归组项目，记忆按项目隔离，跨设备、跨 Agent 共享；支持绑定/解绑仓库和项目合并。
- **长期记忆管理**：创建、更新（保留修改历史）、软删除、恢复和永久删除；每条记忆含标题、正文、分类、标签、来源和时间戳。
- **支持中文的全文检索**：SQLite FTS5 `trigram` 分词器加关键词回退，覆盖中文短语、中英文混合查询和技术缩写；BM25 相关性排序、分类/标签筛选，默认限定当前项目，默认仅返回前 5 条，避免污染 Agent 上下文。
- **项目库与共享库**：每个项目有默认知识库；共享库存放跨项目记忆，读/写权限单独授权。
- **写入由用户驱动**：服务端不自动总结会话——保存/更新/删除均由用户明确触发，Agent 只决定何时回忆（“用户决定记住什么，Agent 决定什么时候需要回忆”）。
- **三种接入方式**：REST API（`/api/v1`）、MCP Streamable HTTP（`/mcp`，工具：`memory_search`、`memory_save`、`memory_list`、`memory_update`、`memory_delete`、`project_info`）、Codex/Pi Agent Skill（`mem-save`、`mem-search`）。
- **Web 管理界面**：首页概览、项目管理（重命名、绑定/解绑仓库、合并、删除）、记忆管理（搜索、筛选、编辑、删除/恢复）、系统状态。
- **两种安全模式**：`private` 面向可信网络，读取免认证、API/MCP 写入使用共享密钥；`secure` 强制 HTTPS，并对除健康检查外的所有请求使用同一密钥认证。
- **轻量单体部署**：单个 Go 二进制 + SQLite，无外部数据库、无 Embedding/AI 模型服务；Docker/Compose 持久化部署，内置健康检查与备份恢复。

## 快速开始

需要 Go 1.24+：

```sh
MEMDOCK_MODE=private MEMDOCK_DB_PATH=./data/memdock.db go run ./cmd/server
```

启动后：

- 健康检查：`GET /healthz`（无需认证）
- 管理界面：`http://localhost:8080/`
- MCP 端点：`http://localhost:8080/mcp`

Private 模式为默认模式；Web Console 的管理写入需管理员会话。首次启动前设置 `MEMDOCK_ADMIN_PASSWORD`（至少 6 位），服务将其安全哈希持久化到 SQLite。Private API/MCP 读取免认证，写入需设置至少 32 字节的 `MEMDOCK_WRITE_SECRET`，并应限制在可信私有网络内。普通 HTTP 不加密密码、Cookie 会话或数据；应配置 MemDock 直连 HTTPS（`MEMDOCK_TLS_CERT`/`MEMDOCK_TLS_KEY`）或仅通过可信加密网络访问，不要在不可信网络使用 HTTP。Private 模式不支持由反向代理终止 TLS；代理 TLS 终止仅支持 Secure 模式。

## 使用示例

以 Private 模式为例走一遍完整流程：启动服务，在 Agent 会话中保存一条决策，之后在新会话中召回它。

**1. 启动服务**

```sh
: "${MEMDOCK_ADMIN_PASSWORD:?请先安全设置至少 6 位管理员密码}"
MEMDOCK_MODE=private MEMDOCK_WRITE_SECRET=$(openssl rand -hex 32) \
MEMDOCK_DB_PATH=./data/memdock.db go run ./cmd/server
```

**2. 保存记忆（由用户明确触发）**

在 Git 仓库内开启 Pi/Codex 会话，告诉 Agent 要记住的内容（例如“记住刚才确定的登录方案”）；Agent 调用 `mem-save` Skill，底层执行：

```sh
MEMDOCK_URL=http://localhost:8080 ./scripts/mem-save.sh \
  "登录鉴权方案" \
  "项目采用 JWT 身份认证，刷新令牌存于 Redis，有效期 7 天。" \
  "auth,jwt"
```

脚本先展示预览并要求在终端输入 `y` 确认；项目从 Git 远程地址自动识别，服务端返回：

```json
{"id":"mem_xxx","knowledge_base_id":"kb_xxx","title":"登录鉴权方案","created_at":"2026-..."}
```

脚本依赖 `curl` 和 `jq`。

**3. 之后召回（Agent 可按需自动检索）**

在新会话中——或换一台设备、换一个 Agent 使用同一仓库——`mem-search` Skill 检索历史上下文：

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

**4. REST API / Web 界面等价操作**

```sh
curl -s -X POST http://localhost:8080/api/v1/memories/search \
  -H 'Content-Type: application/json' \
  -d '{"repository":"https://github.com/you/your-repo.git","query":"JWT 身份认证","limit":5}'
```

或打开 `http://localhost:8080/` 在浏览器中浏览、编辑、删除记忆。

Secure 模式的客户端需提供同一个 `MEMDOCK_WRITE_SECRET`；用户明确触发并确认后可直接写入。

## 配置

所有配置均通过环境变量完成：

| 变量 | 模式 | 说明 |
| --- | --- | --- |
| `MEMDOCK_ADDR` | 通用 | 监听地址，默认 `:8080`。 |
| `MEMDOCK_DB_PATH` | 通用 | SQLite 数据库路径，默认 `memdock.db`。 |
| `MEMDOCK_MODE` | 通用 | `private`（默认）或 `secure`。 |
| `MEMDOCK_ADMIN_PASSWORD` | private | 首次初始化 Private Console 管理员密码，至少 6 个字符；仅在数据库尚无管理员凭据时生效。妥善通过部署环境注入，勿提交或记录。密码哈希和管理员会话持久化于 SQLite。 |
| `MEMDOCK_WRITE_SECRET` | all | 至少 32 字节。Secure 模式必填，除 `/healthz` 外所有请求均需认证；Private 模式保护 API/MCP 写入。通过 `X-MemDock-Write-Secret` 传递。Secure 密钥持有者可读写全部功能。 |
| `MEMDOCK_TLS_CERT`、`MEMDOCK_TLS_KEY` | 通用 | 成对配置时服务直接提供 HTTPS。 |
| `MEMDOCK_TRUST_PROXY_TLS` | secure | `true` 表示只信任代理 TLS 模式；必须同时配置 `MEMDOCK_TRUST_PROXY_CIDRS`（逗号分隔 CIDR）。服务校验连接源 IP 位于可信 CIDR 且 `X-Forwarded-Proto` 精确为 `https`。代理仍须清理客户端转发头并重写；不能让不可信来源直接访问后端端口。Private 模式不支持代理终止 TLS。 |

Private Web Console 保留随机会话 Cookie（HttpOnly、SameSite=Strict）和 CSRF 防护。Secure Console 也要求共享密钥，可用 HTTP Basic 用户名 `memdock`、密码为该密钥登录；浏览器写入会校验同源 Origin/Fetch Metadata，拒绝跨站请求以防 CSRF。无浏览器来源头的非浏览器客户端仍可正常调用。旧 HMAC 签名、独立读写 token、ACL 与 Ed25519 审批配置已不再使用。

### Secure 模式认证

Secure 启动必须配置至少 32 字节的 `MEMDOCK_WRITE_SECRET` 和直连 TLS 或可信代理 TLS。`/healthz` 是唯一免认证端点；所有 REST、MCP、Console、备份请求使用同一密钥。持有密钥者可执行所有操作；项目数据隔离仍由项目 ID 实现，不提供用户 ACL。旧 token、ACL 和独立审批设置已失效。

旧版 Secure 的读写 token、项目 ACL、审批请求文件和 Ed25519 密钥流程均已移除；使用一个 `MEMDOCK_WRITE_SECRET` 即可认证所有访问和写入。

## Docker / Compose

仓库提供 [.env.example](.env.example)。Compose 会自动读取根目录的 `.env`；设置至少 32 字节的 `MEMDOCK_WRITE_SECRET` 和 Private Console 初始管理员密码。切勿提交凭据。

```sh
# 设置共享写密钥；妥善保管并勿提交。
umask 077
cp .env.example .env
# 编辑 .env：设置强管理员密码（至少 6 个字符），
# 将 MEMDOCK_WRITE_SECRET 设为以下命令生成的值：
openssl rand -hex 32
docker compose up -d --build
docker compose ps
docker compose logs -f memdock
```

Secure 模式设置 `MEMDOCK_MODE=secure`，并配置共享密钥及直连或可信代理 TLS；旧 token/ACL 设置已失效。直接使用 `docker run --env-file .env` 时，文件中的服务端环境变量会传入容器；Compose 专用的端口及挂载目录变量仍需通过 `-p`、`-v` 等参数单独配置。

默认仅发布到宿主机 `127.0.0.1:8080`，容器以 UID/GID 10001 非 root 运行，SQLite 位于持久卷 `memdock-data`。Private 模式应配置 MemDock 直连 TLS，或仅通过 Tailnet 等可信加密网络访问其 HTTP 监听端口；不要对 Private Console 使用 `tailscale serve` 或其他 TLS 终止代理，因为 Private 模式只在 TLS 直达服务端时接受 HTTPS Origin 并设置 Secure Cookie。Secure 模式支持下文所述的可信 TLS 终止代理。不要将默认 HTTP 端口直接发布到公网。

### Secure 模式 TLS 配置

Secure 可由可信反向代理终止 TLS：确保网络层仅代理可访问后端，设置 `MEMDOCK_MODE=secure`、`MEMDOCK_WRITE_SECRET`、`MEMDOCK_TRUST_PROXY_TLS=true` 及代理实际源地址对应的 `MEMDOCK_TRUST_PROXY_CIDRS`。任意来源即使伪造 `X-Forwarded-Proto: https` 也会因源 IP 不可信而拒绝；可信代理须覆盖客户端提交的转发头。也可设置证书/key 让服务直接 TLS；Compose 将 `${MEMDOCK_TLS_DIR:-./certs}` 只读挂载到 `/certs`，`.env` 设置 `MEMDOCK_TLS_CERT=/certs/fullchain.pem` 与 `MEMDOCK_TLS_KEY=/certs/privkey.pem`。证书和私钥须允许容器 UID 10001 读取且不能被 Agent 修改。

### 旧版 Secure 身份配置（已废弃，不要使用）

旧版 keygen、Ed25519、公钥/私钥和分离操作系统身份方案均已移除。
旧版分读写 token 和项目 ACL 配置均已失效；请改用单一 `MEMDOCK_WRITE_SECRET`。
Secure 客户端使用统一密钥认证，用户明确触发并确认后直接写入。

```sh
MEMDOCK_URL=https://memdock.example \
MEMDOCK_WRITE_SECRET='随机生成的共享密钥'

```

Secure API、MCP、Console 和脚本均使用同一密钥读写；密钥持有者拥有完整访问权限。

写入脚本需要交互终端并要求用户明确输入 `y` 确认；MCP 使用 `X-MemDock-Write-Secret` 请求头。

```json
{"project_id":"项目ID","title":"用户确认的标题","content":"用户确认的内容"}
```



## Agent Skills

`skills/mem-save` 和 `skills/mem-search` 含 Codex/Pi 可识别的 Skill 元数据。Pi 可通过 `pi --skill /path/to/memdock/skills/mem-search/SKILL.md`（或 `mem-save/SKILL.md`）加载。Codex CLI 从 `$CODEX_HOME/skills` 加载 Skill，默认目录为 `~/.codex/skills`；可链接本仓库的 Skill 目录：

```sh
codex_skills_dir="${CODEX_HOME:-$HOME/.codex}/skills"
mkdir -p "$codex_skills_dir"
ln -s /path/to/memdock/skills/mem-search "$codex_skills_dir/mem-search"
ln -s /path/to/memdock/skills/mem-save "$codex_skills_dir/mem-save"
```

### 配置 Skill 连接

脚本通过 `MEMDOCK_URL` 访问服务，该变量没有默认值，必须对 Agent 的 shell 可见。按服务所在的 IP、端口或域名设置：

```sh
# 本机：
export MEMDOCK_URL=http://127.0.0.1:8080
# 局域网/其他设备（直接 `go run` 默认监听所有网卡；Compose 仅发布 127.0.0.1）：
export MEMDOCK_URL=http://192.168.1.10:8080
# Tailscale/域名 HTTPS：
export MEMDOCK_URL=https://memdock.example
```

启动 Agent 时设置 `MEMDOCK_URL` 和 `MEMDOCK_WRITE_SECRET` 并让子 shell 继承。Secure 模式用该密钥认证所有访问，Private 模式仅用于写入。脚本写入前要求交互输入 `y`。可选变量：`MEMDOCK_PROJECT_ID`、`MEMDOCK_KNOWLEDGE_BASE_ID`、`MEMDOCK_REPOSITORY`。

## API 与客户端

健康检查：`GET /healthz`；管理界面：`/`；MCP：`/mcp`。Secure 模式除健康检查外的所有请求需带 `X-MemDock-Write-Secret`；Private 模式读取免认证、API/MCP 写入需该密钥。

### MCP 客户端配置

把 MCP 客户端指向同一服务的 Streamable HTTP 端点 `/mcp`。客户端侧无需安装任何东西——MemDock 服务本身即 MCP server。Secure 模式须在所有请求中通过 `X-MemDock-Write-Secret` 发送共享密钥；Private 模式仅写操作需要该请求头。

Pi：

```sh
pi mcp add memdock --url http://127.0.0.1:8080/mcp
# Secure 模式，从环境变量读取 token：
# Secure 模式请在 MCP 配置 headers 中设置 X-MemDock-Write-Secret。
```

该命令写入用户级 `~/.pi/agent/mcp.json`（对所有项目生效）；加 `-l` 则写入项目级 `.pi/mcp.json`（仅受信项目）。工具以 `mcp__memdock__memory_search`、`mcp__memdock__memory_save` 等名称出现；用 `pi mcp list` 验证连接。

Private 模式下如需通过 MCP 直接写入，在 `~/.pi/agent/mcp.json` 中加写入密钥头：

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

Codex（`~/.codex/config.toml` 对所有项目生效，或项目根目录 `.codex/config.toml` 仅对受信项目生效）：

```toml
[mcp_servers.memdock]
url = "http://127.0.0.1:8080/mcp"
# Secure 模式所有请求必需；Private 模式写入需要
http_headers = { X-MemDock-Write-Secret = "<MEMDOCK_WRITE_SECRET>" }
```

## SQLite 备份与恢复

可从 `GET /api/v1/backup` 下载一致性快照。Secure 模式请求需携带共享密钥；端点使用 SQLite `VACUUM INTO` 创建独立快照并执行 `integrity_check` 后才返回。

恢复前先通过一致性备份 API 保存完整快照，再停服保留原数据库目录（含可能存在的 WAL/SHM sidecar）：

```sh
curl --fail --cacert trusted-ca.pem -H "X-MemDock-Write-Secret: $MEMDOCK_WRITE_SECRET" \
  https://memdock.example/api/v1/backup -o ./before-restore.sqlite
docker compose stop memdock
docker compose cp memdock:/data ./before-restore-data
# 将待恢复备份放入可由容器访问的路径，执行：
docker compose run --rm -v "$PWD:/restore:ro" memdock restore /restore/backup.sqlite /data/memdock.db
docker compose up -d memdock
```

`restore` 在目标目录的 0700 暂存目录中对来源数据库执行 `VACUUM INTO`，校验 SQLite 完整性、MemDock 表/列/索引/触发器及受支持的 schema 版本后再安装。它要求服务已停止；旧主库、`-wal`、`-shm` 先移至同目录保留文件，安装失败则回滚，成功后清理旧文件，因此不会把旧 WAL 应用到新主库。恢复后检查容器健康、项目/记忆数据和日志；备份文件须按敏感数据加密保存并定期进行实际恢复演练。Compose 命令是否能按具体 Docker Compose 版本访问命名卷应在部署环境先确认；也可在宿主机直接调用同一镜像并挂载数据卷。

## 验证

```sh
go test ./...
go vet ./...
go build ./cmd/server
git diff --check
```

AMD64/ARM64 镜像构建可用：

```sh
docker buildx build --platform linux/amd64,linux/arm64 -t memdock:local .
```

本仓库阶段四验证环境、未覆盖项和安全授权阻塞记录在 `docs/tasks/04-部署安全与交付.md` 与 `docs/待确认问题.md`。镜像构建、Tailscale 多设备、代理 TLS、备份恢复生产演练及用户验收未验证前，不视为部署验收通过。
