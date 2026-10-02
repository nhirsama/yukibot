# 统一消息流与单一命令鉴权

当前 Go 运行时使用一个有界入口队列，将消息接收、命令鉴权、业务订阅和 Telegram 回复解耦。owner、admin、私聊、收藏夹及群消息不拥有各自的鉴权链路；它们只是在同一身份归一化和授权策略中得到不同的判断结果。

## 数据流

```text
Telegram 实时新增 / 编辑 / 删除
  → EventSource：归一化、校验
  → queuedPublisher：标记 OriginLive
                                      ┐
                                      ├→ MessageStream（256 个待处理事件，FIFO）
                                      │    → control 订阅者（优先、有消费语义）
公开源历史轮询                        │        → 仅实时新增消息可执行控制命令
  → SourcePoller：标准化事件           │        → CommandDispatcher
  → queuedPublisher：标记 OriginHistory┘            → 回执去重
                                                    → 唯一 Authorizer
                                                    → 注册的命令 handler
                                                    → 独立 ReplySender
                                           → 未消费事件
                                             → features 订阅者
                                             → EventBus 按事件类型分发
                                             → Forwarder 等功能订阅者
                                             → 现有持久化任务与 worker
```

`MessageStream` 是入口排队和消费顺序的机制，`EventBus` 是同一流水线下游的业务事件分发器，并非第二套鉴权入口。内核队列不了解 Telegram SDK、数据库或 owner/admin 规则；组合根负责将具体生产者与订阅者连接起来。

## 唯一授权边界

- **身份归一化**：Telegram adapter 只确定发送者是谁。incoming 个人私聊缺少 `from_id` 时可使用用户 peer；群/频道、转发来源及 outgoing 收件人不能用来推断个人身份。
- **策略定义**：`management.Authorizer` 只依赖只读 `AdminLookup` 和登录账号 `Identity`。保留现有 outgoing 授权规则；其余命令允许当前 owner ID 或已登记 admin ID。
- **策略调用**：生产代码只有 `kernel.CommandDispatcher.Dispatch` 调用一次 `IsAuthorized`，`/route`、`/admin`、`/summary`、`/help` 共用该调用。重复消息先按回执消费，不再次鉴权或执行。
- **业务服务**：`management.Service` 与其他 feature service 一样是可信进程内业务 API，不再重复查询发送者权限。直接调用 service 或 handler 不等于通过授权，任何新的外部入口必须经过命令分发器。
- **架构约束**：`TestSingleCommandAuthorizationBoundary` 扫描生产 Go 代码，若新增第二个 `IsAuthorized` 调用位置即失败。跨模块测试还统计各命令根的实际鉴权次数，覆盖管理员增删，防止只在类型层面统一、运行时仍重复判断。

授权失败、参数错误、重复命令和命令回复都保持控制面消费语义，不作为普通业务消息转发。控制订阅者出错或 panic 时停止当前事件的后续传播，而不是将原始命令交给 Forwarder；后续事件仍可继续处理。

## 队列与确认语义

- **实时入口**：`Publish` 返回代表已进入内存队列，不代表已经执行或落库。接收回调不执行命令、不调用授权策略，也不直接等待 Telegram 命令回复。
- **背压**：最多缓存 256 个待处理事件，另有一个事件正在消费。队列满时生产者等待可用容量，或者因调用上下文取消而失败，不无限开 goroutine、不静默丢弃溢出消息。
- **FIFO**：单一消费者按成功入队顺序分发，订阅者按注册顺序执行；这不是对 Telegram 在网络上乱序到达消息的重新排序。业务 EventBus 在同一事件内仍按已有约定并发通知不同功能订阅者。
- **历史入口**：轮询也进入同一个队列，但来源标记为 `OriginHistory`，即使文本像 `/admin ...` 也不能执行命令。来源由可信生产者写入，不解析用户正文来确定来源。
- **历史确认**：`PublishAndWait` 等下游订阅者成功后才返回，Forwarder handler 的任务入库成功后轮询端才有资格保存游标。下游失败、没有历史订阅者或取消都会返回错误，不能把“已进入易失队列”当成已持久化。
- **取消**：同步历史发布的生产者被取消时，尚未处理的该事件会跳过；消费者正在处理时会收到取消上下文。已经成功入队的实时事件不因接收回调返回而自动取消。
- **回复隔离**：`CommandReplySender` 独立于命令策略。已记录的回复按聊天和消息 ID 消费，不再依赖收藏夹可能缺失的 `outgoing` 标志。

本队列不自动重试控制命令，以免重复产生副作用。异步事件处理失败只记录不含消息正文的失败类型，持久化前的失败事件没有新增重放保证；已有 Forwarder 持久任务的重试策略不变。

## 启停和模块切换

启动顺序为数据库、Telegram client、任务监管、management、业务 modules、message-stream、实时 EventSource。模块启动时的轮询任务允许预先排队；消费者在全部入口订阅关系安装后才启动。

正常停止按反向顺序进行：先停止实时接收，再拒绝新的入队并排空已接收事件，然后关闭业务模块、监管器和客户端。队列排空受关停超时约束，超时会取消消费者，未完成的易失事件不承诺恢复。

`/admin module disable forwarder` 在队列消费者上执行，因此不能等待一个正在等待同一队列确认的 poller 自然退出。停用时先取消 poller 的等待，再 join poller；未成功确认的历史批次不会推进游标，可在重新启用后重读并由现有持久任务去重。

## 边界和取舍

- **不是持久化 MQ**：本轮采用进程内订阅/分发队列，没有引入 Redis、RabbitMQ、Kafka 或新的消息表。进程崩溃时，尚未进入原有 Forwarder 持久任务队列的内存事件可能丢失。
- **不是 exactly-once**：没有新增跨 Telegram API 与数据库的事务，也没有解决旧有命令副作用与回执保存之间的崩溃窗口。历史批次失败后可能重读，仍依赖业务幂等/去重。
- **串行的取舍**：慢命令或慢回复会阻塞该 FIFO 消费者，随后触发接收背压。本轮优先保持授权、管理员撤销和事件消费顺序；未将长耗时命令另行改造为后台作业，也未进行吞吐压测。
- **可信调用约定**：feature service 没有对进程内代码建立沙箱；绕过 dispatcher 直接调用业务方法属于违反内部 API 约定。新的 HTTP、CLI 等外部控制接口不得直连业务 handler。
- **覆盖范围**：只调整当前 Go 运行时。历史 Python 代码没有迁移到该消息流；真实 Telegram 账号、生产负载和容器部署仍需独立验证。

主要回归覆盖消息归一化、owner/admin 授权、管理员撤销、各命令根单次鉴权、重复与编辑命令、历史消息不执行命令、回复隔离、失败隔离、FIFO、背压、取消、排空和关停超时。PostgreSQL 集成测试继续用于验证管理员状态、迁移及持久任务存取。
