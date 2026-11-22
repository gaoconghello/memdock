---
name: mem-project-binding
description: 用户明确要求替换或解除 MemDock 项目的 Git 仓库绑定时使用，不删除项目或记忆
---

1. 用 `project_lookup` 或 `project_list` 核对用户指定的项目 ID，再用 `project_info` 查看 `repositories`。用列表中的原值（包括 `d/Project/pulse` 之类的旧绑定）定位旧绑定，不从项目名称猜测；如果项目或绑定不明确，先让用户选择。
2. 替换时调用 `project_repository_update`，传入 `project_id`、旧绑定 `repository` 和新 Git 远程地址 `new_repository`（HTTPS、SSH 或 SCP 格式，不含凭据）。用一次更新，不要先解绑再新增。若新地址已经绑定到同一项目，而用户明确要求保留新地址并移除旧绑定，则只删除旧绑定。若新地址属于其他项目，不自动迁移或解绑。
3. 解除绑定时调用 `project_repository_delete`，只传 `project_id` 和要移除的 `repository`。如果 MCP 不可用，可用 REST：对 `/api/v1/projects/{id}/repositories` 发 `PATCH`（替换）或 `DELETE`（解除），请求体字段与 MCP 相同但不含 `project_id`。写请求使用 `X-MemDock-Write-Secret`，不要回显密钥。
4. 成功后再次查询 `project_info`（REST：`GET /api/v1/projects/{id}`）核对结果。解绑只影响仓库自动识别，不删除项目、知识库或记忆。
