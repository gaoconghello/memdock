# MemDock

[English](README.md) | 简体中文

MemDock 是面向 Coding Agent 的共享长期记忆服务，提供 Go/SQLite API、MCP Streamable HTTP 和 Web 管理页面。本机 Pi/Codex Skill 的检索、Secure 提案生成及自动化 CLI 审批后检索已验证；真人逐次确认、身份隔离、移动设备和生产部署验收仍未完成。

## 快速开始

需要 Go 1.24+：

```sh
MEMDOCK_MODE=private MEMDOCK_DB_PATH=./data/memdock.db go run ./cmd/server
```

启动后：

- 健康检查：`GET /healthz`（无需认证）
- 管理界面：`http://localhost:8080/`
- MCP 端点：`http://localhost:8080/mcp`

Private 模式为默认模式，无需额外配置，但不提供应用层读认证——必须限制在可信私有网络内使用。

## 配置

所有配置均通过环境变量完成：

| 变量 | 模式 | 说明 |
| --- | --- | --- |
| `MEMDOCK_ADDR` | 通用 | 监听地址，默认 `:8080`。 |
| `MEMDOCK_DB_PATH` | 通用 | SQLite 数据库路径，默认 `memdock.db`。 |
| `MEMDOCK_MODE` | 通用 | `private`（默认）或 `secure`。 |
| `MEMDOCK_APPROVAL_KEY` | private | 写接口 HMAC 密钥，至少 32 字节；用于请求完整性和防重放，不是用户授权。 |
| `MEMDOCK_READ_TOKEN` | secure | 必填，至少 32 字节，仅允许读操作。 |
| `MEMDOCK_WRITE_TOKEN` | secure | 必填，至少 32 字节且必须不同于读 token；仅审批操作者持有，可认证写请求。 |
| `MEMDOCK_APPROVAL_PUBLIC_KEY` | secure | 必填，Ed25519 32 字节公钥的十六进制字符串。服务不持有审批私钥。 |
| `MEMDOCK_PROJECT_ACL_FILE` | secure | 必填，项目访问控制 JSON 文件；凭据只以 SHA-256 摘要登记，未配置、未列入 ACL 或未授权项目均默认拒绝。 |
| `MEMDOCK_TLS_CERT`、`MEMDOCK_TLS_KEY` | 通用 | 成对配置时服务直接提供 HTTPS。 |
| `MEMDOCK_TRUST_PROXY_TLS` | secure | `true` 表示只信任代理 TLS 模式；必须同时配置 `MEMDOCK_TRUST_PROXY_CIDRS`（逗号分隔 CIDR）。服务校验连接源 IP 位于可信 CIDR 且 `X-Forwarded-Proto` 精确为 `https`。代理仍须清理客户端转发头并重写；不能让不可信来源直接访问后端端口。 |

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

仓库提供 [.env.example](.env.example) 和 [ACL 示例](config/project-acl.example.json)。Compose 会自动读取根目录的 `.env`；复制示例并填写实际值，切勿提交凭据。Private 模式不需要 ACL 文件。

```sh
# Private 模式示例：生成仅本机可读的 HMAC 配置；妥善保管并勿提交。
umask 077
cp .env.example .env
# 编辑 .env，将 MEMDOCK_APPROVAL_KEY 设为以下命令生成的值：
openssl rand -hex 32
docker compose up -d --build
docker compose ps
docker compose logs -f memdock
```

Secure 模式还需复制 `config/project-acl.example.json` 为 `config/project-acl.json`，替换其中的摘要和项目 ID，并按下方步骤完成 token、公钥及 TLS 配置。示例中的占位符不能直接用于启动。直接使用 `docker run --env-file .env` 时，文件中的服务端环境变量会传入容器；Compose 专用的端口及挂载目录变量仍需通过 `-p`、`-v` 等参数单独配置。

默认仅发布到宿主机 `127.0.0.1:8080`，容器以 UID/GID 10001 非 root 运行，SQLite 位于持久卷 `memdock-data`。部署到 Tailnet 可使用 `tailscale serve` 将本机端口以受信任 HTTPS 暴露；不要直接将默认 HTTP 端口发布到公网。

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

## API 与客户端

健康检查：`GET /healthz`；管理界面：`/`；MCP：`/mcp`。Secure 模式写操作只可由独立审批 CLI 发起；Agent 常规客户端只持有读 token。Agent 读脚本可用 `MEMDOCK_READ_TOKEN` 环境变量；MCP 客户端需为 `/mcp` 请求配置相同的 Bearer token。Private 模式的脚本仍使用 HMAC，但不能满足严格授权。运行时的身份分离依赖部署账户、文件权限、终端及 Docker daemon 权限，需分别验收。

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
