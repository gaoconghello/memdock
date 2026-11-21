# MemDock

[English](README.md) | 简体中文

MemDock 是面向 Coding Agent 的共享长期记忆服务，提供 Go/SQLite API、MCP Streamable HTTP 和 Web 管理页面。本机 Pi/Codex Skill 的检索、Secure 提案生成及自动化 CLI 审批后检索已验证；真人逐次确认、身份隔离、移动设备和生产部署验收仍未完成。

## 功能特性

- **项目自动识别**：通过 Git 仓库远程地址自动识别并归组项目，记忆按项目隔离，跨设备、跨 Agent 共享；支持绑定/解绑仓库和项目合并。
- **长期记忆管理**：创建、更新（保留修改历史）、软删除、恢复和永久删除；每条记忆含标题、正文、分类、标签、来源和时间戳。
- **支持中文的全文检索**：SQLite FTS5 `trigram` 分词器加关键词回退，覆盖中文短语、中英文混合查询和技术缩写；BM25 相关性排序、分类/标签筛选，默认限定当前项目，默认仅返回前 5 条，避免污染 Agent 上下文。
- **项目库与共享库**：每个项目有默认知识库；共享库存放跨项目记忆，读/写权限单独授权。
- **写入由用户驱动**：服务端不自动总结会话——保存/更新/删除均由用户明确触发，Agent 只决定何时回忆（“用户决定记住什么，Agent 决定什么时候需要回忆”）。
- **三种接入方式**：REST API（`/api/v1`）、MCP Streamable HTTP（`/mcp`，工具：`memory_search`、`memory_save`、`memory_list`、`memory_update`、`memory_delete`、`project_info`）、Codex/Pi Agent Skill（`mem-save`、`mem-search`）。
- **Web 管理界面**：首页概览、项目管理（重命名、绑定/解绑仓库、合并、删除）、记忆管理（搜索、筛选、编辑、删除/恢复）、系统状态。
- **两种安全模式**：`private` 面向可信网络，写接口带 HMAC 完整性保护；`secure` 提供 token + 项目 ACL、只读 Agent token 和独立 Ed25519 审批 CLI。
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

Private 模式为默认模式，API/MCP 自动化契约保持不变；Web Console 的管理写入需管理员会话。首次启动前设置 `MEMDOCK_ADMIN_PASSWORD`（至少 6 位），服务将其安全哈希持久化到 SQLite；之后可在 Console 修改密码，修改会使所有旧会话失效。Private API/MCP 仍不提供读认证，必须限制在可信私有网络内。普通 HTTP 不加密密码、Cookie 会话或数据；应配置 MemDock 直连 HTTPS（`MEMDOCK_TLS_CERT`/`MEMDOCK_TLS_KEY`）或仅通过可信加密网络访问，不要在不可信网络使用 HTTP。Private 模式不支持由反向代理终止 TLS；代理 TLS 终止仅支持 Secure 模式。

## 使用示例

以 Private 模式为例走一遍完整流程：启动服务，在 Agent 会话中保存一条决策，之后在新会话中召回它。

**1. 启动服务**

