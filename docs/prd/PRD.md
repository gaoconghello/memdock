# MemDock V1.0 项目建设方案

**项目名称：** MemDock\
**项目标识：** `memdock`\
**英文定位：** Lightweight Shared Memory for Coding Agents\
**中文定位：** 轻量级 Coding Agent 共享长期记忆服务\
**版本：** V1.0\
**部署模式：** Docker / Tailscale / 可扩展公网 HTTPS

---

# 一、项目概述

## 1.1 项目背景

Codex、Pi 等 Coding Agent 已经能够承担大量软件开发工作，但在长期项目开发中，仍然存在跨会话、跨设备和跨 Agent 的记忆共享问题。

主要表现为：

1. 新会话可能无法获取之前讨论并确定的技术决策。
2. 不同 Coding Agent 之间缺乏统一的长期记忆。
3. 不同计算机开发同一 Git 仓库时，记忆无法自然共享。
4. 用户需要重复解释项目架构、开发规范和历史问题。
5. 将所有历史信息写入 `AGENTS.md` 等文件，会增加上下文消耗。
6. 自动记忆系统容易保存无价值信息，导致记忆污染。

因此，需要建设一个独立运行的长期记忆服务，为不同 Coding Agent 提供统一、持久化、按项目隔离的记忆存储和检索能力。

## 1.2 项目定位

MemDock 是一个面向 Coding Agent 的轻量级共享记忆服务。

它不替代 Codex、Pi 等 Agent，也不负责执行软件开发任务。

主要职责包括：

- 持久化保存用户指定的重要项目记忆。
- 根据 Git 仓库自动识别和归类项目。
- 允许 Agent 在开发过程中按需查询历史记忆。
- 在多台设备、多个 Agent 之间共享项目记忆。
- 提供 Skill、MCP 和 HTTP API 接入方式。
- 提供简单的 Web 管理界面。
- 通过 Docker 进行统一部署。

MemDock 的核心原则：

**用户决定记住什么，Agent 决定什么时候需要回忆。**

## 1.3 V1.0 设计原则

| 原则       | 具体要求                  |
| -------- | --------------------- |
| 轻量化      | Go 单体服务，SQLite 数据库    |
| 高性能      | 本地索引、项目隔离、低延迟检索       |
| 主动写入     | 只有用户明确触发才能保存、更新或删除记忆  |
| 智能读取     | Agent 可以根据开发任务自动检索    |
| 跨 Agent  | Codex、Pi 等共享同一套记忆     |
| 跨设备      | 同一 Git 仓库自动归入同一项目     |
| Skill 优先 | 不强制安装额外 MCP 扩展        |
| MCP 兼容   | 提供标准 MCP 工具接口         |
| 容器化      | Docker 部署，数据持久化       |
| 网络安全     | 默认私有网络，公网模式强制认证和加密    |
| 可扩展      | 后续可增加 Embedding 和向量检索 |

---

# 二、总体系统架构

## 2.1 架构组成

系统由四个主要部分构成。

**第一部分：Coding Agent 客户端**

包括：

- Codex CLI
- Pi Agent
- 其他支持 Skill、MCP 或 HTTP API 的 Agent

客户端通过 Skill 或 MCP 与 MemDock 通信。

**第二部分：Skill 接入层**

主要完成：

- 根据当前目录识别 Git 项目。
- 根据用户命令保存、修改或删除记忆。
- 根据开发任务自动查询相关记忆。
- 将服务器返回的记忆交给 Agent 使用。

**第三部分：MemDock 服务端**

由 Go 实现，包括：

- HTTP REST API
- MCP Server
- 项目管理
- 记忆管理
- 全文检索
- 认证与权限控制
- Web 管理界面

**第四部分：持久化存储**

使用 SQLite：

- 项目信息
- 仓库映射
- 长期记忆
- 记忆分类
- 全文检索索引
- 创建及更新时间

V1.0 不依赖外部数据库服务。

## 2.2 系统工作方式

```
Windows / macOS / Linux
         │
   ┌─────┴─────┐
   │           │
 Codex        Pi
   │           │
   └─────┬─────┘
         │
   Skill / MCP
         │
   Git 项目识别
         │
     Tailscale
         │
 ┌─────────────────┐
 │  Docker         │
 │                 │
 │  MemDock Server │
 │  ├ REST API     │
 │  ├ MCP Server   │
 │  ├ 项目管理      │
 │  ├ 记忆管理      │
 │  ├ FTS5 搜索    │
 │  └ Web Console  │
 │                 │
 └────────┬────────┘
          │
    Persistent Data
          │
      SQLite
```

