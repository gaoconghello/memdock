---
name: mem-save
description: Save a user-requested memory to MemDock through the applicable confirmation and approval flow.
---

# mem-save

仅当用户明确要求保存某项内容时使用。普通开发任务中禁止调用。Secure 模式下 `mem-save.sh` 只执行只读项目 lookup 并生成 `memdock-approval` 请求文件，不会直接写入；有交互终端时脚本要求输入 `y`，无交互终端时仅生成待独立审批的请求。通过可信渠道交给独立审批者执行 `memdock-server approve-request REQUEST.json`。项目须已由 Admin 建立并在 ACL 授权给当前 token；不要向 Agent 提供写 token 或审批私钥。完整 ACL 和审批边界见 README。

1. 提取用户指定的内容并拟定标题和正文；内容不明确时先询问，不要自行保存整个对话。
2. 可根据内容建议标签，调用 `scripts/mem-save.sh TITLE CONTENT [TAGS_CSV]`。Private 模式必须在交互终端输入 `y` 后写入；Secure 模式在交互终端要求 `y`，无交互终端时只生成待审批请求并打印交接命令。Secure 请求文件不代表已获批或已保存。
3. 默认保存至当前项目库；仅在明确指定时设置 `MEMDOCK_PROJECT_ID` 选择已有授权项目，或设置 `MEMDOCK_KNOWLEDGE_BASE_ID` 选择已授权知识库（共享库写入还需 ACL 授权）。
4. 报告服务返回结果。不要在日志或回复中输出访问凭据。
