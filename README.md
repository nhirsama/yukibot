# yukibot

yukibot 是一个基于 Python 3.12、Telethon 1.44 和 SQLite 的模块化 Telegram userbot。
当前实现包含：

- 与 Telegram 无关的事件总线、任务监管、生命周期和关闭协调；
- 不可变 Telegram/数据库契约；
- 环境配置和 JSON 结构化日志；
- SQLite 事务、按功能迁移、Forwarder 短期持久任务与崩溃恢复；
- 可排空的 Telethon event source 和 Forwarder 自有 gateway；
- 显式组合根与 `yukibot` CLI；
- 带外管理命令、SQLite 管理员与运行时模块开关；
- Forwarder 功能及其框架接入层和路由管理命令；
- 独立的 Summarizer 功能、结构化模型适配器和总结规则。

整体边界见 [`docs/architecture.md`](docs/architecture.md)，各功能说明见
[`src/yukibot/features/forwarder/README.md`](src/yukibot/features/forwarder/README.md) 和
[`src/yukibot/features/summarizer/README.md`](src/yukibot/features/summarizer/README.md)。

当前 Go 运行时的统一消息队列、订阅分发和单一鉴权边界见
[`docs/message-stream.md`](docs/message-stream.md)。实时更新和历史轮询共用入口队列，只有实时控制命令
在分发端经过一次鉴权；历史文本不会被当成控制命令执行。

## Run

```bash
cp .env.example .env
# 填写 Telegram API ID 和 API hash
uv sync --frozen
uv run yukibot
```

首次启动且 session 尚未登录时，Telethon 会执行交互式登录。登录完成后，当前账号可以在任意
聊天中发送已注册命令，结果会回复到同一聊天的原消息。命令是普通消息处理之外的带外控制信令，
不会进入 Forwarder；未注册的 `/xxx` 仍按普通消息处理。

## Docker 部署

`docker-compose.yml` 默认使用 `ghcr.io/nhirsama/yukibot:latest`，并将其同目录下的 `./data`
挂载到容器的 `/app/data`。应用不需要开放入站端口，但服务器必须能通过 HTTPS 访问 Telegram、
GHCR 和配置的模型 API。

```bash
cp .env.example .env
# 填写 Telegram API ID 和 API hash
chmod 600 .env
mkdir -p data
chmod 700 data

docker compose pull
docker compose run --rm yukibot
# 完成交互登录后按 Ctrl+C，再转为后台运行
docker compose up -d
docker compose logs -f yukibot
```

容器启动时只以 root 修正 `./data` 的属主和权限，然后以 UID/GID `10001` 运行应用。
目录会设为 `0700`，其中的 session、SQLite 和备份文件会设为 `0600`；宿主机上看到这些文件
属于 UID `10001` 是预期行为。不要在 Compose 中覆盖 `user`，否则入口无法修复新建绑定目录的权限。

升级使用 `docker compose pull && docker compose up -d`。该项目只能运行一个实例；备份时先执行
`docker compose stop`，完整备份 `./data`，再执行 `docker compose start`。

Forwarder 只在本地保留等待、重试和失败的任务，成功后立即删除任务内的消息文本和元数据。
升级时数据库迁移会删除旧版本累积的成功任务；SQLite 会复用释放的页，但不会立即缩小宿主机上的文件。
需要立即回收磁盘空间时，在迁移成功后停服并执行一次：

```bash
docker compose stop yukibot
docker compose run --rm --user 10001:10001 --entrypoint python yukibot -c \
  'import sqlite3; db = sqlite3.connect("/app/data/yukibot.db"); db.execute("VACUUM"); db.close()'
docker compose start yukibot
```

`forwarder_message_links` 只保存源和目标的消息 ID、路由 ID 及实际投递方式，不保存正文或媒体。
该映射用于同步编辑和删除、保持回复链以及防止重复投递，Telegram 不会为复制转发提供这层映射，
因此这部分仍需持久化。

GitHub Actions 会在每次 push 和 pull request 时执行 Ruff、格式检查、Mypy 和全部测试。默认分支
通过后发布 `ghcr.io/<owner>/<repository>:latest`，`v*` tag 还会生成对应的版本标签。GHCR 包首次
发布后需要在 GitHub Packages 设置中确认服务器所需的可见性；私有包部署前需先执行
`docker login ghcr.io`。

