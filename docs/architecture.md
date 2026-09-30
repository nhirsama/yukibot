# yukibot 架构设计

> 状态：基础框架与 Forwarder 持久任务已实现；管理面待实现
> 语言：Python 3.12+  
> 包与环境管理：uv  
> Telegram 客户端：Telethon  
> 架构形态：模块化单体、端口与适配器、功能纵向切片

## 1. 背景

yukibot 是运行在 Telegram 用户账号上的自动化程序。首个功能是将指定频道或话题中的消息转发到目标群组，后续可能加入消息归档、关键词提醒、自动回复、内容加工和定时任务等功能。

系统需要保持部署简单，同时让各功能能够独立开发、测试、启停和删除。这里不采用微服务，也不构建重量级插件框架；使用单进程 `asyncio` 应用，通过稳定契约隔离 Telethon、存储设施和业务功能。

## 2. 设计目标

1. **低耦合**：功能之间禁止直接调用实现，只能通过公开事件契约通信。
2. **边界清晰**：每个功能拥有自己的模型、表、仓储、服务和测试。
3. **基础设施可替换**：Telethon、SQLite 等实现不能泄漏到业务服务。
4. **扩展成本低**：增加普通功能只需新增一个功能包，并在组合根注册。
5. **运行可靠**：消息处理具备幂等、限流、重试、顺序控制和可观测性。
6. **保持简单**：手工依赖注入、显式注册，不使用自动扫描和 DI 框架。

## 3. 非目标

- 不为未知需求提前引入微服务、Redis、Celery、Kafka 或分布式事务。
- 不构造一套覆盖 Telegram 所有能力的通用领域模型。
- 不允许功能通过共享数据库表隐式通信。
- 不追求运行时安装第三方插件；插件均是同一代码库内受控的功能模块。
- 不同时运行 Telethon 与 Pyrogram。`telegram-forwarder` 仅作为行为和边界场景参考。

## 4. 核心原则

### 4.1 依赖方向

```text
                       bootstrap（组合根）
                    /          |           \
                   v           v            v
              features      kernel       adapters
                  |             ^            |
                  |             |            |
                  +------ contracts <--------+
```

- `kernel` 只包含生命周期、事件分发等与 Telegram 无关的机制。
- `contracts` 只包含不可变数据契约和少量协议，不包含业务实现。
- `adapters` 提供跨功能的外部系统接入，例如 Telethon client/event source、SQLite 和日志。
- `features` 的业务核心只依赖契约和自己定义的端口，不依赖 Telethon、其他功能实现或组合根。
- 功能专用的外层 adapter 归该功能所有，可以依赖通用 adapter 的窄接口，但不得进入业务核心。
- `bootstrap` 是唯一可以知道所有具体实现并把它们组装起来的位置。

### 4.2 功能拥有自己的数据

每个功能独占自己的数据库表，表名使用功能前缀，例如：

```text
forwarder_routes
forwarder_message_links
forwarder_jobs
archive_messages
reminder_rules
```

禁止跨功能外键、跨功能 SQL JOIN，以及直接读取另一个功能的表。确实需要共享信息时，由数据拥有者发布集成事件。

### 4.3 Telethon 是适配器

Telethon 的 `Client`、事件对象、消息对象和异常不得进入 `features`。Telethon 适配器负责：

- 将 Telethon update 转换为稳定的应用事件；
- 将功能发出的操作转换为 Telethon API 调用；
- 将 `FloodWait`、网络异常等转换为应用级错误；
- 隔离 Telethon 主版本升级造成的 API 变化。

对于媒体等不适合完整标准化的对象，契约只保存稳定引用，例如 `chat_id`、`message_id` 和 `grouped_id`。真正的读取、复制或转发由 Telegram 端口根据引用完成。

### 4.4 显式优于自动

功能在 `bootstrap.py` 中显式创建和注册：

```python
features = [
    build_forwarder(container),
    # build_archive(container),
]
```

这比目录扫描和 Python entry point 更容易理解、调试和静态检查。只有出现独立发布第三方插件的真实需求时，才引入动态发现机制。

## 5. 项目目录结构

