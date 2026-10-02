# Summarizer

Summarizer 是独立的 Go 消息总结功能，位于 `internal/features/summarizer`。它拥有自己的模型、端口、Telegram/模型适配器、PostgreSQL 表、
命令和运行时开关，不读取 Forwarder 的表，也不依赖 Forwarder 的实现。

## Commands

```text
/summary list
/summary show <id>
/summary add <source> <destination> [时间窗]
/summary set <id> <source> <destination> [时间窗]
/summary run <id> [时间窗]
/summary enable <id>
/summary disable <id>
/summary remove <id>
/summary model show
/summary model set <provider> <model> [-api-key <key>] [-base-url <url>]
/summary model tune <input_tokens> <output_tokens> <temperature> <timeout> <retries> [concurrency]
/summary model clear
/summary prompt list
/summary prompt show
/summary prompt use <focused|decisions|technical|digest>
/summary prompt custom <自定义偏好>
/summary prompt clear
```

时间窗支持分钟、小时和天，例如 `30m`、`6h`、`1d`，默认一天，最大 30 天。当前由管理员使用
`/summary run` 手动执行；规则和成功执行记录存入 `summarizer_rules`、`summarizer_runs`，原始消息
不会写入数据库。

来源与目标支持数字 ID、`@username` 和 Telegram 公开链接。论坛话题支持：

```text
-1001234567890/42
https://t.me/c/1234567890/42
https://t.me/public_group/42
```

目标可以是私聊、频道、群组或论坛话题。发送到话题时使用稳定的群组 ID 和 `topic_id`，不会把
话题 ID 当作普通回复目标重新解析。私有聊天需要当前 Telegram 账号已经加入或有权访问，并使用
其稳定数字 ID 配置；总结规则不会保存或使用私有邀请链接。

## Model Configuration

模型调用使用 Responses HTTP/SSE 接口，不依赖 Python SDK。OpenAI 兼容服务统一配置为 `provider=openai`，并通过
`base-url` 指定服务地址。模型配置属于 Summarizer 业务数据，通过命令写入
`summarizer_model_config`，不使用环境变量。未配置模型时仍可增删和查看规则，执行 `/summary run`
时会返回明确的缺少配置提示。`model show` 只显示 API key 是否存在，不回显原值。

OpenAI：

```text
/summary model set openai gpt-4.1-mini -api-key sk-example
```

OpenAI 兼容服务：

```text
/summary model set openai model-name -api-key api-key -base-url https://models.example/v1
```

OpenAI 及其兼容服务统一使用 Responses SSE。结构化 JSON 通过提示词约束并在本地校验，不发送
兼容性差异较大的 `tools` 或 `tool_choice`。

APIArc 同样使用通用 OpenAI 配置，模型名不要添加 provider 前缀：

```text
/summary model set openai deepseek-v4-flash-free -api-key api-key -base-url https://apiarc.ai/v1
```

所有请求都使用 Responses SSE 并持续消费事件；模型配置中的 `timeout` 是连续无网络数据的读取
超时，服务端心跳或输出数据会刷新该超时，不限制一次总结的总生成时间。
调用方取消或显式总时限仍然有效。单个 SSE 事件、累计输出和总响应流另有大小限制，不能通过持续心跳绕过资源边界。

推理参数也通过业务命令调整：

```text
/summary model tune 32768 4096 0.1 120 2 3
/summary model show
```

参数依次是输入 token 上限、输出 token 上限、temperature、超时秒数、重试次数和可选并发数。
输入和输出 token 上限用于本地 map/reduce 分批预算。OpenAI Responses 兼容网关的请求体不发送
模型相关的 token 上限和 temperature，避免推理 token 耗尽输出上限或网关拒绝可选参数。

默认 `focused` 预设只保留重要事实、进展、结论、问题、行动项、风险、关键数据和有价值资源，
忽略问候、表情回应、玩笑、情绪宣泄、无结论争论、无关闲聊和重复内容。`decisions` 侧重结论与
行动项，`technical` 侧重技术变更、故障和验证，`digest` 提供覆盖面更广的简报。使用
`/summary prompt custom` 可以保存自己的总结偏好；自定义偏好不会覆盖证据约束、事实归属和消息内
提示注入防护。`/summary prompt clear` 恢复 `focused`。

## Processing

每次运行按时间窗即时读取 Telegram 历史消息，不设置消息条数硬上限，并执行以下流程：

1. 丢弃服务消息和没有文字的纯媒体消息，保留文字、媒体说明、投票、链接、回复关系、转发来源、
   发言人和相册 ID。
2. 合并同一相册，以及三分钟内同一发言人的少量连续消息。
3. 按模型输入预算分批 map，并在配置的并发上限内同时总结独立分块；ASCII 按约 3 字符/token、
   非 ASCII 按 2 token/字符保守估算，
   单批正文最多 12000 token，超过单条预算的消息使用二分边界切成多个保留原证据 ID 的片段，
   不丢弃长消息尾部内容。
4. 每批解析为结构化主题、结论、行动项和待确认问题；多批结果按相同预算分组并执行分层
   reduce，同一层的独立分组也会并发处理，直到只剩一个结果，不会把全部中间摘要一次塞回模型。
5. 删除模型返回的未知消息 ID，只保留可回溯到本次输入的证据；每个主题只附首条原消息链接和
   证据消息 ID 范围，不展开所有链接。
6. 将超过 Telegram 单条限制的结果拆分后，发送到规则指定目标或论坛话题。

群聊提示词强调发言归属、分歧、结论和明确行动项；频道提示词强调信息发布、事件更新与去重；
私聊提示词区分双方陈述。所有提示词都把聊天消息视为不可信数据，消息内的指令不能改变总结任务。
如果整个时间窗都只有被过滤的低价值内容，运行会正常发送“没有值得总结的有效信息”，不会把空主题
误报为模型调用失败。
