# Python 行为基准

`python312.json` 在删除原实现之前，直接执行 Python oracle 捕获，未使用 Go 输出生成预期值。
当前 Go 测试直接嵌入该文件，不再运行 Python，也不需要 `parity` build tag。

- **参照提交**：`e7f7d36b9b0393daf7d4f554776355acb981d9f7`。
- **Python 生产源码树**：Git tree `8581063395394e7cbd2b10657eed722fc14aa4c1`，路径 `src/yukibot`；与上一轮保留的源码相同。
- **生成环境**：Python 3.12.13，Unicode 15.0.0；该提交的 `uv.lock`。
- **生成命令**：`uv run python tests/parity/oracle.py > tests/parity/testdata/python312.json`，在上述历史提交执行。
- **SHA-256**：`933974a06c91b0357c3aeebdfb4e9cd0b5285224807077acff2d556e6675c15f`。
- **数量**：1113 个用例。包括 48 个完整 map/reduce 提示词组合、14 个命令切分输入、转发过滤/话题/任务键/引用解析，以及一个穷尽合法 Unicode 标量的折叠表对照。

这些是可追溯的关键行为基准，不是“所有外部服务、所有输入完全等价”的证明。
若需要增加对照，另建历史工作区运行 oracle；不要为了让新实现通过而把 Go 实际输出覆盖为预期值。
