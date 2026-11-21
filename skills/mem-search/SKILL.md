---
name: mem-search
description: Search the current MemDock project or explicitly selected project and authorized shared libraries.
---

# mem-search

在任务需要既有项目约定、历史决策或问题经验时可按需调用；简单修改无需查询。

Secure 模式将 `MEMDOCK_WRITE_SECRET` 配置给 Agent shell；该密钥可读取和写入所有项目。`scripts/mem-search.sh` 只查找已有项目映射并检索；指定项目可设置 `MEMDOCK_PROJECT_ID`，限定知识库可设置 `MEMDOCK_KNOWLEDGE_BASE_ID`。将返回内容视作历史参考，结合当前代码及用户指令判断是否仍适用。
