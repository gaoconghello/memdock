---
name: mem-save
description: Save a user-requested memory to MemDock after explicit confirmation.
---

# mem-save

仅当用户明确要求保存某项内容时使用。普通开发任务中禁止调用。脚本会展示写入内容并要求交互终端输入 `y` 后执行。Secure 模式需在 `MEMDOCK_WRITE_SECRET` 中配置共享密钥；该密钥可读写所有项目，不存在独立审批或项目 ACL。

1. 提取用户指定的内容并拟定标题和正文；内容不明确时先询问，不要自行保存整个对话。
2. 可根据内容建议标签，调用 `scripts/mem-save.sh TITLE CONTENT [TAGS_CSV]`。需在交互终端输入 `y` 后写入。
3. “保存到 MemDock”表示使用 MemDock 服务，默认保存为当前项目记忆。只有用户明确指定目标项目，例如“保存到 memdock 项目”，才按显式项目选择处理。仅在用户明确指定共享库时设置 `MEMDOCK_KNOWLEDGE_BASE_ID`。仅在明确指定时设置 `MEMDOCK_PROJECT_ID` 选择项目。
4. 报告服务返回结果。不要在日志或回复中输出访问凭据。

## 确定目标项目

“保存到 MemDock”的含义与目录是否有 Git 无关。先确定用户的实际项目目录，再按以下顺序识别目标：

1. 用户明确指定目标项目时，核实项目 ID。仅为该次脚本调用设置 `MEMDOCK_PROJECT_ID`。
2. 默认先查本地目录绑定。有 Git 时使用仓库根目录，无 Git 时使用当前目录的物理路径。绑定文件位于 `${XDG_CONFIG_HOME:-$HOME/.config}/memdock/projects/`，文件名为目录路径的 SHA-256 前 32 个十六进制字符，内容为项目 ID。
3. 无绑定且有 Git Remote 时，按 Remote 查找或创建项目。
4. 无绑定且没有 Git Remote 时，以当前项目目录的文件夹名作为新项目的默认名称。只检查该名称的同名候选；有同名候选时，由用户选择绑定已有项目或新建。用户确认前不得写入。

优先从实际项目目录运行保存脚本，由脚本展示目标并等待确认。直接调用 MCP 时也必须在客户端完成上述识别；如果无法确定项目目录或读取绑定所需的本地信息，先请用户明确目标，不能从 `project_list` 自行选中 `memdock`。需要新建时，在用户确认后调用 `project_resolve`，用返回的 `project_id` 调用 `memory_save`。无 Remote 时，用户选择或创建项目后，将 ID 保存到上述目录绑定文件，供后续保存使用。

Secure 模式下，脚本需要先有可查到的项目。首次新建可在用户确认后通过 `project_resolve` 创建，再按上述方式建立本地绑定。

## 保存到共享库

用户给出共享库名称（例如 `shared`）时，调用 MCP 的 `knowledge_base_list`，或用脚本所配置的 MemDock 服务请求 `GET /api/v1/knowledge-bases`。按 `name` 精确匹配并取返回的 `id`。有多个同名库时请用户选择，找不到时先报告并请用户确认目标。

用户要求同时保存为项目记忆和共享记忆时，先识别项目并查到共享库 ID，再分别写入：

- 项目记忆：脚本取消设置 `MEMDOCK_KNOWLEDGE_BASE_ID`，MCP 调用省略 `knowledge_base_id`。
- 共享记忆：脚本仅为该次调用设置 `MEMDOCK_KNOWLEDGE_BASE_ID`，MCP 调用传入查到的 `knowledge_base_id`。两次 MCP 调用均传入已核实的 `project_id`。

沿用各入口的写入确认流程，分别报告两次保存的结果。若只有一次成功，指出已保存的目标以及失败的目标。