```text
yukibot/
├── .github/workflows/ci.yml      # CI 与 GHCR 镜像发布
├── .env.example
├── docker/entrypoint.py          # 容器数据目录权限初始化
├── docker-compose.yml
├── Dockerfile
├── README.md
├── pyproject.toml
├── uv.lock
├── docs/                         # 架构和运维文档
├── src/yukibot/
│   ├── __init__.py
│   ├── __main__.py               # CLI 程序入口（python -m yukibot）
│   ├── bootstrap.py              # 依赖组合根
│   ├── config.py                 # 环境配置
│   ├── kernel/                   # 生命周期、任务和事件总线
│   ├── contracts/                # 稳定业务契约
│   ├── adapters/                 # SQLite、Telethon 和日志适配器
│   └── features/
│       ├── forwarder/
│       ├── management/
│       └── summarizer/
└── tests/
    ├── architecture/
    ├── contract/
    ├── integration/
    ├── unit/
    └── ...
```

`docker/entrypoint.py` 不属于业务入口。它只在容器启动阶段处理绑定目录权限，随后切换到
非 root 用户并执行 `src/yukibot/__main__.py` 暴露的 `yukibot` 命令。运行时数据统一放在根目录下
被 Git 忽略的 `data/` 中。

功能包默认保持扁平。只有单个功能明显变大后，才在其内部拆分 `domain/`、`application/` 和
`infrastructure/`，避免为了形式制造目录层级。

## 6. 内核设计

### 6.1 Feature 契约

功能只通过生命周期契约接入进程：

```python
from typing import Protocol


class Feature(Protocol):
    name: str

    async def start(self) -> None: ...

    async def stop(self) -> None: ...
```

事件订阅、仓储和外部端口通过构造函数注入。不要把包含所有服务的 `AppContext` 传给功能，否则它会演变成 Service Locator，让依赖重新变得隐式。

### 6.2 事件总线

进程内事件总线负责功能解耦，不承担持久化消息队列的职责：

```python
from collections.abc import Awaitable, Callable
from typing import Protocol, TypeVar

EventT = TypeVar("EventT")
EventHandler = Callable[[EventT], Awaitable[None]]


class EventBus(Protocol):
    def subscribe(
        self,
        event_type: type[EventT],
        handler: EventHandler[EventT],
    ) -> None: ...

    async def publish(self, event: object) -> None: ...
```

实现必须满足：

- 单个 handler 失败不能阻止其他订阅者执行；
- 所有后台任务由统一的 Task Supervisor 持有，不能裸用无人管理的 `asyncio.create_task()`；
- handler 名称、耗时、异常和事件类型进入结构化日志；
- 默认不保证进程崩溃后的事件恢复；需要可靠交付的功能必须先写入自己的任务表。

第一版可使用 `asyncio.gather(..., return_exceptions=True)` 并发分发。出现慢订阅者后，再为该功能增加独立 inbox/worker，而不是提前建设通用消息中间件。

### 6.3 应用事件

外部输入转换为不可变事件。契约不包含 Telethon 类型：

```python
from dataclasses import dataclass
from datetime import datetime


@dataclass(frozen=True, slots=True)
class MessageRef:
    chat_id: int
    message_id: int


@dataclass(frozen=True, slots=True)
class TelegramMessageReceived:
    message: MessageRef
    sender_id: int | None
    topic_id: int | None
    grouped_id: int | None
    text: str | None
    occurred_at: datetime
    outgoing: bool
```

契约按增加字段的方式演进；已有字段不得随意改变语义。需要破坏性变化时新增 `V2` 契约并保留过渡期。

## 7. 端口与适配器

### 7.1 功能本地端口

端口应由使用者定义，而不是由基础设施预先设计一个巨大的 `TelegramService`。例如 forwarder 只声明自己需要的能力：

```python
from typing import Protocol


class ForwarderTelegram(Protocol):
    async def forward_message(
        self,
        source: MessageRef,
        destination_chat_id: int,
        *,
        destination_topic_id: int | None,
        reply_to: int | None,
    ) -> MessageRef: ...

    async def copy_message(
        self,
        source: MessageRef,
        destination_chat_id: int,
        *,
        destination_topic_id: int | None,
        reply_to: int | None,
    ) -> MessageRef: ...

    async def edit_copied_message(self, target: MessageRef, text: str) -> None: ...

    async def delete_messages(self, messages: list[MessageRef]) -> None: ...
```

Telethon gateway 通过 Python 的结构化类型同时满足多个功能的小型 Protocol。新增功能通常只需新增本地端口，不必修改全局接口。

### 7.2 仓储端口

仓储 Protocol 放在功能内部，业务服务不依赖 SQLAlchemy、`aiosqlite` 或 SQL：

