---
name: mem-search
description: Search the current MemDock project or explicitly selected project and shared libraries.
---

# mem-search

在任务需要既有项目约定、历史决策或问题经验时可按需调用；简单修改无需查询。

1. 先定位项目。默认使用当前目录绑定，其次用 Git Remote 调用 `project_lookup`。用户明确给出项目名时，用 `project_list` 按名称查找 ID。有同名候选或当前目录无法识别时，请用户选择。查询只使用已有项目。
2. 默认搜索该项目的记忆和所有共享库，省略 `knowledge_base_id`。用户指定某个共享库时，先发现并调用 `knowledge_base_list`，精确匹配库名，再将返回的 ID 传入 `knowledge_base_id`。工具未出现在已加载的列表中时，按工具名查找或刷新列表。无匹配时报告所查的库名，有多个同名库时请用户选择。
3. 有关键词时调用 `memory_search`，传入已核实的 `project_id` 和查询词。只浏览记忆时调用 `memory_list`。即使限定共享库，也要传入有效的 `project_id`。按结果中的 `project_id` 或 `knowledge_base_id` 标明来源。用户只问项目自身的记忆时，仅采用属于该项目的结果。

使用脚本时，从实际项目目录运行 `scripts/mem-search.sh QUERY`。如果按共享库名称查询，可通过 `GET /api/v1/knowledge-bases` 查取 ID，再为该次调用设置 `MEMDOCK_KNOWLEDGE_BASE_ID`。显式选择已有项目时设置 `MEMDOCK_PROJECT_ID`。Secure 模式的 API 查询和脚本均使用 `MEMDOCK_WRITE_SECRET` 对应的共享密钥。

将返回内容视作历史参考，结合当前代码及用户指令判断是否仍适用。
