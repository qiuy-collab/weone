# 单包内置 Runtime 开发计划

> 目标：把当前 `weclaw` 从“微信桥接到本地外部 agent”升级成“单包部署的微信陪伴 Agent 服务”。用户无需安装 openclaw、Claude Code、Codex 等本地 agent 程序，只需要运行一个服务包，配置模型 API，扫码绑定微信，即可开始使用。

## 核心原则

1. **单包部署**：微信接入、内置 runtime、provider、web 控制台都在同一个程序内。
2. **不依赖本地第三方 agent**：`openclaw` 仅作为设计参考，不作为用户侧前置安装依赖。
3. **主链路内置化**：消息处理主链路改为 `handler -> orchestrator/runtime -> provider`。
4. **兼容层保留**：`agent/cli_agent.go`、`agent/acp_agent.go` 可暂时保留，但不再作为正式产品主路径。

## 分阶段开发

### Phase 1：初始化闭环 MVP
目标：跑通“配置模型 API → 扫码绑定微信 → 微信收发消息 → 内置 runtime 直连模型 API”。

范围：
- 扩展配置结构，新增 `runtime/provider/persona` 基础配置。
- 实现内置 runtime：
  - 最近对话上下文
  - 基础 persona prompt
  - OpenAI-compatible provider 直连
- 将默认消息处理从旧 `agent` 路由改成内置 runtime。
- 新增 HTTP API：
  - 运行状态
  - provider 配置查看/保存
  - 二维码登录会话创建/轮询
- 新增内置静态 Web 控制台：
  - 配置 provider
  - 展示二维码
  - 查看绑定状态
  - 查看服务状态

验收标准：
- 不安装任何本地 agent 程序时，程序仍可工作。
- 在控制台配置 endpoint/apiKey/model 后可保存配置。
- 可从网页发起微信扫码绑定并完成保存。
- 微信消息能触发内置 runtime 并获得模型回复。

### Phase 2：persona + memory
目标：让陪伴能力从“单次聊天”升级为“带角色和记忆的连续关系”。

范围：
- Persona 配置对象化。
- 消息持久化。
- 最近消息窗口与阶段摘要。
- 长期记忆抽取/召回。

### Phase 3：主动触达
目标：支持定时问候、沉默唤醒、事件提醒。

范围：
- 调度器
- 主动发送策略
- 任务配置 API

### Phase 4：素材库
目标：支持图片、emoji、表情素材管理和发送编排。

范围：
- 素材上传/标签管理
- 回复时素材选择
- 后续可接入 persona 风格化素材策略

## 代码结构调整

### 保留
- `ilink/`
- `messaging/sender.go`
- `messaging/media.go`
- `ilink/monitor.go`

### 主路径改造
- `config/config.go`
- `cmd/start.go`
- `messaging/handler.go`
- `api/server.go`

### 新增
- `internal/runtime/`
- `internal/provider/`
- `internal/web/`

## Phase 1 实施顺序

1. 扩展配置模型。
2. 落地内置 provider/runtime。
3. 接管默认消息链路。
4. 增加 Web API。
5. 增加内置静态页面。
6. 验证扫码绑定与微信回复闭环。

## 当前实现约束

- 先做**进程内 session/history**，后续再切数据库。
- 先支持 **OpenAI-compatible Chat Completions**。
- 先保留旧 agent 兼容代码，但默认优先使用内置 runtime。
