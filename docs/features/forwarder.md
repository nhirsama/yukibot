# Forwarder

Go 转发模块位于 `internal/features/forwarder`，核心服务不依赖 Telegram SDK 或具体数据库。
`infra` 将领域端口映射到 gotd，`store` 提供 PostgreSQL 仓储，`feature.go` 接入进程内消息订阅和持久任务。

## 保留的行为

- **路由**：chat/topic 匹配、源和目标解析、自动分配 ID、配置幂等复用、环检测、启停和显式更新。
- **过滤**：Python 3.12 Unicode casefold 关键词语义、内容类型白黑名单、服务消息开关；黑名单优先。
- **投递**：原生 forward、显式 copy、受限原生转发的 copy 回退，实际投递模式随消息映射持久化。
- **相册与回复**：相册排序后整体发送；保存逐项消息映射和回复关系；不补发部分成功的相册以避免重复。
- **编辑与删除**：copy 消息同步编辑，原生 forward 不调用编辑 API。服务支持同步删除，但当前应用组合根关闭删除同步；缺少来源 chat 的删除默认拒绝模糊匹配。
- **自动话题**：按源 chat/topic 和目标论坛复用持久话题；创建重试使用稳定 random ID；只对明确改名事件更新标题。
- **历史轮询**：首次定位到当前最新消息，不回灌旧历史；新增消息进入同一持久任务链路，成功入队后才推进游标。轮询不追踪历史编辑或删除。
- **换号重建**：按路由检查会话、保存访问元数据和邀请链接；重建串行执行并保留最小加群间隔。

## 运行链路

```text
gotd 实时事件 / SourcePoller 历史事件
  -> 统一有界 MessageStream
  -> 控制订阅者（唯一命令鉴权）/ 功能订阅者
  -> ForwarderFeature
  -> PostgreSQL forwarder_jobs
  -> 单 worker
  -> ForwarderService
  -> gotd gateway + 消息映射仓储
```

`Forwarder` facade 仍可作为独立内存相册缓冲器使用；应用运行时采用持久任务路径。
成功任务删除，永久错误或重试耗尽保留为 failed；启动恢复 processing 任务。同进程重复投递使用路由级锁，重放先检查持久消息映射。

Telegram 发送和数据库写入不能构成原子事务，发送成功、映射未落库时的崩溃仍可能造成重复。
Go copy 复用重新读取源消息得到的文件引用，不沿用 Python 的下载再上传实现；受限媒体和失效引用属于仍需真实账号验证的适配器边界。

命令与示例见根目录 README；统一鉴权和队列边界见 `docs/message-stream.md`。
退役前实现及对应测试保存在 Git 历史，不再维护第二个 Python 运行时。