多个 Agent 访问同一个服务器，以项目为单位共享记忆。

---

# 三、项目自动识别与分组

## 3.1 项目识别目标

MemDock 应当自动识别当前 Agent 正在开发的项目。

用户不需要每次手动指定项目名称。

例如：

Windows：

`D:\projects\viral-flow`

macOS：

`/Users/dev/projects/viral-flow`

Linux：

`/home/dev/viral-flow`

只要对应同一个 Git 仓库，就应自动识别为同一个 MemDock 项目。

## 3.2 Git Remote 识别

优先通过 Git Remote 获取仓库地址。

例如：

```
git remote get-url origin
```

下列地址应归一化为同一个仓库标识：

```
git@github.com:company/viral-flow.git
https://github.com/company/viral-flow.git
ssh://git@github.com/company/viral-flow.git
```

统一标识：

```
github.com/company/viral-flow
```

Gitee 使用相同规则：

```
gitee.com/company/viral-flow
```

仓库地址规范化需要考虑：

- SSH / HTTPS 格式差异
- `.git` 后缀
- URL 末尾斜杠
- 主机名大小写
- SSH 自定义端口
- 仓库命名大小写规则
- Remote 地址中的认证信息

不得将 Git URL 中的密码或访问令牌上传到 MemDock。

## 3.3 项目身份机制

MemDock 内部为每个项目生成固定的 `project_id`。

项目名称仅用于展示，仓库地址用于识别。

例如：

```
{
  "project_id": "proj_001",
  "name": "viral-flow",
  "repositories": [
    "github.com/company/viral-flow",
    "gitee.com/company/viral-flow"
  ]
}
```

一个项目可以绑定多个仓库地址。

当 GitHub 仓库迁移至 Gitee，或者仓库改名时，可以通过 Web Console 绑定新地址。

不同仓库即使名称相同，也不能仅凭名称自动合并。

## 3.4 识别优先级

1. 当前目录已明确绑定的 MemDock 项目。
2. Git Remote 与已有项目的仓库映射。
3. 根据 Git Remote 自动创建新项目。
4. 没有 Git Remote 时，使用独立临时项目标识，等待用户绑定。

对于包含多个 Remote 的仓库：

- 优先使用已经建立的项目绑定。
- 其次使用 `origin`。
- 存在歧义时提示用户选择。

在 Git 子目录或 Worktree 中工作时，应识别实际所属仓库。

## 3.5 自动分组

Web Console 以项目为单位展示记忆。

```
MemDock
│
├── viral-flow
│   ├── 架构设计
│   ├── 开发规范
│   ├── 技术决策
│   └── 问题经验
│
├── aira-ai
│   ├── 架构设计
│   ├── 开发规范
│   └── 技术决策
│
└── lead-hub
    ├── 架构设计
    └── 问题经验
```

以上为结构示例。

Agent 默认只能在当前项目范围内检索记忆，避免不同项目的信息混淆。

---

# 四、长期记忆管理

## 4.1 记忆分类

V1.0 提供以下默认分类。

| 分类    | 内容             |
| ----- | -------------- |
| 架构设计  | 系统架构、模块划分、技术选型 |
| 技术决策  | 确定的技术方案及决策原因   |
| 开发规范  | 命名规则、代码规范、开发约定 |
| 问题经验  | 历史问题、原因、解决方案   |
| 项目上下文 | 长期有效的业务规则、重要背景 |
| 其他    | 不属于上述分类的重要记忆   |

分类用于方便管理，不强制用户每次手动选择。

保存时 Agent 可以推荐分类。

## 4.2 记忆内容结构

每条记忆至少包含：

| 字段         | 说明             |
| ---------- | -------------- |
| ID         | 唯一记忆标识         |
| Project ID | 所属项目           |
| Title      | 记忆标题           |
| Content    | 记忆正文           |
| Category   | 分类             |
| Tags       | 可选标签           |
| Source     | Codex、Pi 或 Web |
| Created At | 创建时间           |
| Updated At | 更新时间           |
| Status     | 有效、已废弃等状态      |

可选保存来源会话标识，但不存储完整聊天记录。

不需要在第一版增加复杂的知识图谱或多级关系模型。

## 4.3 保存机制

记忆写入必须由用户主动触发。

例如：

```
/mem-save 记住刚才确定的登录方案
```

Agent 执行：

