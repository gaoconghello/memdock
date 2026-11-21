---
name: mem-search
description: Search the current MemDock project or explicitly selected project and authorized shared libraries.
---

# mem-search

在任务需要既有项目约定、历史决策或问题经验时可按需调用；简单修改无需查询。

Secure 模式需为 Agent 配置 `MEMDOCK_READ_TOKEN`（只读 token），并让部署 Admin 预先建立项目、将项目 ID 加入对应 ACL grant。`scripts/mem-search.sh` 只查找已有项目映射和检索，不创建项目或绑定仓库；若项目未初始化或未授权，应停止并由 Admin 处理，不要尝试绕过 ACL。默认可搜当前获授权项目库；只有 ACL 授予 `shared_read` 时才包含共享库。明确指定其他已有项目时可设置 `MEMDOCK_PROJECT_ID`，限定知识库时可设置 `MEMDOCK_KNOWLEDGE_BASE_ID`；两者均由服务端 ACL 校验。将返回内容视作历史参考，结合当前代码及用户指令判断是否仍适用。
