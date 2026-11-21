---
name: mem-delete
description: 用户明确要求删除某条既有记忆时使用
---

仅响应用户明确提出的记忆删除请求。永久删除另有确认要求，不通过此 Skill 执行。先通过记忆 ID 唯一定位目标；若目标不明确，先列出候选并让用户选择。使用已明确选定的 `MEMDOCK_PROJECT_ID` 运行 `scripts/mem-delete.sh MEMORY_ID` 执行软删除；脚本要求交互确认。Secure 模式下脚本使用 `MEMDOCK_WRITE_SECRET` 直接写入；该密钥持有者拥有完整读写权限。不得直接调用永久删除接口。
