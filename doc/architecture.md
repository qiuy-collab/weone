# 微信陪伴 Agent 系统架构图

> 设计方向：**weclaw 做微信接入层，openclaw 思路改造成自有 Agent Runtime，上层补 companion 产品能力。**

## 总体架构图

```text
┌──────────────────────────────────────────────────────────────┐
│                        WeChat 用户侧                         │
│  用户消息 / 语音 / 图片 / 文件 / 主动触达 / 多轮对话         │
└──────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────┐
│                    WeChat 接入网关层（weclaw）               │
│--------------------------------------------------------------│
│  ilink/client.go      负责 iLink API 调用                    │
│  ilink/monitor.go     负责 long-poll 收消息                  │
│  messaging/sender.go  负责发文本/图片/文件/typing            │
│  messaging/media.go   负责媒体上传/下载                      │
│  cmd/start.go         启动、登录、守护、监控                 │
└──────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────┐
│                    Message Router / Handler                  │
│--------------------------------------------------------------│
│  messaging/handler.go                                        │
│  - 消息去重                                                   │
│  - 命令解析                                                   │
│  - 用户标识提取                                               │
│  - 构造 conversationKey                                       │
│  - 调用 Orchestrator                                          │
└──────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────┐
│                    Companion Orchestrator                    │
│--------------------------------------------------------------│
│  internal/orchestrator/service.go                            │
│                                                              │
│  流程：                                                      │
│  1. 解析用户/会话/角色                                        │
│  2. 加载 persona                                              │
│  3. 读取 profile + memory                                     │
│  4. 组装 prompt                                               │
│  5. 调用 runtime/provider                                     │
│  6. 后处理回复                                                │
│  7. 写入消息、摘要、长期记忆                                  │
└──────────────────────────────────────────────────────────────┘
          │                 │                   │
          │                 │                   │
          ▼                 ▼                   ▼
┌────────────────┐  ┌────────────────┐  ┌────────────────────┐
│ Persona 服务    │  │ Memory 服务     │  │ Session/Profile 服务 │
│----------------│  │----------------│  │--------------------│
│ 角色身份        │  │ 短期记忆        │  │ 当前会话状态         │
│ 语气风格        │  │ 长期记忆        │  │ 用户资料             │
│ 边界规则        │  │ 关系进展摘要     │  │ persona 绑定         │
│ system prompt  │  │ 偏好/事件提取    │  │ 上下文窗口管理       │
└────────────────┘  └────────────────┘  └────────────────────┘
          │                 │                   │
          └─────────────────┴───────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────┐
│                 Agent Runtime（自有内核，参考 openclaw）     │
│--------------------------------------------------------------│
│  internal/provider/                                          │
│  internal/prompt/                                            │
│  internal/tools/（后续可扩展）                               │
│                                                              │
│  能力：                                                      │
│  - Provider 抽象                                              │
│  - 会话编排                                                   │
│  - Prompt 构建                                                │
│  - 工具调用扩展（后续）                                       │
│  - 多模型路由                                                 │
└──────────────────────────────────────────────────────────────┘
                              │
          ┌───────────────────┼───────────────────┐
          │                   │                   │
          ▼                   ▼                   ▼
┌────────────────┐  ┌────────────────┐  ┌────────────────┐
│ OpenAI兼容模型  │  │ Anthropic模型   │  │ 其他自定义模型   │
│ /v1/chat...    │  │ Messages API   │  │ 私有网关/本地LLM │
└────────────────┘  └────────────────┘  └────────────────┘

                              │
                              ▼
┌──────────────────────────────────────────────────────────────┐
│                       数据存储层                              │
│--------------------------------------------------------------│
│ SQLite / PostgreSQL                                          │
│                                                              │
│ 表：                                                         │
│ - users                                                      │
│ - conversations                                              │
│ - messages                                                   │
│ - personas                                                   │
│ - user_profiles                                              │
│ - memory_items                                               │
│ - conversation_summaries                                     │
│ - provider_configs                                           │
└──────────────────────────────────────────────────────────────┘

                              │
                              ▼
┌──────────────────────────────────────────────────────────────┐
│                    管理后台 / Open API                       │
│--------------------------------------------------------------│
│  api/server.go / admin api                                   │
│  - persona 管理                                               │
│  - provider 配置                                              │
│  - memory 查看/删除                                           │
│  - 用户画像查看                                               │
│  - 会话重置                                                   │
│  - 主动消息发送                                               │
└──────────────────────────────────────────────────────────────┘
```

## 分层说明

### 1. 微信接入层
职责：把微信消息接入系统，并把系统输出发回微信。

建议保留现有 `weclaw` 的：
- 登录
- long-poll
- 消息收发
- typing 状态
- 媒体处理
- 主动发送

### 2. Message Router / Handler
职责：保留微信消息层面的轻逻辑，不承载产品智能。

建议保留：
- 消息去重
- 命令解析
- 用户提取
- conversationKey 构建

然后把业务处理统一转发给 `Orchestrator`。

### 3. Companion Orchestrator
职责：作为系统主调度器，统一编排：
- 用户识别
- persona 加载
- memory 召回
- prompt 拼装
- provider 调用
- 输出后处理
- 数据回写

### 4. Persona 层
职责：定义陪伴角色，而不是只写一个 system prompt。

建议 persona 至少包含：
- 身份
- 性格
- 语气
- 关系目标
- 边界
- opening message
- memory policy
- model provider

### 5. Memory 层
建议分三层：
- 短期记忆：最近几轮消息
- 阶段摘要：最近一段关系进展摘要
- 长期记忆：偏好、事件、边界、称呼、情绪触发点

### 6. Agent Runtime
职责：成为你自己的 Agent 内核。

它不再依赖第三方本地 CLI Agent 作为主链路，而是由你自己控制：
- Provider 抽象
- Prompt Builder
- 会话编排
- 多模型路由
- 后续工具调用

### 7. 数据层
MVP 优先建议：
- SQLite
- 后续多实例部署再切 PostgreSQL

### 8. 后台 / Open API
建议预留 API：
- persona 管理
- provider 配置
- memory 管理
- 用户画像查看
- 主动消息发送
- 会话重置

## 推荐目录结构

```text
weclaw/
├─ cmd/
│  └─ start.go
├─ ilink/
├─ messaging/
├─ api/
│  ├─ server.go
│  └─ handlers/
│
├─ internal/
│  ├─ orchestrator/
│  │  └─ service.go
│  ├─ provider/
│  │  ├─ provider.go
│  │  ├─ openai.go
│  │  ├─ anthropic.go
│  │  └─ router.go
│  ├─ persona/
│  │  ├─ model.go
│  │  └─ service.go
│  ├─ memory/
│  │  ├─ service.go
│  │  ├─ extractor.go
│  │  ├─ retriever.go
│  │  └─ summarizer.go
│  ├─ profile/
│  │  └─ service.go
│  ├─ session/
│  │  └─ service.go
│  ├─ prompt/
│  │  └─ builder.go
│  ├─ store/
│  │  ├─ db.go
│  │  ├─ models.go
│  │  └─ repo/
│  └─ safety/
│     └─ service.go
│
├─ config/
│  └─ config.go
└─ agent/          # 可逐步降级为兼容层
```

## MVP 最小闭环

```text
微信消息
   ↓
weclaw monitor
   ↓
handler
   ↓
orchestrator
   ├─ 读取 persona
   ├─ 读取最近消息
   ├─ 读取长期记忆
   ├─ 拼 prompt
   └─ 调用 OpenAI-compatible provider
   ↓
写入 messages + memory_items
   ↓
sender 发回微信
```