1. 确认本次用户请求。
2. 从当前对话中提取指定内容。
3. 自动识别 Git 项目。
4. 整理记忆标题、正文及分类。
5. 调用 MemDock 写入接口。
6. 返回保存结果。

如果用户没有明确说明需要保存的内容，可以由 Agent 生成预览并请求确认。

MemDock 服务端不负责分析或总结聊天会话。

允许 Agent 在用户主动触发保存命令后，对用户指定的内容进行整理。

## 4.4 记忆更新

用户可以修改已有记忆。

例如：

```
/mem-update 将以前确定的 Redis 缓存方案改为本地缓存
```

处理要求：

- 先定位相关记忆。
- 对存在歧义的目标要求选择。
- 修改指定记录。
- 更新修改时间。
- 保留必要的修改历史。

当新的决策替代旧决策时，可以将旧记忆标记为已废弃。

默认检索只返回有效记忆。

## 4.5 记忆删除

用户可以指定删除记忆。

例如：

```
/mem-delete 删除以前关于旧登录方案的记忆
```

建议采用软删除。

被删除的记忆默认不参与检索，但可以在 Web Console 中恢复或永久删除。

删除和批量修改应具备明确的用户授权。

---

# 五、Skill 设计

## 5.1 设计目标

Skill 是 MemDock V1.0 的主要 Agent 接入方式。

Skill 不依赖完整的 MCP 客户端。

优先使用 Agent 已有的 Shell 或脚本执行能力，通过 HTTP API 访问 MemDock。

Skill 文件尽量简洁，不包含复杂的运行环境。

## 5.2 Skill 划分

建议采用独立 Skill：

| Skill        | 功能     | Agent 自动调用 |
| ------------ | ------ | ---------- |
| `mem-search` | 搜索历史记忆 | 允许         |
| `mem-save`   | 保存记忆   | 禁止         |
| `mem-list`   | 列出记忆   | 仅用户主动      |
| `mem-update` | 更新记忆   | 仅用户主动      |
| `mem-delete` | 删除记忆   | 仅用户主动      |

自动调用策略的重点是严格区分读取与写入。

## 5.3 Agent 调用方式

不同 Agent 的 Skill 触发语法可能不同。

建议遵循各自客户端的原生方式。

Pi 可使用：

```
/skill:mem-save
/skill:mem-search
```

Codex 可使用：

```
$mem-save
$mem-search
```

具体语法以所安装的 Agent 版本为准。

系统不应假设 MCP 工具能够自动注册成斜杠命令。

如果客户端支持自定义命令别名，可额外映射：

```
/mem-save
/mem-search
```

## 5.4 Skill 文件结构

```
skills/
├── mem-search/
│   └── SKILL.md
├── mem-save/
│   └── SKILL.md
├── mem-list/
│   └── SKILL.md
├── mem-update/
│   └── SKILL.md
└── mem-delete/
    └── SKILL.md
```

可以通过共享的轻量客户端脚本封装 HTTP 请求，避免每个 Skill 重复实现。

Skill 负责告诉 Agent：

- 什么时候调用。
- 如何自动识别项目。
- 如何构造请求。
- 如何解释返回结果。
- 什么时候禁止写入。

## 5.5 自动读取策略

`mem-search` 允许 Agent 在普通开发过程中自动调用。

适用场景：

- 需要了解以前确定的技术方案。
- 需要遵循历史开发约定。
- 开发已有模块。
- 修改系统架构或数据库设计。
- 遇到之前可能解决过的问题。
- 跨设备、跨 Agent 接续开发任务。

不必查询的场景：

- 简单代码格式化。
- 普通语法问题。
- 不需要历史上下文的简单修改。
- 与当前项目无关的讨论。

不应在每次会话启动时读取全部记忆。

## 5.6 写入权限控制

普通开发任务中，Agent 不允许主动保存、更新或删除记忆。

实现时采用双重控制：

**第一层：Skill 行为约束**

写入 Skill 只响应用户明确的命令。

**第二层：实际授权控制**

写入 API 不应仅依赖模型是否遵守 Skill 指令。

建议将读取权限和写入权限分离。

当需要严格防止非授权写入时，采用客户端命令适配器或本地授权工具，将用户的显式写入动作转换为独立授权请求。

普通自动检索使用只读权限。

仅靠 `SKILL.md` 的文字说明，不能构成严格的安全边界。

---

# 六、MCP 接入设计

## 6.1 MCP 定位

MCP 是 MemDock 的第二种接入方式。

与 Skill 共用同一套记忆服务。

