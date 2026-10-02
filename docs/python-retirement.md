# Python 运行时退役

按照最新维护决定，退役不再以 100% 单元覆盖率为门槛。评估依据是实际 Go 运行链路、关键行为对照、现有回归测试和可追踪的模块级 Git 提交。
这里的目标是移除重复运行时，不借机改变已上线 Go 的权限、消息分发或数据删除策略。

## 行为证据与追溯

删除前，已执行原 Python 的 220 项全量测试，并验证 Go 的持久队列、管理命令、路由、摘要及 PostgreSQL 回归测试。
Python oracle 的 1113 个关键行为对照已冻结为 `tests/parity/testdata/python312.json`，原始提交、源码树和 SHA-256 见同目录 README；正常 `go test ./...` 即执行，无需 Python。

对照不是全输入穷尽证明。Telegram 真实账号、媒体兼容性、生产故障恢复和部署环境仍需运行验收，不能从纯函数用例推断都已实测。
已经发现的两处差异先修复再退役：摘要从总时限改为连续无数据超时；命令空白分隔符与 Python 对齐。上一轮 Unicode 关键词/摘要去重修复一并保留。

## 模块对应关系

| 原 Python 模块 | 当前 Go 实现 | 本轮核对重点 |
|---|---|---|
| `features/summarizer` | `internal/features/summarizer`，含 infra/store | 命令、规则/模型持久化、完整提示词、map/reduce、证据过滤、分块与并发、SSE 请求/失败/超时 |
| `features/management` | `internal/features/management`，含 store | owner/委派管理员、模块开关、回执作用域、幂等增删 |
| `features/forwarder` | `internal/features/forwarder`，含 infra/store | 路由、过滤、环检测、相册、回复映射、复制回退、编辑、删除能力、话题、轮询、重建和重试 |
| `adapters/telegram` | `internal/adapters/telegram` | 消息归一化、身份识别、peer、限流、事件源和回复 |
| `adapters/database` | `internal/adapters/database`、`internal/storage/dbsql` | PostgreSQL 事务、校验和迁移、回滚、仓储往返、队列恢复 |
| `adapters/observability` | `internal/observability` | JSON 日志、敏感信息脱敏及并发安全 |
| `kernel` | `internal/kernel` | 生命周期、事件订阅、任务监管、停机、模块状态、命令分发 |
| `contracts` | `internal/contracts` | 消息及删除事件校验、集成契约 |
| `bootstrap/config/__main__` | `internal/bootstrap`、`internal/config`、`cmd/yukibot` | 单一 Go 入口、显式依赖装配、配置和进程生命周期 |

## 保留的差异与运行边界

- **鉴权和消息流**：沿用已合并的 owner/admin 修复、单一命令授权和统一有界消息队列，不恢复旧 Python 的重复鉴权或错误身份判断。
- **删除同步**：业务服务具备与旧实现相同的同步删除能力，但当前 Go 组合根显式关闭。本次源码清理不启用它，不会因为移除 Python 而开始删除目标消息；是否恢复旧默认仍由维护者决定。
- **数据库和会话**：运行时只接受 PostgreSQL；gotd session 与 Telethon session 不是同一格式。不得覆盖旧会话或 SQLite 备份，也不能把旧库当成已自动迁移。
- **媒体复制实现**：Python 下载后重传，Go 使用重新读取源消息获得的 Telegram 文件引用；普通文本和媒体处理路径均存在，但受限媒体/失效引用的外部行为仍需真实账号验收。本次不声称这一差异经过线上对照验证。
- **模型接口**：Go 直接调用 Responses HTTP/SSE，不再使用 Python SDK；保留流大小限制和取消语义。SDK 自带的 HTTP 重试细节、错误类型/文案与 Go 不保证逐字一致，不影响已经对照的业务提示词，但仍是兼容性边界。
- **交付语义**：进程内入口队列不是持久消息中间件；发送成功但映射未落库的崩溃窗口仍可能重发。清理旧实现不会解决或扩大这些已有边界。

## 独立保留的数据导入工具

`scripts/import_sqlite.py` 仅依赖 Python 标准库和宿主机 `psql`，不导入 `yukibot`、Telethon、Pydantic 或 OpenAI SDK。
它是旧 SQLite 数据的一次性离线导入工具，不是第二套运行时，不被 Go 构建、启动、正常测试或容器调用。

保留此工具是为了避免删除运行时的同时切断数据迁移路径。导入会写目标 PostgreSQL，因此不在此次清理中自动执行；使用前必须停机、备份，并审查目标库和脚本校验条件。
旧数据已经完成迁移的部署无需安装 Python。

## 验证与回滚

```bash
# 默认运行 Go 测试及冻结基准，不访问真实 Telegram
go test -timeout=2m -race ./...

# 完整验证，只能指向可清理的一次性 PostgreSQL 测试库
export YUKIBOT_DATABASE_URL='postgres://USER:PASSWORD@localhost:5432/yukibot_test?sslmode=disable'
bash scripts/check-runtime.sh
```

模块移除使用独立提交。需要恢复源码时，从退役前提交 `e7f7d36` 建立独立历史工作区，而不是在当前 Go 分支重新启用残缺的 Python 模块。
Go 数据库变更和 Telegram 会话不应随 Git 回退自动覆盖；生产回滚仍需对应的备份和版本方案。