```python
class RouteRepository(Protocol):
    async def matching(self, chat_id: int, topic_id: int | None) -> list[Route]: ...
    async def add_auto(self, draft: RouteDraft) -> Route: ...
    async def add(self, route: Route) -> Route: ...
    async def remove(self, route_id: int) -> None: ...
```

简单查询使用专用方法，避免泄漏通用 ORM Query 对象。事务边界由单个功能服务控制；不设计跨功能事务。

`repository.py` 是功能最外层的持久化适配器，它可以使用 `contracts.database.Database` 提供的连接和事务能力，但不能把连接、游标或 SQL 类型返回给业务服务。具体 `aiosqlite.Connection` 只存在于 `adapters/database`；因此未来替换驱动时，功能业务代码不变。

## 8. Forwarder 功能设计

### 8.1 职责

Forwarder 负责：

- 管理来源到目标的路由；
- 解析数字 chat ID 或公开用户名，默认加入源频道或按配置轮询公开频道；
- 根据话题、关键词和媒体类型过滤消息；
- 原生转发或复制消息；
- 默认使用原生转发，受保护内容或不支持的操作回退为复制；
- 为未显式指定目标话题的论坛路由创建并持久化同名话题；
- 在源频道标题变化时同步自动话题标题；
- 保持相册、回复关系、copy 消息编辑和目标消息删除同步；
- 记录源消息与目标消息的映射；
- 保证自身任务幂等并防止转发循环。

它不负责创建 Telethon client、全局登录、数据库连接、日志初始化或其他功能的行为。

### 8.2 核心模型

```text
Route
- id（仓储自动分配）
- source_chat_id
- source_topic_id
- source_username
- poll_interval_seconds: null | >= 60
- destination_chat_id
- destination_topic_id
- destination_username
- mode: forward | copy
- filters
- enabled

MessageLink
- route_id
- source_chat_id
- source_message_id
- destination_chat_id
- destination_message_id
- delivery_mode: forward | copy

ManagedTopic
- source_chat_id
- destination_chat_id
- topic_id
- title

PollCursor
- source_chat_id
- last_message_id

ForwardJob
- id
- kind: receive | edit | delete
- deduplication_key
- group_key
- payload_json
- state: pending | processing | failed（`succeeded` 仅为旧数据库结构兼容值）
- attempts
- available_at
- last_error
```

用户名只用于配置、展示和恢复未加入公开频道的访问实体；消息匹配、路由图、任务去重和数据库关联
始终使用解析后的稳定 chat ID。实时路由在配置时幂等加入源频道。轮询路由不加入源频道，首次配置
从当前最新消息开始，此后按持久游标分页拉取新增消息并发布为标准 `TelegramMessageReceived`。
游标只在事件持久化后推进，重放由 `forwarder_jobs.deduplication_key` 吸收。轮询不负责编辑和删除同步。

建议约束：

```text
UNIQUE(deduplication_key)
```

`deduplication_key` 由操作类型、源会话、源消息和事件版本组成。receive/delete 使用稳定消息键；edit
包含 Telegram 编辑时间与可编辑内容指纹，因此同一次编辑重放会被去重，秒级时间戳内的连续编辑仍能
执行。worker 重试整个事件时，已成功 route 的 `MessageLink` 会在副作用前被识别，避免再次发送。
任务成功后整行删除，包含正文或标题的 `payload_json` 不作为历史数据保留；只有等待、处理、重试和失败任务
留在队列中。`MessageLink` 只保留编辑、删除、回复映射和重放去重所需的 ID 及投递模式，不保存消息内容。

### 8.3 处理流程

```text
Telethon update
    -> TelegramMessageReceived
    -> Forwarder handler
    -> 幂等写入 ForwardJob
    -> worker 领取任务
    -> 查询路由、过滤与回复映射
    -> 按显式 topic_id 或持久化的自动 topic_id 定位
    -> Telegram gateway
    -> 写入 MessageLink
    -> 删除已完成任务
```

handler 只做快速校验和任务落库，不在 Telethon update 回调里下载、上传或等待限流。worker 负责实际发送。

### 8.4 顺序与并发