框架直接提供：

```text
/help
/help /route
/help /admin
```

独立管理模块提供：

```text
/admin admin list
/admin admin add <telegram_user_id>
/admin admin remove <telegram_user_id>
/admin module list
/admin module enable forwarder
/admin module disable forwarder
/admin module enable summarizer
/admin module disable summarizer
```

当前登录账号和已登记管理员都可以执行全部管理及功能命令，包括增删其他委派管理员。当前登录
账号本身不能从管理员体系中删除。额外管理员使用稳定的 Telegram user ID 存储在 SQLite 中。
管理模块本身始终保持可用，不属于可关闭模块。

Go 运行时按登录账号的稳定 user ID 识别 owner，不依赖消息一定带有 `outgoing` 标志。
收到的私聊消息省略 `from_id` 时，会用该私聊的用户 peer 识别发送者；不会用群组、频道、
转发来源或发出消息的收件人推断管理员。这里的 admin 指通过 `/admin admin add` 登记的
yukibot 管理员，不会因为某人是 Telegram 群管理员而自动授权。匿名身份或“以频道身份发送”
无法确认个人 user ID 时，请改用个人身份私聊登录账号。

遇到 `Permission denied.` 时，命令尚未进入路由添加逻辑，这与源频道或目标群的发言权限不同。
修复身份问题或登记管理员后，请发送一条新命令；编辑旧命令不会执行，旧消息的处理回执也不会被重放。

Forwarder 提供：

```text
/route list
/route show <id>
/route add <source> <destination> [forward|copy] [--poll <间隔>]
/route set <id> <source> <destination> [forward|copy] [--poll <间隔>]
/route enable <id>
/route disable <id>
/route remove <id>
/route check
/route rebuild
/route rebuild --all
/route rebuild status
/route rebuild cancel
```

例如：

```text
/route add @source_channel -1009876543210
/route add https://t.me/+source_hash https://t.me/+destination_hash
```

`source` 和 `destination` 都可以使用数字 ID、`@username`、公开链接
`https://t.me/<username>`，以及私有邀请链接 `https://t.me/+<hash>`、
`https://t.me/joinchat/<hash>` 或 `tg://join?invite=<hash>`。使用私有邀请链接时，yukibot 会先检查
当前账号是否已经加入；未加入时会通过该链接加入，再将稳定 ID 写入路由。需要管理员审批的群组会
提示先等待审批，通过后重新执行命令。成功创建或更新路由后，使用过的邀请链接会保存为换号重建的
兜底信息；路由列表优先显示用户名。默认实时模式也会幂等地加入尚未加入的公开源频道。通过数字 ID、
用户名或公开链接配置目标群时，账号仍须已经加入目标群并拥有发消息所需的权限。

路由默认使用 Telegram 原生转发；来源禁止转发或当前操作无法原生转发时自动回退为复制。目标是
论坛超级群且没有指定 `destination_topic` 时，yukibot 会创建并保存自动话题；源是超级群组内部话题时
使用“群组名/话题名”，未指定内部话题时使用群组名；
此后只使用持久化的 `topic_id` 定位，源频道改名后再通过明确的改名事件同步话题标题。临时缺失的
频道名称不会覆盖已有标题。相同“源群组/源话题 -> 目标论坛群”的路由复用同一个自动话题，不同
源话题使用各自独立的自动话题。
账号需要在目标群拥有创建和管理话题的权限。

要使用已有话题，在 source 或 destination 引用末尾添加 `/话题ID`。数字 ID、用户名和公开链接均
支持该格式：

```text
/route add -1001234567890/546 -1009876543210 forward
/route add @source_group/546 @target_group/12345
/route add https://t.me/c/3953295839/546 https://t.me/target_group/12345
```

对于不希望账号加入的公开源频道，可以指定轮询间隔：

```text
/route add @public_source -1009876543210 --poll 5m
```