主要用于兼容具备 MCP 客户端能力的 Coding Agent。

## 6.2 MCP 工具

计划提供：

```
memory_search
memory_save
memory_list
memory_update
memory_delete
project_info
```

工具名称保持明确、简洁。

各工具通过统一业务逻辑访问 SQLite，不单独实现记忆管理逻辑。

## 6.3 MCP 传输方式

推荐支持 Streamable HTTP MCP。

通过与 REST API 相同的 Go 服务端和 HTTP 端口提供服务。

例如：

```
/api/v1/...
/mcp
```

不必启动独立的 MCP 容器。

针对不原生支持远程 MCP 的客户端，可后续提供轻量桥接适配。

## 6.4 MCP 权限

自动使用的 MCP 工具应默认仅具有读取能力。

写入工具需要单独授权，或者由客户端明确触发用户命令后执行。

MCP 本身不负责判断用户是否授权写入，MemDock 必须在适配层或 API 层落实权限控制。

---

# 七、搜索与性能设计

## 7.1 V1.0 搜索技术

采用：

**SQLite + FTS5**

不使用：

- Embedding 模型
- 向量数据库
- Reranker
- 独立搜索引擎
- 外部 AI 模型服务

V1.0 的搜索目标是快速、准确地找到已经保存的项目记忆。

## 7.2 中文检索

FTS5 默认分词器对中文的支持有限。

V1.0 可以采用 `trigram` 分词器，并结合普通关键词匹配。

需要处理：

- 英文技术术语
- 中文连续短语
- 中英文混合查询
- 短于三个字符的查询
- 常用技术缩写
- 多关键词组合

对于两字中文词语，应提供额外的短词匹配回退方案。

例如：

记忆内容：

```
项目采用 JWT 身份认证，刷新令牌使用 Redis 管理。
```

搜索：

```
JWT
身份认证
刷新令牌
Redis
```

应能够获得相应结果。

对于“登录鉴权”与“身份认证”这种不完全相同的表达方式，V1.0 不保证具备语义理解能力。

## 7.3 搜索范围

每次搜索必须关联当前项目。

例如：

```
project_id = proj_001
query = JWT
```

不能默认跨项目搜索。

Web Console 可以提供明确的全局搜索入口。

## 7.4 搜索结果

默认返回最相关的 5 条记忆。

支持：

- 关键词相关性排序
- 分类筛选
- 标签筛选
- 更新时间排序
- 返回数量限制
- 结果内容长度限制

FTS5 相关性可以使用 BM25 排序，再结合项目过滤、状态过滤等规则。

避免将大量无关内容注入 Agent 上下文。

## 7.5 性能优化

建议采用以下策略：

1. 项目身份优先在客户端本地识别。
2. 为项目 ID、状态及常用筛选字段建立索引。
3. 使用 FTS5 倒排索引。
4. 限制单次返回的记忆数量。
5. 控制 SQLite 写入并发。
6. 使用预编译 SQL。
7. 减少不必要的远程 API 请求。
8. 不调用 Embedding 或其他 AI 模型。
9. 根据访问情况缓存仓库与项目的映射。
10. 避免每次查询都加载整个记忆库。

性能验收应分别测量数据库搜索耗时和完整 API 响应耗时。

## 7.6 性能目标

建议将以下指标作为 V1.0 的测试目标，而非未经测试的性能承诺。

| 指标            | 建议目标         |
| ------------- | ------------ |
| 10 万条记忆的关键词检索 | P95 低于 50ms  |
| 普通记忆保存        | P95 低于 100ms |
| 项目识别（已有映射）    | 尽量避免额外网络请求   |
| 检索返回数量        | 默认 5 条       |
| Docker 容器数量   | 1 个          |
| GPU 需求        | 无            |

上述服务端指标不包含 Agent 模型推理时间和跨网络延迟，需在指定硬件及并发条件下实测。

---

# 八、Web 管理界面

## 8.1 设计目标

Web Console 用于日常记忆管理。

界面应当简洁、快速，不需要做成复杂的知识管理平台。

建议 PC 优先，兼容移动端。

## 8.2 首页

展示：

- 项目总数
- 记忆总数
- 最近更新的项目
- 最近保存的记忆
- 服务运行状态

支持从首页快速进入项目。

## 8.3 项目管理

每个项目展示：

- 项目名称
- Git 仓库地址
- 关联仓库
- 记忆数量
- 最后更新时间

支持：

- 编辑项目名称
- 绑定新的 Git 仓库
- 解绑仓库
- 合并项目
- 删除项目（需确认）