- 单 worker 按 job ID 顺序处理，Telegram request limiter 额外串行化同一个目标 chat。
- 当前不并发执行不同目标；只有吞吐数据证明需要时才引入按目标分区的 worker。
- 领取任务时进入 `processing`；单实例进程重启时将未完成任务恢复为 `pending`。
- 执行成功后删除任务，失败任务保留最后错误用于排查。
- 相册按 `grouped_id` 在短时间窗口内聚合，再作为一个任务提交。
- 编辑或删除早于 create 完成时，延后该任务，而不是直接丢弃。

SQLite 单实例部署可以使用事务更新完成任务领取。未来切换 PostgreSQL 时，可改为 `FOR UPDATE SKIP LOCKED`，业务服务无需变化。

### 8.5 限流和重试

限流属于 Telegram gateway，因为所有功能共享同一个 Telegram 账号和 API 配额。

- 每个目标会话独立限速，同时设置账号级并发上限。
- `FloodWait` 使用服务端要求的等待时间，不做忙循环。
- 网络超时和临时 RPC 错误使用有上限的指数退避。
- 权限错误、无效目标、受保护内容等永久错误不自动无限重试。
- 日志中记录 route、source、destination、attempt 和错误分类，但不记录 session 或敏感正文。

### 8.6 循环防护

同时采用以下规则：

1. 忽略 userbot 自己发出的 outgoing update，除非某功能显式订阅它。
2. 发送成功后持久化目标 `MessageRef`，再次收到时能够识别为系统生成消息。
3. 创建或更新路由时检查直接自环。
4. 将路由视为有向图并拒绝形成环的配置；是否允许无环的多跳转发必须由显式配置决定。

## 9. 配置与密钥

使用 `pydantic-settings` 从环境变量读取配置，并在启动前完成校验：

```text
YUKIBOT_TELEGRAM_API_ID
YUKIBOT_TELEGRAM_API_HASH
YUKIBOT_TELEGRAM_SESSION_PATH
YUKIBOT_DATABASE_URL
YUKIBOT_LOG_LEVEL
YUKIBOT_FORWARDER_ALBUM_DELAY
```

规则：

- `.env` 仅用于本地开发，不能提交；仓库只提交 `.env.example`。
- `.session` 文件、API hash 和登录验证码不得进入日志或版本控制。
- 动态路由存数据库，环境变量只保存部署级静态配置。
- 配置对象在进程启动后视为只读。

## 10. 持久化与迁移

首个版本推荐 SQLite WAL 模式。它足以支持单进程 userbot，运维成本最低。

可选择 SQLAlchemy 2 async + Alembic，也可以使用 `aiosqlite` 和按版本执行的 SQL 文件。为了减少依赖，第一版建议 `aiosqlite` 加功能自有迁移文件；如果很快需要 PostgreSQL，再切换 SQLAlchemy/Alembic。

迁移规则：

- 每个功能维护自己的迁移目录和表前缀；
- 应用启动时先迁移，成功后再连接 Telegram；
- 迁移只能向前执行，生产数据库变更前先备份；
- 功能卸载默认不删除其数据，数据清理由显式运维命令完成。

## 11. 启动与关闭生命周期

```text
1. 加载并校验 Settings
2. 初始化结构化日志
3. 打开数据库并执行迁移
4. 创建 EventBus 和 Task Supervisor
5. 连接并授权 Telethon，加载初始 peer
6. 创建 gateway，构造并注册 features
7. 恢复中断任务并启动 feature worker
8. 注册并启动 Telethon event source
9. 等待 SIGINT / SIGTERM
10. 停止接收新 update
11. 等待正在执行的 update handler 和任务到达关闭超时
12. 停止 features、关闭 Telethon 和数据库
```

长期任务统一交给 `TaskSupervisor` 持有，critical worker 失败会触发应用关闭。启动中任一步失败，
`LifecycleManager` 都会逆序释放已经创建的资源。

## 12. 管理命令

管理命令是普通消息数据面之外的带外控制信令。Telegram update 规范化后必须先经过控制面；已被
识别和消费的命令不得再发布为 `TelegramMessageReceived`，避免被转发、归档或触发其他普通功能。
当前登录账号发出的已注册命令在任何聊天中都可以进入控制面，不额外判断聊天是否为收藏夹；命令
响应作为对原消息的回复发送到同一聊天。额外管理员通过稳定的 Telegram user ID 记录在 SQLite，
只有数据库中的委派管理员可以从收到的消息调用控制面。当前登录账号由已授权 session 的 `get_me()`
确定，不依赖环境变量或联系人备注。

