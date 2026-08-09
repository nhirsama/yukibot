# Forwarder feature

核心公开入口是一个不依赖 Telegram SDK、数据库驱动和应用主框架的可复用转发模块。它参考了
[`alpersamur3/telegram-forwarder`](https://github.com/alpersamur3/telegram-forwarder)
（MIT License）的功能行为，但重新定义了模块边界，没有复制其 Pyrogram handler 结构。

## 已抽象的行为

- 来源 chat/topic 到目标 chat/topic 的多路由匹配；
- 数字 chat ID 与公开 `@username` 的配置解析；
- 添加路由时由仓储分配 ID，相同配置幂等复用已有路由；
- 默认自动加入源频道，或按持久游标定时拉取未加入的公开频道；
- 不区分大小写的关键词过滤；
- 内容类型白名单、黑名单及服务消息开关；
- 默认 native forward 和显式 copy 模式；
- native forward 受限时按路由回退到 copy；
- 向论坛群转发时自动创建、复用与来源同名的话题；指定超级群组内部话题时使用“群组名/话题名”，
  未指定话题时使用群组名；
- 源频道改名时同步自动话题名称；
- 回复链 source/destination message ID 映射；
- 相册按 `(chat_id, grouped_id)` 缓冲、排序并整体发送；
- copy 消息的编辑同步与全部目标消息的删除同步；
- 缺少来源 chat 的删除事件默认不做危险的模糊匹配；
- 服务消息规范化为纯文本；
- chat 级路由图循环检测；
- 重放与并发重复事件在 Telegram 副作用前检查持久映射。

## 模块边界

```text
Telethon adapter
        |
        | IncomingMessage / TelegramGateway
        v
Forwarder / ForwarderService
        |
        +-- RouteRepository
        +-- MessageLinkRepository
        +-- ManagedTopicRepository
        +-- PollCursorRepository
```

核心模块不会创建 Telethon client、读取环境变量、注册事件 handler、处理管理员命令或决定数据库。
现有 `feature.py`、`job_repository.py`、`repository.py` 和 `infrastructure/` 在核心边界之外完成接入。
`yukibot.features.forwarder` 只导出核心 API，不会隐式加载 Kernel、SQLite 或 Telethon 接入层。

`TelegramGateway` 是功能本地的 Protocol。具体实现位于
`features/forwarder/infrastructure/telethon_gateway.py`，应完成以下转换：

- Forwarder 操作 -> Telethon API 调用；
- Telethon FloodWait -> `RetryAfter`；
- 原生转发受限 -> `NativeForwardUnsupported`；
- 消息不存在 -> `MessageNotFound`；
- 内容未变化 -> `MessageNotModified`。

Telethon update 到应用事件的规范化由共享的 `adapters/telegram/event_source.py` 负责，功能专用
gateway 不注册 handler，也不拥有 client 生命周期。

`SourcePoller` 是框架接入层的另一种消息来源。它按照路由中的 `poll_interval_seconds` 拉取公开
频道，并把结果发布成同一种 `TelegramMessageReceived`，随后仍走持久 job、相册分组、路由匹配和
发送流程。首次配置将 `forwarder_poll_cursors` 初始化为当前最新消息 ID；每批事件成功写入 job 后
才推进游标。轮询只发现新增消息，不提供编辑和删除同步。

未显式配置 `DestinationEndpoint.topic_id` 时，`ManagedTopicService` 会先判断目标是否为论坛群。
论坛群使用 `(source_chat_id, source_topic_id, destination_chat_id)` 持久化自动话题；创建请求使用
稳定 random ID，让创建成功但映射尚未落库时的重试仍保持幂等。创建后只使用持久化的 `topic_id`
定位，普通消息
不会根据临时或缺失的会话标题重命名话题；只有明确的频道改名事件或管理操作会同步标题。显式话题
和普通群不会进入自动话题管理。

格式化实体和媒体不被复制到领域模型。gateway 收到稳定的 `MessageRef` 后，应使用 Telethon 读取、
复制或转发原始消息，从而完整保留 Telegram 能力且不污染业务边界。

## 最小用法

```python
from datetime import UTC, datetime

from yukibot.features.forwarder import (
    ContentType,
    DestinationEndpoint,
    Forwarder,
    ForwarderService,
    IncomingMessage,
    InMemoryMessageLinkRepository,
    InMemoryRouteRepository,
    MessageRef,
    Route,
    SourceEndpoint,
)

routes = InMemoryRouteRepository(
    [
        Route(
            id=1,
            source=SourceEndpoint(-100100),
            destination=DestinationEndpoint(-100200, topic_id=10),
        )
    ]
)
links = InMemoryMessageLinkRepository()

# gateway 是满足 TelegramGateway Protocol 的 Telethon adapter。
service = ForwarderService(routes, links, gateway)
forwarder = Forwarder(service)

await forwarder.handle_message(
    IncomingMessage(
        ref=MessageRef(-100100, 42),
        content_type=ContentType.TEXT,
        text="hello",
        occurred_at=datetime.now(UTC),
    )
)
await forwarder.close()
```

## 框架运行路径

直接使用 `Forwarder` facade 时，相册由内存滑动窗口组装，适合嵌入和单元测试。Yukibot 框架本身
不走这条路径：`ForwarderFeature` 将 receive/edit/delete 事件写入 `forwarder_jobs`，相册消息共享
`group_key` 和可执行时间，由单 worker 批量领取。worker 根据 `RetryAfter` 或指数退避重新调度，
成功任务立即删除，永久错误和达到最大尝试次数的任务进入 `failed`，启动时恢复中断的
`processing` 任务。因此 job 表是短期工作队列，不是 Telegram 消息归档。

`MessageLinkRepository.save_many()` 使用幂等 upsert，服务在发送前检查已有映射，并用 route 级锁
阻止同进程并发重复发送。这提供 at-least-once 任务执行和常规重放幂等；Telegram 发送成功但映射
落库前进程崩溃仍可能产生重复，因为两个系统之间不存在原子事务。映射同时记录实际使用的
`delivery_mode`：copy 回退的消息继续同步编辑，原生 forward 消息不会被错误地调用编辑接口。

## 明确排除

- Pyrogram 兼容层；
- Pyrogram 事件注册；
- JSON 路由文件；
- `/add`、`/remove`、`/status` 等展示层命令；
- 全局可变单例。
