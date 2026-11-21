---
name: mem-save
description: Save a user-requested memory to MemDock after explicit confirmation.
---

# mem-save

仅当用户明确要求保存某项内容时使用。普通开发任务中禁止调用。脚本会展示写入内容并要求交互终端输入 `y` 后执行。Secure 模式需在 `MEMDOCK_WRITE_SECRET` 中配置共享密钥；该密钥可读写所有项目，不存在独立审批或项目 ACL。

1. 提取用户指定的内容并拟定标题和正文；内容不明确时先询问，不要自行保存整个对话。
2. 可根据内容建议标签，调用 `scripts/mem-save.sh TITLE CONTENT [TAGS_CSV]`。需在交互终端输入 `y` 后写入。
3. 默认保存至当前项目库；仅在明确指定时设置 `MEMDOCK_PROJECT_ID` 选择项目，或设置 `MEMDOCK_KNOWLEDGE_BASE_ID` 选择知识库。
4. 报告服务返回结果。不要在日志或回复中输出访问凭据。

调用前确认脚本在用户的实际项目目录运行。不要从项目列表、名称相似或之前对话猜测项目 ID；无可靠目录绑定时应让用户选择或创建项目。`MEMDOCK_PROJECT_ID` 仅可在用户明确选择该项目时为本次调用设置，不要沿用来源不明的环境变量。脚本显示的目录、项目名称和 ID 必须与用户意图一致；Secure 无交互终端时，在生成审批请求前向用户确认目标。直接使用 MCP 时同样先在客户端核实目录绑定；未绑定时先由用户选择项目，不能自行挑选。