项目合并时，应检查记忆冲突，并保留必要的操作历史。

## 8.4 记忆管理

项目详情页展示记忆列表。

主要功能：

- 关键词搜索
- 分类筛选
- 查看记忆详情
- 编辑记忆
- 删除或恢复记忆
- 查看创建时间
- 查看更新时间
- 查看来源 Agent

建议采用列表加详情的布局。

## 8.5 系统设置

提供：

- 当前安全模式
- 服务版本
- 数据存储状态
- 已配置的认证方式
- 数据备份与恢复
- 服务健康状态

涉及网络安全的关键配置以部署环境变量为准，避免 Web 端随意修改运行安全模式。

---

# 九、HTTP API 设计

## 9.1 API 原则

所有 Skill、MCP 和 Web 管理操作复用统一的业务服务。

REST API 使用 JSON。

建议统一路径：

```
/api/v1
```

## 9.2 核心接口

| 方法     | 路径                               | 功能         |
| ------ | -------------------------------- | ---------- |
| POST   | `/api/v1/projects/resolve`       | 根据仓库信息识别项目 |
| GET    | `/api/v1/projects`               | 获取项目列表     |
| GET    | `/api/v1/projects/{id}`          | 获取项目详情     |
| GET    | `/api/v1/projects/{id}/memories` | 获取记忆列表     |
| POST   | `/api/v1/memories`               | 创建记忆       |
| GET    | `/api/v1/memories/{id}`          | 获取记忆       |
| PATCH  | `/api/v1/memories/{id}`          | 更新记忆       |
| DELETE | `/api/v1/memories/{id}`          | 删除记忆       |
| POST   | `/api/v1/memories/search`        | 搜索记忆       |
| GET    | `/healthz`                       | 健康检查       |

接口必须验证当前身份是否有权操作对应项目。

## 9.3 查询请求示例

```
{
  "repository": "github.com/company/viral-flow",
  "query": "JWT 身份认证",
  "limit": 5
}
```

服务端负责解析仓库映射、确认项目权限并检索。

## 9.4 返回结果

```
{
  "project_id": "proj_001",
  "results": [
    {
      "id": "mem_1024",
      "title": "JWT 身份认证方案",
      "category": "architecture",
      "content": "项目采用 JWT 进行身份认证。",
      "updated_at": "2026-11-20T10:00:00Z"
    }
  ]
}
```

以上为接口结构示例。

API 返回的记忆是历史上下文数据，而不是具有更高权限的系统指令。Agent 应结合当前代码和用户要求判断其是否仍然适用。

---

# 十、数据库与存储

## 10.1 数据库选型

采用 SQLite。

优点：

- 无独立数据库进程
- 适合单容器部署
- 运维成本低
- 资源消耗低
- 具有事务支持
- 支持 FTS5
- 易于备份

## 10.2 数据结构

建议包含以下主要数据表：

```
projects
project_repositories
memories
memory_tags
memory_revisions
memories_fts
```

其中：

`projects` 管理项目身份。

`project_repositories` 管理仓库与项目的映射关系。

`memories` 保存记忆内容。

`memory_tags` 管理标签。

`memory_revisions` 保存必要的历史版本。

`memories_fts` 是全文搜索索引。

具体字段及索引在开发阶段确定。

## 10.3 SQLite 配置

建议使用：

- WAL 模式
- Busy Timeout
- 外键约束
- 合理的连接池大小
- 数据库迁移机制
- 定期检查数据库完整性

单实例部署使用本地持久化目录。

不建议将活动中的 SQLite 数据库直接放在不受支持的网络共享文件系统上。

## 10.4 备份与恢复

必须支持：

- 手动备份
- 手动恢复
- 备份文件下载
- 恢复前验证
- 备份结果检查

优先使用 SQLite 官方备份机制。

不能在数据库持续写入期间，仅复制主 `.db` 文件而忽略 WAL 数据。

建议允许管理员将备份文件保存到宿主机指定目录。

---

# 十一、Docker 部署设计

## 11.1 部署原则

MemDock V1.0 使用一个 Docker 容器运行。

容器包含：

- Go Server
- REST API
- MCP Server
- Web Console 静态资源
- SQLite 数据库驱动

不需要：

- Redis
- PostgreSQL
- Qdrant
- Elasticsearch
- 独立 Node.js 服务
- Embedding 推理服务

## 11.2 Docker 镜像

使用多阶段构建：

