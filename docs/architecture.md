# yukibot 架构

当前唯一运行时为 Go，组合根在 `internal/bootstrap`，入口在 `cmd/yukibot`。
领域功能通过端口与适配器连接 gotd 和 PostgreSQL，不再运行 Telethon、SQLite 或 Python 应用。

- **整体架构**：参阅 [`architecture-go.md`](architecture-go.md)。
- **消息与鉴权**：参阅 [`message-stream.md`](message-stream.md)，包括统一入口队列和单一命令授权边界。
- **功能实现**：参阅 [`features/forwarder.md`](features/forwarder.md) 和 [`features/summarizer.md`](features/summarizer.md)。
- **Python 退役与兼容边界**：参阅 [`python-retirement.md`](python-retirement.md)，包括模块对应、冻结基准及离线导入工具。

旧 Python 架构文档保存在 Git 历史 `e7f7d36:docs/architecture.md`，仅作历史参考。
不得再按旧文档启动 Python 或把旧 Telethon 会话当作 gotd 会话使用。