```sh
: "${MEMDOCK_ADMIN_PASSWORD:?请先安全设置至少 6 位管理员密码}"
MEMDOCK_MODE=private MEMDOCK_APPROVAL_KEY=$(openssl rand -hex 32) \
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

Secure 模式步骤相同，区别在于写入脚本/MCP 工具只生成审批请求文件而不直接写入，需独立审批者执行 `memdock-server approve-request`（见下方 Secure 相关章节）。

## 配置

所有配置均通过环境变量完成：

| 变量 | 模式 | 说明 |
| --- | --- | --- |
| `MEMDOCK_ADDR` | 通用 | 监听地址，默认 `:8080`。 |
| `MEMDOCK_DB_PATH` | 通用 | SQLite 数据库路径，默认 `memdock.db`。 |
| `MEMDOCK_MODE` | 通用 | `private`（默认）或 `secure`。 |
| `MEMDOCK_ADMIN_PASSWORD` | private | 首次初始化 Private Console 管理员密码，至少 6 个字符；仅在数据库尚无管理员凭据时生效。妥善通过部署环境注入，勿提交或记录。密码哈希和管理员会话持久化于 SQLite。 |
| `MEMDOCK_APPROVAL_KEY` | private | 写接口 HMAC 密钥，至少 32 字节；用于请求完整性和防重放，不是用户授权。 |
| `MEMDOCK_WRITE_SECRET` | private | 可选，至少 32 字节。设置后客户端可用静态 `X-MemDock-Write-Secret` 请求头写入，替代逐请求 HMAC 签名——持有该值即拥有写入能力，请当作写入凭据保管。Secure 模式忽略此值。 |
| `MEMDOCK_READ_TOKEN` | secure | 必填，至少 32 字节，仅允许读操作。 |
| `MEMDOCK_WRITE_TOKEN` | secure | 必填，至少 32 字节且必须不同于读 token；仅审批操作者持有，可认证写请求。 |
| `MEMDOCK_APPROVAL_PUBLIC_KEY` | secure | 必填，Ed25519 32 字节公钥的十六进制字符串。服务不持有审批私钥。 |
| `MEMDOCK_PROJECT_ACL_FILE` | secure | 必填，项目访问控制 JSON 文件；凭据只以 SHA-256 摘要登记，未配置、未列入 ACL 或未授权项目均默认拒绝。 |
| `MEMDOCK_TLS_CERT`、`MEMDOCK_TLS_KEY` | 通用 | 成对配置时服务直接提供 HTTPS。 |
| `MEMDOCK_TRUST_PROXY_TLS` | secure | `true` 表示只信任代理 TLS 模式；必须同时配置 `MEMDOCK_TRUST_PROXY_CIDRS`（逗号分隔 CIDR）。服务校验连接源 IP 位于可信 CIDR 且 `X-Forwarded-Proto` 精确为 `https`。代理仍须清理客户端转发头并重写；不能让不可信来源直接访问后端端口。Private 模式不支持代理终止 TLS。 |

Private Web Console 使用随机会话 Cookie（HttpOnly、SameSite=Strict；HTTPS 时启用 Secure）和 CSRF token。管理员会话有效期为 12 小时；退出、到期或修改密码后失效。若数据库已有管理员密码，更改环境变量不会重置密码，需登录 Console 修改。首次密码尚未初始化时，Console 会提示设置环境变量并重启服务。管理员登录只授予 Private Web Console 会话写入，不改变 Secure 模式审批，也不替代既有 HMAC/静态写入密钥自动化接口。

### Secure 模式授权模型

Secure 启动时缺少读/写凭据、公钥、项目 ACL 或 TLS/可信代理配置会失败。`/healthz` 无需认证；其余 API/MCP/Web 以 Bearer token 或 Web Basic 密码匹配 ACL 中登记的 token SHA-256。

ACL `role` 为 `read`、`write` 或 `admin`；read/write 的项目权限仅来自 `projects` 明确列出的 ID（空列表无项目权限），共享库另用 `shared_read` / `shared_write` 授权。项目创建、状态中的全局数量和完整数据库备份仅 `admin` 可用；未授权项目返回拒绝，项目/知识库列表只返回已授权项。读 token 强制为只读，写 token 也必须在 ACL 中显式授予项目范围。修改 ACL 文件需重启服务。Private 模式仍是受信网络内的全服务访问，不提供项目身份 ACL。

### Secure ACL 示例

把 token 换成至少 32 字节的真实随机值，仅将 SHA-256 摘要保存到 `config/project-acl.json`，文件权限设为 `0600`：

```json
{
  "credentials": [
    {"token_sha256":"<READ_TOKEN_SHA256>","role":"read","projects":["prj_example"],"shared_read":true},
    {"token_sha256":"<WRITE_TOKEN_SHA256>","role":"write","projects":["prj_example"],"shared_read":true,"shared_write":true},
    {"token_sha256":"<ADMIN_TOKEN_SHA256>","role":"admin"}
  ]
}
```

生成摘要示例：`printf %s "$TOKEN" | sha256sum | cut -d ' ' -f1`。`MEMDOCK_READ_TOKEN` 与 `MEMDOCK_WRITE_TOKEN` 必须各自对应 ACL 中的 `read` / `write` 凭据；Admin 使用单独凭据，不能交给 Agent。新项目由 Admin 使用 `MEMDOCK_APPROVER_TOKEN`（审批 CLI 默认使用 `MEMDOCK_WRITE_TOKEN`）建立，并把项目 ID 加入需要授权的 token grant 后重启服务；共享库和整库备份权限单独配置。

### Secure 写入审批流程

Secure 写入由 Web、写入 Skill 脚本和 MCP 工具生成 `{"method":"POST","path":"/api/v1/...","body":{...}}` 审批请求文件，不直接修改数据。Agent 通过可信渠道把文件交给独立审批者；审批者在无法被 Agent 控制的 OS 账户/终端运行 `memdock-server approve-request REQUEST.json`。CLI 显示完整请求正文并要求 TTY 输入 `approve`，随后用 Ed25519 私钥及审批者凭据直接提交 HTTPS 请求。普通项目写入使用 ACL 授权的 `MEMDOCK_WRITE_TOKEN`；Admin 项目初始化使用 `MEMDOCK_APPROVER_TOKEN`。旧式 `approve METHOD /api/v1/path BODY.json` 仍可用。私钥权限为 `0600`，服务端仅持有公钥。**如果 Agent 与审批者同 UID、能访问审批私钥/写 token、控制审批终端或 Docker daemon，隔离边界即失效。**

Secure Web/Skill/MCP 审批文件链路提供可执行请求生成及独立 CLI 审批；真实不同 OS 身份/终端交接仍需部署验收。Private 模式仍沿用 HMAC（仅完整性/防重放，不提供严格审批）。

## Docker / Compose

仓库提供 [.env.example](.env.example) 和 [ACL 示例](config/project-acl.example.json)。Compose 会自动读取根目录的 `.env` 并将 `MEMDOCK_ADMIN_PASSWORD` 传入 Private 服务；复制示例后，在首次启动前设置强管理员密码，切勿提交凭据。密码以哈希形式持久化到 SQLite，因此初始化后可从环境中移除该值而不影响登录。Private 模式不需要 ACL 文件。

```sh
# Private 模式示例：生成仅本机可读的 HMAC 配置；妥善保管并勿提交。
umask 077
cp .env.example .env
# 编辑 .env：设置强管理员密码（至少 6 个字符），
# 并将 MEMDOCK_APPROVAL_KEY 设为以下命令生成的值：
openssl rand -hex 32
docker compose up -d --build
docker compose ps
docker compose logs -f memdock
```

Secure 模式还需复制 `config/project-acl.example.json` 为 `config/project-acl.json`，替换其中的摘要和项目 ID，并按下方步骤完成 token、公钥及 TLS 配置。示例中的占位符不能直接用于启动。直接使用 `docker run --env-file .env` 时，文件中的服务端环境变量会传入容器；Compose 专用的端口及挂载目录变量仍需通过 `-p`、`-v` 等参数单独配置。

默认仅发布到宿主机 `127.0.0.1:8080`，容器以 UID/GID 10001 非 root 运行，SQLite 位于持久卷 `memdock-data`。Private 模式应配置 MemDock 直连 TLS，或仅通过 Tailnet 等可信加密网络访问其 HTTP 监听端口；不要对 Private Console 使用 `tailscale serve` 或其他 TLS 终止代理，因为 Private 模式只在 TLS 直达服务端时接受 HTTPS Origin 并设置 Secure Cookie。Secure 模式支持下文所述的可信 TLS 终止代理。不要将默认 HTTP 端口直接发布到公网。

### Secure 模式 TLS 配置

Secure 可由可信反向代理终止 TLS：确保网络层仅代理可访问后端，设置 `MEMDOCK_MODE=secure`、读/写 token、公钥、项目 ACL 文件、`MEMDOCK_TRUST_PROXY_TLS=true` 及代理实际源地址对应的 `MEMDOCK_TRUST_PROXY_CIDRS`。任意来源即使伪造 `X-Forwarded-Proto: https` 也会因源 IP 不可信而拒绝；可信代理须覆盖客户端提交的转发头。也可设置证书/key 让服务直接 TLS；Compose 将 `${MEMDOCK_TLS_DIR:-./certs}` 只读挂载到 `/certs`，`.env` 设置 `MEMDOCK_TLS_CERT=/certs/fullchain.pem` 与 `MEMDOCK_TLS_KEY=/certs/privkey.pem`。证书和私钥须允许容器 UID 10001 读取且不能被 Agent 修改。

### Secure 身份配置（由部署操作者完成）

1. 以**不同于 Agent 的操作系统账户**运行 `memdock-server keygen /secure/path/approval.key`；保存其输出的 Ed25519 公钥到服务端 `MEMDOCK_APPROVAL_PUBLIC_KEY`。私钥仅留在审批者账户，工具创建时设为 `0600`。
2. 分别生成至少 32 字节随机的 `MEMDOCK_READ_TOKEN`、`MEMDOCK_WRITE_TOKEN`，二者必须不同；另生成独立 Admin token。为每个 token 计算 SHA-256，并在 `config/project-acl.json` 中授予明确 role、project ID 与 shared 权限。ACL 文件设为 `0600`；Secure Compose 只读挂载 `./config` 并读取 `/config/project-acl.json`。`.env` 设置 `MEMDOCK_MODE=secure`、读/写 token、公钥及 TLS 配置，或设置可信代理 CIDR。Secure 不使用 Private 模式的 HMAC `MEMDOCK_APPROVAL_KEY`。Agent 只持有其 `read` token；Admin token 不交 Agent。不得允许 Agent 访问 Docker daemon 或含凭据的部署文件。
3. Web、Secure 写入 Skill 脚本和 MCP 写工具生成审批请求 JSON，不直接写入。Agent 通过可信渠道将文件交给审批者；审批者在自己的交互终端执行：

```sh
MEMDOCK_URL=https://memdock.example \
MEMDOCK_WRITE_TOKEN='审批者专用写 token' \
MEMDOCK_APPROVAL_PRIVATE_KEY_FILE=/secure/path/approval.key \
./memdock-server approve-request request.json
```

CLI 显示将要提交的完整 JSON（终端控制/双向文本已转义），必须在终端输入 `approve` 才会发送一次性请求。签名绑定 HTTP 方法、路径和正文，约一分钟有效且服务端拒绝重放；REST 最终写入还会校验审批 token 对目标项目/知识库的 ACL。新项目由 Admin 建立，并将项目加入授权 grant 后重启服务。执行 Admin 操作时设置 `MEMDOCK_APPROVER_TOKEN='Admin token'`（会覆盖写 token）。不要把私钥、任何审批 token 或审批者终端交给 Agent。

Secure Web 会下载审批请求文件；Secure `mem-save` 在用户明确触发后生成 `0600` 请求文件，有 TTY 时还会要求输入 `y`，无 TTY 时仅生成待独立审批的请求；mem-update/delete/kb-create/project-rename 脚本在交互确认后写出请求文件；MCP 写工具返回结构化 `approval_request`。请求文件须经可信渠道交给独立审批者。文件生成本身不代表获批或保存成功。

```json
{"project_id":"项目ID","title":"用户确认的标题","content":"用户确认的内容"}
```

如需新建项目/绑定仓库，由 Admin 准备 `{"method":"POST","path":"/api/v1/projects/resolve","body":{"name":"项目名","repository":"https://example/repo.git"}}`，设置 `MEMDOCK_APPROVER_TOKEN` 后执行 `approve-request project.json`；随后把新项目 ID 加到所需身份 grant 并重启服务。Agent 在 ACL 更新前不能访问新项目。

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

启动 Agent 时带上该变量——例如 `MEMDOCK_URL=http://192.168.1.10:8080 pi --skill /path/to/memdock/skills/mem-search/SKILL.md`——或在 shell 配置文件（如 `~/.bashrc`）中 `export`，让 Codex/Pi 的子 shell 继承。Private 模式的写入脚本用 HMAC 密钥签名：设置 `MEMDOCK_APPROVAL_KEY`（与服务器同值）或用 `MEMDOCK_APPROVAL_KEY_FILE` 指向密钥文件；脚本写入前仍会要求交互输 `y` 确认。Secure 模式则设置 `MEMDOCK_READ_TOKEN`（Agent 的只读 token）。可选变量：`MEMDOCK_PROJECT_ID`、`MEMDOCK_KNOWLEDGE_BASE_ID` 指定已授权的项目/知识库，仓库有多个 Git remote 时用 `MEMDOCK_REPOSITORY` 指明。