1. Go 编译阶段。
2. Web 静态资源构建阶段（如需要）。
3. 最终轻量运行镜像。

Web 资源可以嵌入 Go 可执行文件。

正式运行容器采用非 root 用户。

支持 AMD64 和 ARM64 架构。

## 11.3 Docker Compose

建议使用以下基础配置：

```
services:
  memdock:
    build: .
    container_name: memdock
    restart: unless-stopped

    ports:
      - "127.0.0.1:8080:8080"

    environment:
      MEMDOCK_ADDR: "0.0.0.0:8080"
      MEMDOCK_DATA_DIR: "/data"
      MEMDOCK_SECURITY_MODE: "private"

    volumes:
      - ./data:/data

    healthcheck:
      test: ["CMD", "/app/memdock", "healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
```

这是目标部署配置，要求服务端实现相应的环境变量与健康检查子命令。

## 11.4 数据持久化

宿主机目录：

```
./data/
```

容器内目录：

```
/data/
```

数据库：

```
/data/memdock.db
```

容器删除、重建或更新时，不应导致数据丢失。

## 11.5 启动与更新

首次启动：

```
docker compose up -d --build
```

查看日志：

```
docker compose logs -f memdock
```

重新构建：

```
docker compose up -d --build
```

数据库结构升级通过版本化迁移脚本完成。

升级前建议自动检查数据库兼容性，并提供备份机制。

## 11.6 资源要求

由于不运行 AI 模型或向量数据库，MemDock 的资源需求应当较低。

建议以以下环境作为 V1.0 的基本测试配置：

- 1 vCPU
- 512MB RAM
- SSD 存储
- Linux Docker

这是测试配置，不代表已经验证的最低资源需求。

应通过实际压力测试确认内存占用、并发能力和磁盘空间。

---

# 十二、网络与安全设计

## 12.1 双运行模式

MemDock 支持：

**Private 模式（默认）**

用于 Tailscale 私有网络。

**Secure 模式**

用于公网或不可信网络。

## 12.2 模式对比

| 能力              | Private              | Secure      |
| --------------- | -------------------- | ----------- |
| 默认使用            | 是                    | 否           |
| MemDock 应用层 TLS | 默认不启用                | 强制 HTTPS 访问 |
| API Token       | 默认不启用                | 必须          |
| Web 身份认证        | 依赖受限内网访问             | 必须          |
| 传输保护            | Tailscale 加密         | TLS 加密      |
| 公网直接访问          | 禁止                   | 允许受控发布      |
| 访问限制            | Tailnet ACL / Grants | 认证、授权与网络策略  |

## 12.3 Private 模式

适用场景：

一台服务器运行 MemDock，其他 Windows、Mac、Linux 设备通过 Tailscale 访问。

建议部署方式：

```
Codex / Pi
     │
Tailscale 网络
     │
Tailscale Serve
     │
127.0.0.1:8080
     │
MemDock Docker
```

MemDock 应用层可以使用 HTTP。

跨设备的 Tailnet 通信由 Tailscale 提供加密，Tailscale Serve 也可以提供 HTTPS 入口。

此模式默认关闭 MemDock 自身的 Token 认证。

但仍需要通过 Tailnet 访问规则限制可访问设备和用户。

不允许将未认证的 Private 模式直接暴露到公网。

## 12.4 Secure 模式

当需要公网部署时，启用：

- HTTPS
- API Token 认证
- Web Console 登录认证
- 接口权限校验
- 读写权限隔离

推荐架构：

```
Codex / Pi / Browser
         │
       HTTPS
         │
     Caddy
         │
  Docker 内部网络
         │
     MemDock
         │
      SQLite
```

Caddy 负责 TLS 证书管理。

MemDock 负责身份认证和接口授权。

可以使用自有域名与免费 TLS 证书。

如果由反向代理终止 HTTPS，MemDock 后端应限制在可信内部网络，不允许公网绕过代理直接访问。

## 12.5 安全配置

建议使用：

```
MEMDOCK_SECURITY_MODE=private

MEMDOCK_AUTH_ENABLED=false
MEMDOCK_TLS_ENABLED=false
```

公网模式：

```
MEMDOCK_SECURITY_MODE=secure

MEMDOCK_AUTH_ENABLED=true
MEMDOCK_TLS_ENABLED=true
```

具体 API Token、证书和反向代理配置通过安全的环境变量、Secret 或配置文件提供。

服务器在 Secure 模式启动时必须进行安全配置校验。

缺少认证配置或可信 TLS 接入配置时，应拒绝启动。

