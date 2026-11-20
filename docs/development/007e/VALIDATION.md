# 007-E1 复跑

在 Linux 上用项目的 Go 工具链，从工作树根目录运行下列命令。Python 3 只用于复跑脚本核对 JSON。默认在临时目录生成合成账本与缓存，复跑结束删除临时输入。显式输出目录中保留 JSON、日志和退出码。

```bash
scripts/development/trial-007e.sh /tmp/007e-review
go -C backend test -race ./internal/cachetrial ./internal/adapters/fs/diskcache
go -C backend test ./internal/preview ./internal/previewwarmup ./internal/cacheledger ./internal/adapters/fs/diskcache ./internal/database/sql
```

需要 C 的移动复验时，使用已有前端 embed，或在干净检出里放一个仅供测试的忽略文件 `backend/internal/web/embed/placeholder.txt`。进程内 handler transport 使用合成文件。

```bash
MUMENT_MOVE_HANDLER_TRANSPORT=1 MUMENT_MOVE_IMAGE_COUNT=1000 go -C backend test ./internal/web -run '^TestLedgerMoveHTTP$' -count=1 -v
```

独立命令从指定备份和缓存父目录生成 JSON。此例仍应使用合成输入。

```bash
go -C backend build -o /tmp/preview-cache-trial ./cmd/preview-cache-trial
/tmp/preview-cache-trial -snapshot "$fixture/snapshot.sqlite" -cache-dir "$fixture/cache" \
  -max-entries 2048 -max-bytes 268435456 -max-time 3s -samples 12 > /tmp/007e-report.json
```

继续时将上一份输出的 `snapshot_id`、`ledger.next_result`、`inventory.next_cache` 分别传给 `-expect-snapshot`、`-after-result`、`-after-cache`。目录游标 `done` 可继续传递。若需要在完整账本覆盖下继续目录，可将 `-after-result` 留为 0，以重新读出完整的小账本。各页 `window_complete` 表示本次读到范围末尾，`coverage_from_start` 表示从头且无覆盖缺口。账本另报 `result_key_coverage_from_start`，说明快照内的结果键是否完整覆盖。账本访问缓存时遇到符号链接、权限不足、挂载边界或无法读取，即使目录游标已是 `done`，也会使当前报告部分完成。明确缺失和大小不一致仍是已观测状态。快照或目录发生变化时重新从头试算。

| 退出码 | 含义 |
| --- | --- |
| 0 | 请求的窗口读至末尾，无新增覆盖缺口。续页的结果仍只代表其窗口。 |
| 1 | 输入、快照、数据库查询或输出错误。 |
| 2 | 条目、字节、时间预算或覆盖缺口导致报告部分完成。命令行参数语法错误也由 Go 标准标志解析器以 2 返回，错误信息见 stderr。 |
| 130 | 收到 SIGINT 或 SIGTERM。进入遍历后可输出至最后已纳入项的游标。 |

合成用例逐文件核对试算前后快照、仍打开的 WAL 数据库及缓存文件的内容、权限、mtime 和 ctime。分页用例走完同一目录的 1,280 个文件和账本的 1,280 行，通过查询计划验证 rowid、引用和移动索引，核对预算、取消、快照标识及目录变化拒绝续跑。发布结果共享同键或硬链接、大小一致但摘要不同、未经过宽限、最后引用仍存在的对象均不能进入估算。
