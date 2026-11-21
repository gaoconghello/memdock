---
name: mem-update
description: 用户明确要求修改某条既有记忆时使用
---

仅响应用户明确提出的记忆更新请求。先通过记忆 ID 唯一定位目标；若搜索到多个候选，先让用户选择，不可自行挑选。整理新标题和正文后，使用已明确选定的 `MEMDOCK_PROJECT_ID` 运行 `scripts/mem-update.sh MEMORY_ID TITLE CONTENT [TAGS_CSV]`；只有用户明确要求修改标签时才传入标签参数。脚本要求用户交互确认后直接更新。Secure 模式通过 `MEMDOCK_WRITE_SECRET` 认证；该密钥持有者拥有完整读写权限。