## API 与客户端

健康检查：`GET /healthz`；管理界面：`/`；MCP：`/mcp`。Secure 模式写操作只可由独立审批 CLI 发起；Agent 常规客户端只持有读 token。Agent 读脚本可用 `MEMDOCK_READ_TOKEN` 环境变量；MCP 客户端需为 `/mcp` 请求配置相同的 Bearer token。Private 模式的脚本仍使用 HMAC，但不能满足严格授权。运行时的身份分离依赖部署账户、文件权限、终端及 Docker daemon 权限，需分别验收。

### MCP 客户端配置

把 MCP 客户端指向同一服务的 Streamable HTTP 端点 `/mcp`。客户端侧无需安装任何东西——MemDock 服务本身即 MCP server。Private 模式无需 token；Secure 模式以 Bearer 方式携带 Agent 的读 token。Private 模式下也可让客户端直接写入：在服务端设置 `MEMDOCK_WRITE_SECRET`，客户端以 `X-MemDock-Write-Secret` 请求头携带即可。

Pi：

```sh
pi mcp add memdock --url http://127.0.0.1:8080/mcp
# Secure 模式，从环境变量读取 token：
pi mcp add memdock --url https://memdock.example/mcp --bearer-token-env-var MEMDOCK_READ_TOKEN
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
# Private 模式免逐请求 HMAC 直接写入：
http_headers = { X-MemDock-Write-Secret = "<MEMDOCK_WRITE_SECRET>" }
# 仅 Secure 模式需要：
# http_headers = { Authorization = "Bearer <READ_TOKEN>" }
```

## SQLite 备份与恢复

Admin 身份可下载一致性快照：`GET /api/v1/backup`。使用 HTTPS 和 Admin Bearer token（或浏览器 Basic）保存响应文件；端点使用 SQLite `VACUUM INTO` 创建独立快照并执行 `integrity_check` 后才返回。

恢复前先通过一致性备份 API 保存完整快照，再停服保留原数据库目录（含可能存在的 WAL/SHM sidecar）：

```sh
curl --fail --cacert trusted-ca.pem -H "Authorization: Bearer $MEMDOCK_READ_TOKEN" \
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