间隔支持分钟、小时和天，例如 `5m`、`2h`、`1d`；不带单位的数字按分钟处理。轮询模式不会自动
加入源频道，只适用于当前账号可以公开读取的频道，因此轮询源不能使用私有邀请链接。首次配置会把
游标定位到频道当前最新消息，
只转发之后出现的新消息，不回灌已有历史。游标在消息进入持久任务队列后推进并保存到 SQLite，
重启后继续拉取。轮询模式不接收 Telegram 实时更新，因此不会同步已拉取消息之后发生的编辑和删除。

目标不是论坛群时，目标引用不带话题 ID 表示直接发送到该群。显式使用 `copy` 可以始终复制
消息内容，不保留 Telegram 的“转发自”标记。

动态转发路由保存在 `forwarder_routes` 表中。`add` 由数据库自动分配路由 ID，重复添加相同配置
返回已有路由，不产生重复转发；`enable`、`disable` 和 `remove` 是幂等的，修改使用 `set <id>`。

切换 Telegram 账号前，建议先执行 `/route check`。该命令会检查路由涉及的频道和群组，并更新
数据库中的频道名称、公开 `https://t.me/<username>` 链接，以及当前账号能够读取到的已有私有邀请
链接。它只读取现有链接，不会创建新邀请；如果当前无法读到新链接，配置路由时记录的私有邀请链接
会继续保留。切换 session 并使用新账号登录后，再执行
`/route rebuild`；程序会跳过已加入聊天和轮询源，只重建启用路由需要的实时源与目标。
`/route rebuild --all` 还会包含停用路由。

加群操作严格串行，默认每次尝试随机间隔 5 至 10 分钟。缺少用户名或邀请链接的频道不会进入
队列，而会直接出现在命令回复中。重建进度仅保存在当前进程内，不写入数据库；进程重启后需要
重新执行命令。间隔可通过 `YUKIBOT_REBUILD_JOIN_MIN_INTERVAL` 和
`YUKIBOT_REBUILD_JOIN_MAX_INTERVAL` 调整，其中最小值不能低于 300 秒。
当前登录账号和数据库中的委派管理员都可以执行这些命令。

Summarizer 独立提供：

```text
/summary list
/summary show <id>
/summary add <source> <destination> [30m|6h|1d]
/summary set <id> <source> <destination> [30m|6h|1d]
/summary run <id> [30m|6h|1d]
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

例如，将公开频道最近一天的内容总结到论坛群话题：

```text
/summary add @source_channel -1001234567890/42 1d
/summary run 1
```

目标可以是私聊、频道、群组或论坛话题。论坛话题支持
`-1001234567890/42`、`https://t.me/c/1234567890/42` 和
`https://t.me/public_group/42`。模型、API 密钥和推理参数通过 `/summary model` 命令管理，保存在
Summarizer 自己的业务配置表中；命令输出不会回显 API 密钥。每次运行会读取完整时间窗，不设置
消息条数硬上限；map 分块和同一 reduce 层的独立分组会按 `concurrency` 并发处理。提示词可以选择
内置预设或持久化自定义偏好。详细配置、消息归一化和 map/reduce 行为见 Summarizer 功能说明。

APIArc 通过通用 OpenAI Responses 接口配置，使用官方模型 ID：

```text
/summary model set openai deepseek-v4-flash-free -api-key api-key -base-url https://apiarc.ai/v1
```

Forwarder handler 只将事件幂等写入 `forwarder_jobs`，由单个受监管 worker 按任务顺序发送。
任务成功后会从队列删除；进程中断时，处于 `processing` 的任务会在下次启动恢复为 `pending`。
严格 exactly-once 仍受
Telegram 发送与本地映射落库之间无法建立跨系统事务的限制。

## Development

```bash
uv sync --dev
uv run pytest
uv run ruff check .
uv run ruff format --check .
uv run mypy src
```

## License

Copyright (C) 2026 [nhirsama](https://github.com/nhirsama).

yukibot 源代码采用 [GNU Affero General Public License v3.0 or later](LICENSE)
（`AGPL-3.0-or-later`）授权。项目源码位于
[github.com/nhirsama/yukibot](https://github.com/nhirsama/yukibot)。第三方依赖和基础镜像组件
继续适用各自的许可证与版权声明。