```text
Telegram update
    -> normalize
    -> CommandRouter
         |-- registered command -> module command handler
         `-- ordinary message   -> EventBus
```

功能只注册第一个命令参数及其 handler，例如 Forwarder 注册 `/route`，另一个功能可以注册
`/other`。框架仅从消息开头提取这个已注册的一级命令，将其后的内容保持原样交给对应功能；
子命令、参数语法和业务校验完全由功能负责。框架不对命令名称做大小写归一化或额外字符集限制，
注册和匹配使用功能提供的原始名称。

框架唯一直接实现的业务可见命令是 `/help`，用于列出当前已注册命令或显示某个一级命令的帮助。
`/admin` 属于独立的常驻 Management 功能，负责委派管理员和可管理模块的期望启用状态；它本身不
纳入运行时模块开关，避免关闭后失去管理入口。Forwarder 自行拥有 `/route` 及其参数解析和管理
service，停用 Forwarder 时同时注销 `/route`，重新启用时恢复注册。

控制面必须遵守以下约束：

1. 只把消息开头的 `/command` 识别为控制命令。
2. 框架只拆分第一个 token，剩余参数原样传给模块。
3. 已注册的一级命令一旦匹配就始终被消费；即使参数错误，也不能回落到普通 `EventBus`。
4. 未注册的 `/command` 按普通消息处理，不能被控制面截获。
5. 同一个一级命令只能注册一次，冲突必须在模块启动或注册阶段立即失败。
6. 框架统一完成调用者身份验证和全局管理员检查，模块继续负责自己的业务权限校验。
7. 框架统一捕获 handler 异常，并将其转换为控制面响应和结构化日志。
8. 只执行首次收到的新命令消息；编辑已有命令消息不能再次触发执行。
9. 使用 `(chat_id, message_id)` 标识和去重命令，防止 Telethon catch-up 或 update 重放造成重复执行。

委派管理员列表、模块期望状态和命令回执均持久化到 SQLite。当前账号和委派管理员都可以执行
全部管理及功能命令，包括添加或删除其他委派管理员；当前登录账号本身始终有效且不能被删除。
模块开关使用明确的 `enable` / `disable` 期望状态，进程重启后继续生效。

命令处理必须无状态，或者对同一个命令重复执行保持幂等。禁止依赖未持久化的多轮命令会话状态；
会改变配置的命令应使用明确目标和期望状态，例如 `enable`、`disable`、按稳定 ID 更新或删除。
控制面的消息去重是额外保护，不能代替模块自身的幂等设计。

命令 handler 只能调用本功能的管理 service，不能绕过 service 直接写数据库。未来增加 CLI、HTTP
或 AstrBot 管理入口时，它们应复用同一个管理 service，而不是复用或模拟 Telegram 命令文本。

## 13. 可观测性

第一版使用标准库 `logging` 输出 JSON 或键值结构化日志，不必为了日志引入大型框架。

所有事件和任务至少包含：

```text
event_type
feature
operation
chat_id
message_id
route_id
job_id
duration_ms
attempt
result
error_type
```

健康状态至少包括：Telegram 是否连接、数据库是否可用、pending/failed job 数量和最近一次成功转发时间。单进程部署可以先通过 `/status` 和日志暴露，不需要立即启动 Web 服务。

## 14. 测试策略

### 14.1 单元测试

业务服务使用内存仓储和 fake Telegram port，不启动 Telethon：

- 路由匹配和过滤；
- 幂等处理；
- 回复映射；
- 循环检测；
- 重试分类；
- 编辑和删除的状态转换。

### 14.2 集成测试

使用临时 SQLite 数据库验证：

- migrations；
- repository；
- job 去重、相册批量领取、重试和恢复；
- 唯一约束和事务行为。

### 14.3 契约测试

针对 Telethon gateway 建立契约测试，确保它持续满足各功能本地 Protocol。真实 Telegram 账号测试单独标记为 `live`，默认测试套件不执行。

测试目录按测试类型组织，但文件名映射到功能，例如：

```text
tests/unit/features/forwarder/test_service.py
tests/integration/features/forwarder/test_repository.py
tests/contract/features/forwarder/test_telethon_gateway.py
```

## 15. uv 与工程工具

建议依赖：

```text
运行依赖
- telethon
- pydantic-settings
- aiosqlite