## 12.6 API 权限

建议将权限分为：

- `memory:read`
- `memory:write`
- `project:manage`
- `admin`

对于自动调用的 Agent，优先授予只读访问权限。

写入权限通过用户明确触发的命令流程使用。

公网 API Token 必须安全存储，不记录在日志中。

## 12.7 Web 安全

Web Console 需要考虑：

- 会话认证
- XSS 防护
- CSRF 防护
- 安全 Cookie
- 访问控制
- 输入校验
- 敏感信息脱敏

Private 模式虽然可以不启用登录，但应限制部署访问范围。

---

# 十三、运行日志与可观测性

## 13.1 日志

Go 服务统一输出结构化日志。

主要记录：

- 服务启动和停止
- API 请求状态
- 项目识别结果
- 记忆保存与更新操作
- 数据库异常
- MCP 调用异常
- 身份认证失败

日志中不记录 API Token、密码或其他敏感凭证。

记忆正文也不应默认完整写入访问日志。

## 13.2 健康检查

提供：

```
GET /healthz
```

返回服务运行状态。

数据库健康检查可以另外提供内部检查机制。

## 13.3 系统状态

Web Console 可查看：

- 服务版本
- 运行模式
- 项目数量
- 记忆数量
- 数据库状态
- 最近备份状态

第一版不必部署 Prometheus、Grafana 等监控组件。

---

# 十四、项目代码结构

建议采用以下 Go 项目结构：

```
memdock/
│
├── cmd/
│   └── server/
│       └── main.go
│
├── internal/
│   ├── api/
│   ├── auth/
│   ├── config/
│   ├── memory/
│   ├── project/
│   ├── search/
│   ├── storage/
│   ├── mcp/
│   └── backup/
│
├── web/
│
├── skills/
│   ├── mem-save/
│   ├── mem-search/
│   ├── mem-list/
│   ├── mem-update/
│   └── mem-delete/
│
├── scripts/
│
├── migrations/
│
├── Dockerfile
├── compose.yaml
├── .dockerignore
├── .env.example
├── go.mod
├── go.sum
├── AGENTS.md
└── README.md
```

所有服务端功能统一在 Go 中实现。

尽量避免引入不必要的框架和额外运行时。

---

# 十五、典型使用场景

## 15.1 场景一：主动保存架构设计

用户与 Codex 讨论数据库架构。

方案确定后，用户输入：

```
$mem-save 记住刚才确定的数据库架构
```

Codex：

1. 提取本次指定的架构内容。
2. 识别当前 Git 仓库。
3. 生成结构化记忆。
4. 调用 MemDock。
5. 返回保存结果。

记忆永久保存在对应项目中。

## 15.2 场景二：跨设备开发

用户在 Windows 上通过 Codex 保存了一条项目记忆。

随后在 macOS 上使用 Pi 开发同一仓库。

Pi 读取当前 Git Remote，并识别为相同项目。

开发过程中需要历史架构时，调用 `mem-search`。

MemDock 返回对应记忆。

Pi 根据历史信息继续开发。

整个过程不需要用户手动切换项目。

## 15.3 场景三：Agent 自动查询

用户要求：

```
继续完善登录模块。
```

Codex 判断当前任务可能涉及历史登录方案。

自动调用 `mem-search`。

MemDock 返回相关历史记忆。

Codex 结合实际代码使用这些信息。

**不会自动修改或新增记忆。**

## 15.4 场景四：变更历史技术方案

用户决定取消之前采用的某项技术。

执行：

```
/mem-update 更新以前的认证方案
```

Agent 查询相关记忆，确认目标记录后执行更新。

旧的方案可以保留为历史版本，但默认不参与后续有效记忆检索。

## 15.5 场景五：Web 管理

用户通过浏览器打开 MemDock Console。

进入 `viral-flow` 项目。

可查看：

- 架构决策
- 开发规范
- 历史问题
- 项目上下文

可以直接编辑、删除或恢复记忆。

Web 管理操作同样受身份及权限策略约束。

---

# 十六、V1.0 开发范围

## 16.1 核心服务

- Go HTTP Server
- SQLite 持久化
- 数据库迁移
- 项目识别与自动创建
- GitHub / Gitee 地址规范化
- 项目和仓库映射
- 记忆增删改查
- FTS5 全文检索
- 中文检索兼容
- REST API
- MCP Server

## 16.2 Agent 接入

