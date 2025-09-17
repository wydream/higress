# AI 智能工具精选 Higress Wasm 插件

## 1. 概述

本项目是一个基于 `Golang` 开发的 [Higress](https://higress.io/) Wasm 插件，旨在作为 AI 网关的核心组件，为大型语言模型（LLM）应用提供智能工具精选（Intelligent Tool Selection）能力。

在复杂的 Agent 或 Copilot 应用中，LLM 可能会被赋予大量可选工具（Tools）。当请求中包含的工具过多时，会导致 LLM 推理延迟增加、选择精度下降以及 API 调用成本上升。此插件通过在请求到达 LLM 之前，在网关层对工具列表进行智能的预处理和筛选，有效解决上述问题。

### 核心价值
- **提升性能**：显著减少传递给 LLM 的 Token 数量，加快模型响应速度。
- **提高精度**：通过 Rerank 模型筛选与用户意图最相关的工具，帮助 LLM 做出更优决策。
- **降低成本**：有效减少每次 API 请求的 Token 成本。
- **灵活配置**：提供丰富的配置选项，适应不同业务场景的需求。

## 2. 核心功能

- **智能工具精排 (Tool Reranking)**：利用外部的 Rerank 模型服务，根据用户查询（Query）与工具描述（Description）的相关性对工具列表进行重新排序和过滤。
  - 支持 **按数量（Top-N）**、**按比例（Top-K%）** 及 **组合** 方式进行筛选。
  - 支持设置相关性得分阈值，过滤掉低质量结果。
- **查询改写 (Query Rewriting)**：在工具精排前，利用一个外部 LLM 服务对用户的原始查询进行优化，使其更清晰、更适合用于工具检索，从而提升精排的准确率。
  - 支持根据**对话轮次**作为触发条件。
  - 支持灵活选择用于改写的上下文范围（如最近N条消息）。
- **可配置的失败降级策略**：当外部模型服务（Rerank/Rewrite）调用失败时，可选择是**跳过当前阶段继续处理**（`skip`），还是**中断请求并报错**（`error`），保证服务的高可用性。
- **详细的调试日志**：在关键处理环节输出 `DEBUG` 级别的日志，便于开发者追踪问题。

## 3. 工作流程

插件在接收到客户端请求后，遵循以下处理流程：

1.  **接收请求**：插件在 `ProcessRequestBody` 阶段获取到完整的请求体。
2.  **插件开关检查**：检查全局 `enabled` 配置项，若为 `false`，则跳过所有处理，直接转发原始请求。
3.  **启用条件检查**：检查请求中的工具数量是否满足 `enableConditions.toolCountThreshold` 阈值，若工具数量小于阈值，则跳过所有rewrite和rerank流程，直接转发原始请求。
4.  **查询改写阶段 (可选)**：
    a. 检查 `queryRewriting.enabled` 是否为 `true`。
    b. 检查是否满足 `triggerConditions.messageCountThreshold` 触发条件（即当前对话轮次是否大于设定阈值）。
    c. 若满足条件，则根据 `contextSelection` 策略构建上下文，调用外部 LLM 服务进行查询改写。
    d. 若调用成功，使用改写后的新查询进行下一步；若失败，则根据 `fallbackStrategy` 决定是使用原始查询还是中断请求。
5.  **工具精排阶段**：
    a. 使用上一步得到的查询（原始的或改写后的）和原始工具列表，调用外部 Rerank 模型服务。
    b. 若调用成功，根据 `filteringMethod` 和 `scoreThreshold` 对返回结果进行过滤，生成最终的精选工具列表。
    c. 若调用失败，则根据 `fallbackStrategy` 决定是使用原始工具列表还是中断请求。
6.  **请求重构**：使用精选后的工具列表替换原始请求体中的 `tools` 字段。
7.  **转发请求**：将重构后的请求体转发至后端 AI 服务。

## 4. 前提条件

- 一个正在运行的 **Higress 网关**。
- 一个可被 Higress 网关访问的 **Rerank 模型服务**（用于工具精排）。
- (可选) 一个可被 Higress 网关访问的 **LLM 服务**（用于查询改写）。

## 5. 插件配置

插件配置采用 `YAML` 格式。以下是所有可用配置项的详细说明。

### 顶级配置

| 参数 | 类型 | 是否必需 | 描述 |
| :--- | :--- | :--- | :--- |
| `_match_route_prefix_` | `Array<String>` | 是 | 插件生效的路由前缀列表。 |
| `enabled` | `Boolean` | 是 | 功能总开关。如果为 `false`，则插件不执行任何操作。 |
| `enableConditions` | `Object` | 否 | 启用功能的条件配置。详见下文。 |
| `toolReranking` | `Object` | 是 | 工具精排的核心配置。详见下文。 |
| `queryRewriting` | `Object` | 否 | 查询改写的可选增强配置。详见下文。 |

### `enableConditions` 配置

| 参数 | 类型 | 是否必需 | 描述 |
| :--- | :--- | :--- | :--- |
| `toolCountThreshold` | `Integer` | 否 | 工具数量阈值。当请求中的工具数量小于此值时，跳过所有rewrite和rerank流程。默认值为 `10`。 |

### `toolReranking` 配置

| 参数 | 类型 | 是否必需 | 描述 |
| :--- | :--- | :--- | :--- |
| `serviceName` | `String` | 是 | Rerank 模型服务的 K8s FQDN，例如 `my-rerank.default.svc.cluster.local`。 |
| `servicePort` | `Integer` | 是 | Rerank 模型服务的端口。 |
| `modelName` | `String` | 是 | 要调用的具体 Rerank 模型名称。 |
| `timeoutMillisecond` | `Integer` | 是 | 调用 Rerank 服务的超时时间（毫秒）。 |
| `filteringMethod` | `String` | 是 | 工具筛选方式。可选值：`topN` (按数量), `topK` (按比例), `combined` (组合)。 |
| `topNCount` | `Integer` | 否 | 当 `filteringMethod` 为 `topN` 或 `combined` 时使用，表示保留的工具数量上限。 |
| `topKPercent` | `Integer` | 否 | 当 `filteringMethod` 为 `topK` 或 `combined` 时使用，表示保留的工具百分比 (1-100)。 |
| `scoreThreshold` | `Float` | 否 | 相关性得分阈值 (0.0-1.0)。得分低于此值的工具将被丢弃。设置为 `0` 或不传表示禁用。 |
| `fallbackStrategy` | `String` | 是 | 当 Rerank 服务调用失败时的降级策略。可选值：`skip` (跳过精排), `error` (中断请求)。 |

### `queryRewriting` 配置

| 参数 | 类型 | 是否必需 | 描述 |
| :--- | :--- | :--- | :--- |
| `enabled` | `Boolean` | 是 | 查询改写功能开关。 |
| `serviceName` | `String` | `enabled:true`时是 | LLM 服务的 K8s FQDN。 |
| `servicePort` | `Integer` | `enabled:true`时是 | LLM 服务的端口。 |
| `modelName` | `String` | `enabled:true`时是 | 要调用的具体 LLM 模型名称。 |
| `timeoutMillisecond` | `Integer` | `enabled:true`时是 | 调用 LLM 服务的超时时间（毫秒）。 |
| `promptTemplate` | `String` | 否 | 改写提示词模板。支持 `{{.context}}` 和 `{{.query}}` 占位符，分别代表对话历史和当前查询。 |
| `maxOutputTokens`| `Integer` | 否 | 控制改写模型输出的最大 Token 数。 |
| `triggerConditions` | `Object` | 否 | 触发查询改写的条件。 |
|  L `messageCountThreshold` | `Integer` | 否 | 对话轮次超过此阈值时触发查询改写。设置为 `0` 或不设置表示不启用。 |
| `contextSelection` | `Object` | 否 | 定义用于改写的上下文范围。 |
| L `type` | `String` | 否 | 上下文选取方式。可选值：`allMessages` (全部历史), `recentMessages` (最近N条)。 |
| L `value` | `Integer` | 否 | 当 `type` 为 `recentMessages` 时，定义"N"的值。 |
| `fallbackStrategy` | `String` | 否 | 当 LLM 服务调用失败时的降级策略。可选值：`skip`, `error`。 |

### 模板语法说明

`promptTemplate` 支持Go语言的 `text/template` 语法，提供以下占位符：

- `{{.context}}`: 根据 `contextSelection` 配置选择的对话历史，格式为 `role: content` 的多行文本
- `{{.query}}`: 当前用户的查询内容（通常是最后一条用户消息的内容）

**模板示例：**
```
你是一个专业的查询改写助手。你的任务是将用户的查询改写为更适合工具选择的精确表达。

请根据以下对话历史和当前查询，输出一个更清晰、更具体的查询语句：

对话历史：
{{.context}}

当前查询：
{{.query}}

改写要求：
1. 保持原意不变
2. 使用更精确的词汇
3. 突出关键动作和对象
4. 长度控制在50个token以内

改写后的查询：
```

## 6. 配置示例

### 示例1：基础功能 - 仅工具精排

仅启用工具精排，保留最相关的5个工具，失败则跳过。

```yaml
_match_route_prefix_:
  - "/your/llm/api/route"
enabled: true
enableConditions:
  toolCountThreshold: 8  # 当工具数量小于8时跳过处理
toolReranking:
  serviceName: "your-rerank-service.default.svc.cluster.local"
  servicePort: 443
  modelName: "ali-bailian-gte-rerank-v2"
  timeoutMillisecond: 800
  filteringMethod: "topN"
  topNCount: 5
  scoreThreshold: 0.3
  fallbackStrategy: "skip"
```

### 示例2：精排 + 查询改写

启用工具精排和查询改写。当对话超过3轮时，会先调用LLM优化查询，然后再进行工具精排。

```yaml
_match_route_prefix_:
  - "/your/llm/api/route"
enabled: true
enableConditions:
  toolCountThreshold: 12  # 当工具数量小于12时跳过处理
toolReranking:
  serviceName: "your-rerank-service.default.svc.cluster.local"
  servicePort: 443
  modelName: "ali-bailian-gte-rerank-v2"
  timeoutMillisecond: 800
  filteringMethod: "topN"
  topNCount: 10
  scoreThreshold: 0.3
  fallbackStrategy: "skip"
queryRewriting:
  enabled: true
  serviceName: "your-llm-service.default.svc.cluster.local"
  servicePort: 443
  modelName: "qwen-turbo"
  timeoutMillisecond: 1500
  promptTemplate: "你是一个专业的查询改写助手。你的任务是将用户的查询改写为更适合工具选择的精确表达。\n\n请根据以下对话历史和当前查询，输出一个更清晰、更具体的查询语句：\n\n对话历史：\n{{.context}}\n\n当前查询：\n{{.query}}\n\n改写要求：\n1. 保持原意不变\n2. 使用更精确的词汇\n3. 突出关键动作和对象\n4. 长度控制在50个token以内\n\n改写后的查询："
  maxOutputTokens: 50
  triggerConditions:
    messageCountThreshold: 3 # 当对话轮次 > 3 时触发
  contextSelection:
    type: "recentMessages"
    value: 5
  fallbackStrategy: "skip"
```

### 示例3：严格的生产环境策略

启用所有功能，并采用严格的失败策略。任何外部服务调用失败都会直接导致请求中断，以保证工具选择的质量。

```yaml
_match_route_prefix_:
  - "/your/production/api/route"
enabled: true
enableConditions:
  toolCountThreshold: 15  # 生产环境设置更高的阈值
toolReranking:
  serviceName: "your-rerank-service.default.svc.cluster.local"
  servicePort: 443
  modelName: "ali-bailian-gte-rerank-v2"
  timeoutMillisecond: 800
  filteringMethod: "topN"
  topNCount: 8
  scoreThreshold: 0.5
  fallbackStrategy: "error" # 失败时中断请求
queryRewriting:
  enabled: true
  serviceName: "your-llm-service.default.svc.cluster.local"
  servicePort: 443
  modelName: "qwen-plus"
  timeoutMillisecond: 1500
  promptTemplate: "You are a professional query rewriting assistant. Your task is to rewrite user queries into precise expressions more suitable for tool selection.\n\nPlease provide a clearer, more specific query based on the following conversation history and current query:\n\nConversation History:\n{{.context}}\n\nCurrent Query:\n{{.query}}\n\nRewriting Requirements:\n1. Maintain the original meaning\n2. Use more precise vocabulary\n3. Highlight key actions and objects\n4. Keep length under 50 tokens\n\nRewritten Query:"
  maxOutputTokens: 70
  triggerConditions:
    messageCountThreshold: 2 # 对话超过2轮就触发
  contextSelection:
    type: "allMessages"
  fallbackStrategy: "error" # 失败时中断请求```

## 7. 调试

插件在关键步骤（如配置解析、触发条件判断、服务调用、失败处理等）会输出 `DEBUG` 级别的日志。您可以通过查看 Higress 网关的日志来追踪插件的详细执行流程，以便进行调试和问题排查。