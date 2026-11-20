# 007-D RC1 验证记录

2026-11-19，Asia/Shanghai。候选来自 `feature/007d-idle-preview-worker`。基线为 `dadd3c5d9ff060f5acbfaaa5facf2f3f82af4234`，文件树与 C 的 `9beb9ea39bec45a56e0ff603ffe7cb5e14cef300` 相同。参考实验为 `758f6c2`。候选源码固定为 `95c26279539d827b83f6cce15435e25c1a6345b7`。后续提交记录验证和完善回放脚本。

本地候选为 `.validation/007d/mument-007d-rc1`，Go 1.27.3，Linux amd64，`CGO_ENABLED=0`，内嵌本分支的前端构建。SHA-256 为 `86ec3924a1692be757a1330ad7a240b00542c93464e2ff03799d41c867d769a7`。原始证据同目录下包含 `candidate.sha256`、`native-buildinfo` 和 `native-version.log`。构建记录的源码工作树干净。

## 实际执行

| 验证 | 退出码 | 结果与原始证据 |
| --- | --- | --- |
| 前端构建、独立二进制构建 | 0 / 0 | Node 22.23.0 和本地锁定依赖构建前端，Go 编译内嵌候选。见 `frontend-build.{log,exit}` 和 `native-build.{log,exit}`。 |
| 后端核心及设置 race | 0 | 预览、调度、账本、磁盘缓存、SQLite 和索引通过。设置包排除了需要 TCP 监听的 `TestHTTPListenAddr`。见 `race-core-final.{log,exit}`。 |
| HTTP 处理器和目录回归 race | 0 | 进程内 HTTP handler。用真实工作线程生成 PNG 的 `xlarge`，首次前台读取新增解码 0 次，`large` 仍按需生成。覆盖目录分页、预览状态和 C 移动场景。见 `race-web-final.{log,exit}`。 |
| C 的 1000 图同盘移动 | 0 | 用 handler transport 执行原有断言。16 组账本事务、1000 张逐图校验成功、重读新增解码 0 次、目录刷新 2 次。一次运行总计 84.35 秒，首次目标目录 GET 约 52.9 ms。见 `move-1000-final.{log,exit}`。 |
| 独立二进制监听 | 1 | 启动到 Unix socket 监听时，当前沙箱拒绝 `setsockopt`。见 `native-http.{log,exit}` 和 `native-http/default-server.log`。 |
| Docker 访问 | 1 | Docker daemon socket 拒绝访问。容器测量待跑。见 `docker-access.{log,exit}`。 |
| 回放脚本语法 | 0 | `python3 -m py_compile`。见 `script-syntax.exit`。 |

核心 race 测试验证了前台排队让步、同键加入和取消、旧文件在解码期间被替换、首次前台才更新 `last_used_at`，并拒绝 APNG 和超预算样本。合成目录守卫测试核对了兄弟目录、越界软链接、目录替换、挂载身份不匹配和恢复。压力测试使用可控的内存/CPU指标及空间门槛，确认损坏图退避和重启续跑。测试覆盖了已发布但缺进度标记和候选尚未生成两种恢复边界。

6,000 行 SQLite 夹具中，历史、近期、手动查询分别使用 `idx_warmup_path`、`idx_warmup_recent`、主键的有界 seek。三个计划均无临时排序树。持续更新近期候选的 64 轮里，历史路径游标继续前进。数值来自本机合成样本和进程内传输。

## 复跑

从 D 工作树执行。原生构建要求已有 Node 22 和 Go 1.27，后端候选的编译 SHA 从本页取得。

```bash
npm --prefix frontend ci
npm --prefix frontend run build
export GOMAXPROCS=2 TZ=Asia/Shanghai
go -C backend test -race ./internal/preview ./internal/previewwarmup ./internal/cacheledger ./internal/database/sql ./internal/adapters/fs/diskcache ./pkg/indexing ./pkg/settings -skip '^TestHTTPListenAddr$'
MUMENT_MOVE_HANDLER_TRANSPORT=1 go -C backend test -race ./internal/web -run '^(TestPreviewWarmup|TestLedger|TestResourceListing|TestListing|TestErrToStatusPreview)'
MUMENT_MOVE_HANDLER_TRANSPORT=1 MUMENT_MOVE_IMAGE_COUNT=1000 go -C backend test ./internal/web -run '^TestLedgerMoveHTTP$' -count=1 -v
python3 scripts/development/smoke-007d.py --binary .validation/007d/mument-007d-rc1 --output .validation/007d/native-replay --interrupt --compare
```

使用有 Docker 权限的验收机时，按固定源码构建隔离镜像。下面的临时占位文件用于 Docker 分阶段构建，最终镜像单独包含前端产物。

```bash
sha=95c26279539d827b83f6cce15435e25c1a6345b7
scratch=$(mktemp -d)
git archive "$sha" | tar -x -C "$scratch"
mkdir -p "$scratch/backend/internal/web/embed"
printf 'placeholder\n' > "$scratch/backend/internal/web/embed/placeholder.txt"
docker build -f "$scratch/_docker/Dockerfile.slim" \
  --build-arg VERSION=007d-rc1 --build-arg REVISION="$sha" \
  -t "mument:007d-rc1-${sha:0:7}" "$scratch"
python3 scripts/development/smoke-007d.py --image "mument:007d-rc1-${sha:0:7}" \
  --output .validation/007d/docker-replay --interrupt --compare
```

镜像回放固定为 2 CPU、1 GiB 内存、内存加 swap 共 2 GiB。脚本使用合成数据、临时挂载、独立 Unix socket 和无网络的容器。收集候选镜像 ID、前台等待、首屏时延、CPU 与 I/O、内存峰值、OOM、处理率和积压。脚本还提供实际中断后的续跑检查。真实二进制 HTTP、限额容器前后对照、真实卸载与重挂、手机马赛克均待验收。