- `mem-search` Skill
- `mem-save` Skill
- `mem-list` Skill
- `mem-update` Skill
- `mem-delete` Skill
- Codex 兼容验证
- Pi 兼容验证
- Agent 自动读取规则
- 用户主动写入控制
- 读写权限隔离

## 16.3 Web Console

- 项目列表
- 项目详情
- 记忆列表
- 记忆搜索
- 记忆详情
- 记忆编辑
- 记忆删除和恢复
- 仓库绑定
- 系统状态

## 16.4 部署与安全

- Dockerfile
- Docker Compose
- AMD64 / ARM64 镜像
- 数据持久化
- Tailscale 部署
- Private 模式
- Secure 模式
- HTTPS 接入
- API Token 认证
- Web 认证
- 健康检查
- 数据备份与恢复
- 日志管理

---

# 十七、V1.0 不包含的功能

以下功能不进入第一版：

1. Embedding 模型。
2. 向量数据库。
3. 语义向量检索。
4. Reranker。
5. 自动分析全部 Agent 会话。
6. 自动总结会话。
7. Agent 自主保存记忆。
8. 自动建立知识图谱。
9. 复杂的多用户协作平台。
10. 复杂的任务调度系统。
11. 大型分布式架构。
12. 云端 AI 推理服务依赖。

第一版不预先实现这些功能，只保留必要的可扩展接口。

---

# 十八、验收标准

MemDock V1.0 完成后，至少应满足以下要求。

### 项目识别

- 相同 GitHub / Gitee 仓库在不同机器上被识别为同一项目。
- 不同仓库即使名称相同，也不会错误合并。
- SSH 与 HTTPS 仓库地址能够统一识别。
- 已绑定的项目支持仓库迁移。
- 无 Git Remote 时不错误合并项目。

### 记忆管理

- 用户能够通过 Skill 主动保存记忆。
- Agent 不会因普通开发对话自动保存记忆。
- 用户能够修改和删除记忆。
- 记忆在容器重建后仍然保留。
- 不同项目之间默认隔离。

### 自动检索

- Codex 和 Pi 可以根据任务自动调用搜索。
- 默认只查询当前项目。
- 不会在每次会话启动时加载全部记忆。
- 检索结果限制数量和长度。
- 中文和英文关键词均可搜索。

### Docker

- 可以使用 Docker Compose 启动。
- 只需要一个 MemDock 应用容器。
- SQLite 数据持久化。
- 支持服务器重启自动恢复。
- 支持 AMD64 / ARM64。
- 可以备份和恢复数据库。

### 安全

- Private 模式不直接暴露公网。
- Tailscale 网络可以正常访问。
- Secure 模式要求认证及 HTTPS。
- 自动读取权限与写入权限能够隔离。
- 未授权请求无法执行记忆修改。
- 日志不泄露认证信息。

---

# 十九、未来扩展方向

在 V1.0 稳定后，可以逐步考虑：

**V1.1：搜索增强**

- Embedding 向量生成
- 语义搜索
- FTS5 + 向量混合检索
- 检索结果重排序

**V1.2：Agent 兼容增强**

- 更多 Coding Agent
- 自定义命令适配
- 跨 Agent 统一配置
- 项目级记忆共享规则

**V2.0：高级记忆管理**

- 记忆之间的关联
- 冲突检测
- 历史决策可视化
- 更精细的授权与团队协作

未来版本仍然坚持用户主动控制写入的基本原则。

---

# 二十、项目最终定位

MemDock 是一个轻量、独立、可自行部署的 Coding Agent 长期记忆服务。

通过 Docker 部署，使用 Go 和 SQLite 构建服务端，通过 FTS5 提供快速全文检索。

支持 Codex、Pi 等 Agent 通过 Skill 或 MCP 访问。

使用 GitHub、Gitee 等 Git Remote 地址自动识别项目，实现跨设备、跨 Agent 的统一记忆管理。

默认通过 Tailscale 私有网络运行，未来可切换为具备 HTTPS 和身份认证的公网部署。

整个系统不依赖额外的 AI 模型，不自动总结开发会话，也不自动写入记忆。

**最终核心理念：**

**主动记忆、智能回忆、跨端共享、轻量运行。**

**MemDock — Remember what matters. Recall when needed.**

这份方案可以作为 MemDock V1.0 的产品需求与技术实施基线。实际开发时，我建议优先打通 **Git 项目识别 → Skill 保存 → SQLite 持久化 → Skill 自动检索** 这条核心链路，再完成 MCP、Web Console 和公网安全模式，保证项目从一开始就能实际使用。
