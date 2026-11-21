---
name: mem-update
description: 用户明确要求修改某条既有记忆时使用
---

仅响应用户明确提出的记忆更新请求。先通过记忆 ID 唯一定位目标；若搜索到多个候选，先让用户选择，不可自行挑选。整理新标题和正文后，使用已明确选定的 `MEMDOCK_PROJECT_ID` 运行 `scripts/mem-update.sh MEMORY_ID TITLE CONTENT [TAGS_CSV]`；只有用户明确要求修改标签时才传入标签参数。Secure 模式脚本经用户确认后生成待审批 JSON，不直接修改；通过可信渠道交给独立审批者运行 `memdock-server approve-request REQUEST.json`。只有审批 CLI 成功返回后才能报告更新成功。