开发依赖
- pytest
- pytest-asyncio
- ruff
- mypy
```

常用命令：

```bash
uv init --package
uv add telethon pydantic-settings aiosqlite
uv add --dev pytest pytest-asyncio ruff mypy
uv lock
uv sync --frozen
uv run yukibot
uv run pytest
uv run ruff check .
uv run ruff format --check .
uv run mypy src
```

Telethon 使用 PyPI 发布的稳定版，并限制在已经验证的次版本范围内：

```bash
uv add "telethon>=1.44,<1.45"
```

`pyproject.toml` 声明兼容范围，`uv.lock` 提交到仓库，以获得可重复部署。代码和文档必须针对同一个 Telethon 主版本。

## 16. 新增功能流程

以新增 `keyword_alert` 为例：

1. 创建 `features/keyword_alert/`。
2. 在包内定义规则模型、仓储 Protocol 和它需要的 Telegram Protocol。
3. 订阅 `TelegramMessageReceived`，不修改 forwarder。
4. 创建带 `keyword_alert_` 前缀的表和迁移。
5. 在 `bootstrap.py` 注入 gateway、repository 和 event bus。
6. 增加单元测试与仓储集成测试。
7. 只有需要让其他功能消费结果时，才新增公开集成事件。

完成上述步骤时，已有功能的代码不应发生变化；通常只允许修改组合根和配置定义。

## 17. 强制边界规则

以下规则应在代码评审中执行：

1. `features/x` 不得 import `features/y` 的实现。
2. 功能的模型、服务、handler 和 worker 不得 import `telethon`、`aiosqlite` 或具体 adapter；最外层 repository 只能依赖数据库契约。
3. 功能不得读取或修改其他功能的表。
4. Telethon 对象不得出现在应用事件和业务模型中。
5. 所有后台任务必须有所有者、关闭策略和异常处理。
6. 所有外部副作用必须通过端口执行并可在测试中替换。
7. 不创建全局可变 client、database session 或 repository。
8. 不添加“万能 utils”；代码应放到拥有该概念的模块中。
9. 只有至少两个功能出现相同且语义稳定的需求后，才抽取共享代码。
10. 新增框架或中间件必须解决已经出现的问题，而不是假设中的扩展需求。

可以增加一个简单的 import 边界测试，扫描 AST 或使用 `import-linter`，自动阻止功能间违规依赖。第一版优先用测试实现，避免增加运行时复杂度。

## 18. 演进路线

### 阶段一：可用骨架

- uv 工程、配置、日志和 SQLite；
- Telethon adapter；
- EventBus 和显式功能注册；
- 单路由文字消息转发。

### 阶段二：可靠转发（主体已实现）

- 路由管理；
- ForwardJob、MessageLink 和启动恢复；
- 媒体、相册、回复、编辑和删除；
- 限流、重试、重放幂等和恢复。

### 阶段三：验证扩展性

- 添加第二个真实功能；
- 根据实际重复点提取共享契约；
- 加强边界测试和运行状态统计。

### 阶段四：按压力演进

- SQLite 写入或并发成为瓶颈时切换 PostgreSQL；
- 单进程吞吐不足时再引入外部队列；
- 只有多个功能需要远程管理时再增加 HTTP 管理接口。

这些变化都应发生在 adapter 或组合根，业务功能的核心逻辑保持不变。

## 19. 架构决策摘要

| 决策 | 选择 | 理由 |
|---|---|---|
| 部署形态 | 模块化单体 | userbot 规模下简单可靠 |
| 并发模型 | asyncio | 与 Telethon 原生模型一致 |
| 功能接入 | 显式注册 | 清晰、可调试、无扫描魔法 |
| 功能通信 | 不可变应用事件 | 避免直接依赖实现 |
| 外部能力 | 功能本地 Protocol | 接口小且按需演进 |
| 初始存储 | SQLite WAL | 单实例下运维成本最低 |
| 可靠任务 | 功能自有 job 表 | 明确数据和恢复责任 |
| 依赖注入 | 手工组合根 | 不引入 DI 框架和 Service Locator |
| Telegram SDK | 仅 Telethon | 避免双客户端和双会话模型 |
| 包管理 | uv + 提交 uv.lock | 快速且可重复构建 |

这套架构的最小稳定核心只有生命周期、事件分发和外部事件契约。功能以纵向切片形式独立拥有业务和数据；基础设施通过小型结构化 Protocol 接入。它不会消除所有依赖，但会让依赖保持单向、显式、可替换和可测试。
