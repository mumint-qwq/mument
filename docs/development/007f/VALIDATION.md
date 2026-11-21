# 007-F 验证记录与验收边界

2026-11-21 收口更新：最终修正候选 `fba5ba2` 已合入本地 main，实际 TCP 后端全量、限额 Docker、容器重建、完整运行镜像与手机操作后核验，以及最终候选独立 30 分钟混合回归均已补齐。当前结果、测量条件和仍未覆盖的组合见[007 状态](../007/STATUS.md)。下文保留原执行会话和首轮候选的历史事实，不把当时的阻塞或部分结果改写为全项通过。

本交付是独立本地候选，**不是 F01–F20 全项验收通过**。核心实现已固定；F4 仍有未执行项和环境阻塞项。未开启生产 F/D/E、未操作真实图库、未推送或合并。任务书记录的生产 B 未在本环境独立核验。

## 源码和环境

- E3 基线：`aaaefebaf29084dc22fc5f0b03cbaeb73aafdb59`，tree `809188ef3ed75864ab5be078d388201ea944be0b`。
- 当前修正功能冻结：`1e3952dd848182dcf7efa8822ee4771a338b2e75`，tree `2a5e66e7038b272bc0a11bf07444c08880393db4`；来源为对 `a0ab1e5` 的两项 P2 修正，详见末节。
- 首轮功能/测试冻结记录：`ea986fe712f51bb86478a93b10a98adb97ffde86`（主流程及故障回归），千图引用计数补充提交 `51fa6e89930350025d7d0f2ba2da3523052a9317`，tree `cac17921af41d1c9ed31f59b4534eaf2edb8aa98`。源先消失顺序补测 `5ecec4ec7b392b1370115bad74dc78579000bec1`，tree `1b7d21b2aa7e05fae538c4603d3df3a7fa583527`。后续交付文档提交不改变功能代码。
- 分支：`feature/007f-external-cache-reuse`；工作树：`custom/publish/worktrees/007f-external-cache-reuse`。
- Linux `6.12.117+deb13-amd64` / amd64，Go `1.27.3`；测试产物使用 `CGO_ENABLED=0`、`-trimpath`，race 检查另用默认 CGO 工具链。
- `GOMAXPROCS=2` 是 Go 运行参数，不能当作实际 2 CPU cgroup 限额证明。测试临时目录位于 tmpfs。真 EXDEV 使用两个独立私有 tmpfs 挂载；这些数据不能代表机械盘或生产磁盘吞吐。
- 当前沙箱禁止 TCP 监听；Docker socket 返回 permission denied。私有用户/挂载命名空间可用。未绕过 Docker 权限。
- web 编译使用明确标注的 synthetic embed 占位文件；它只服务后端测试，不是可发布的前端或生产镜像。

设计提交 `4c835ad`；F1 `89870eb`；F2 `96d209b`；F3 `0a3db09`；验证与修正依次为 `4b83913`、`47e18b4`、`7839410`、`ffe6dcd`。详见 [设计](DESIGN.md)、[配置与暂停/回退](OPERATIONS.md)、[五条代码走读](WALKTHROUGH.md)。

## 复跑入口和原始证据

```bash
export GO=/home/jh/docker/stacks/filebrowser-quantum/custom/tools/go1.27.3/bin/go
export GOPATH=/home/jh/docker/stacks/filebrowser-quantum/custom/tools/gopath
export GOCACHE=/home/jh/docker/stacks/filebrowser-quantum/custom/tools/gocache
bash scripts/development/verify-007f.sh /tmp/f-host-evidence
bash scripts/development/verify-007f-scale.sh /tmp/f-cross-scale
bash scripts/development/verify-007f-samedisk.sh /tmp/f-same-scale
bash scripts/development/verify-007e3.sh /tmp/f-e3-regression
bash scripts/development/verify-007f-mixed.sh /tmp/f-mixed-30m
# 有 Docker 权限的隔离环境；只使用唯一临时夹具和镜像/容器：
bash scripts/development/verify-007f-container.sh /tmp/f-host-evidence
```

[证据总入口](../../../../../reviews/007f-execution-20261120/README.md)保存最终候选 manifest、脚本、二进制 SHA-256、源码提交与 tree、逐阶段退出码和原始日志。`candidate-ea986fe/` 和 `candidate-51fa6e8/` 是首轮交付检查；`candidate-a351c2a/` 包含真实持久索引回退；`mixed-30m/` 是该轮源码的长时场景，未覆盖后来的变体选择/提示队列修正。`candidate-ffe6dcd/` 是前一轮；`run-1`、`run-2` 及较早 scale 目录保留开发过程，不能替代最终结果。修正候选的本轮记录在末节所链接的新目录。各阶段实际结果以 `.exit` 和 `SUMMARY.json` 为准；未执行项没有伪造为零的退出码。

`verify-007f.sh` 分别保留专项、核心全包 race、后端全量、真实 EXDEV 和四阶段回退的结果；任何失败会让总入口非零。原有 E3 脚本独立复跑。真实 TCP/断线行为与进程内 handler 测试严格区分：本环境使用后者，不能据此宣称真实监听已通过。

## 首轮交付 F01–F20 索引（a0ab1e5）

“通过”仅覆盖此处列出的断言；“部分”表示仍缺完整组合或性能证据，不能视为整个编号验收完成。专项原始日志见 host/targeted、supplement/targeted-race 和 late-checks；环境与性能入口另列。

| 编号 | 状态与实际执行 | 尚缺或限制 |
| --- | --- | --- |
| F01 | 通过：JPEG 外部 rename 的 small/xlarge，PNG 外部 rename 的 small/large/xlarge；原字节、epoch 保持，新增解码 0 | 合成 JPEG/PNG |
| F02 | 通过：旧源未证明即消失，不补写旧证明，按新文件处理；F 原图读取 0 | 大规模证明覆盖缺口统计未执行 |
| F03 | 通过：两个真实 tmpfs 挂载、实际 EXDEV、产品移动入口、成功提示、目标复用；暂停原删除 goroutine 时两位置 small/xlarge 都可读 | 无真实磁盘吞吐结论 |
| F04 | 部分：相同副本、源先消失后用已存证明匹配、真实跨挂载目标验证；source-first 补测退出 0 | 外部跨盘工具两种扫描顺序的完整产品组合未执行 |
| F05 | 部分：`TestContentAncestorRenamePreservesCWithoutOriginalRead` 保持 C 目录定位、F 原图读取 0、预览新增解码 0；批量移动/索引回归通过 | 跨盘目录与相邻目录名组合未完整执行 |
| F06 | 通过：同名/长度/mtime/首尾相同而中段不同拒绝；保留目标证明后重试读取 0；可控 inode 碰撞拒绝；同长度 EXIF 1→6 拒绝旧结果，新 xlarge 方向正确 | inode 重用是注入；EXIF 为合成有效 JPEG |
| F07 | 通过：副本共享、硬链接、删一份、ctime 换版再验证、独立改写与最后确认删除 | 大规模硬链接图未执行 |
| F08 | 通过：读中改写、截断、追加、取消、摘要后原子替换均无错误绑定 | 遵循 C/E 强版本契约，不保证特权伪造所有 stat 字段的不可区分编辑 |
| F09 | 部分：既有 root/fd/symlink/no-XDEV 守卫、持久根 pin、E3 私有重绑；混合场景实际卸载/错盘/原盘重挂 23 轮通过 | F 嵌套挂载组合未完整执行，真实容器重建阻塞 |
| F10 | 部分：EXDEV 覆盖旧目标与同批部分失败，仅成功项提示；原异步删除暂停、成功及返回权限错误均通过 | 复制中途短写未注入 |
| F11 | 部分：在途前台生成优先，已有发布保留；A/D 取消与并发回归，D/F 混合 30 分钟通过 | 不是所有前台取消与双访问的 F 组合，也没有配对基线延迟对照 |
| F12 | 部分：管理员严格接口、状态无路径/摘要；完整证明从私有原缓存共享到分享内目标，真实分享鉴权中间件允许读取并拒绝越界 | 进程内 HTTP transport；真实监听阻塞 |
| F13 | 部分：尚有引用不删，最后确认删除进入 E2 宽限并回收；E2/E3 意图阻止 F；实际 E3 隔离后按原协议归还，使用目标已有证明关联，重读原图 0 | 未穷举全部并发时序 |
| F14 | 部分：PNG 三变体、质量/格式/尺寸/fill 键隔离；禁止将曾发布或淘汰后的空资产当新 token；无后台热度更新与解码 | 全部冷却/隔离/历史无归属对象的 F 交叉试算未执行 |
| F15 | 部分：实际 SQLite busy 无部分证明；持久 pending、取消、CAS、有限重试、关闭等待；真实子进程退出 73 覆盖领取后、读完未提交、绑定后未清待办，独立进程恢复零新增解码 | 内核不可中断读未注入 |
| F16 | 通过：默认关闭，D 未建表时独立 F 发现/复用/重启去重；配置、暂停、浏览让步与恢复、scope 脱机及正确根回归 | 容器重建另列 F19 |
| F17 | 部分：基线/候选三轮同盘 1000 独立 JPEG；三轮真实跨挂载批量路径候选新增解码 0；补充轮次精确核对 1000 原引用保留 | 跨挂载为同内容的 1000 独立源缓存，仅 small；核心核验未限速、无后台调度/真实 HTTP/哈希期间前台配对延迟；独立千图外部扫描未执行 |
| F18 | 部分：4000 项普通 handler 和 8 页 GET 的 F 完整原图读取 0；跨 tick 浏览让步，结束后恢复；记录响应与首张耗时 | 不是真实 socket/UI；缺完整 I/O 峰值与快速浏览测量 |
| F19 | 部分/阻塞：核心全量/race、E3 及 A–E 关联入口实际执行；后端全量保留非零结果 | TCP 限制、基线 events 竞争；真实 Docker 2 CPU/1 GiB/1 GiB swap、OOM、重建未执行 |
| F20 | 部分：真实二进制 E3→F→E3→F 保留同一 index/ledger/cache，检查 sentinel 与 image_version；旧版读共享及改写、新版恢复通过；30 分钟混合退出 0 | 手机/马赛克体验、生产长时间及真实容器验收未执行 |

## 千图数据的解释

跨挂载数据使用同一合成 JPEG 的 1000 份独立文件。每份先真实生成并落盘 small；F 对每个源的确切版本取得完整证明，然后调用实际 `moveResources`（64 项分组），在两个 tmpfs 挂载间复制回退，再逐个核验目标。F 保留原结果物理键/发布，不清理历史重复资产。每轮两侧各摘要 `469314000` 字节（约 447.6 MiB）；原图摘要在移动响应之外。目标核验前与核验后的耗时分别报告。

基线对同样目标进行 ordinary demand 重读，新增 1000 次完整解码；候选核验后的新增解码为 0。跨挂载表中的核验时间是**未限速核心协议**时间，不是配置工作器的 8 MiB/s 完成时间；目录数字是两项合并维护意图，不是两次浏览器刷新。独立源文件的旧引用仍保守保留，没有伪造外部删除确认；补充提交逐轮读取有界账本页，核对 1000 份原始来源引用均仍被保守保留；不会把它们伪造为已确认删除。

同盘使用原 C `TestLedgerMoveHTTP`，1000 张不同 JPEG，基线改为本任务 E3 合并提交，candidate/base 均要求零新增解码；handler transport 绕开浏览器缓存，每轮核对实际服务端生成和重读字节、两次索引刷新。没有假称为 loopback TCP。

三轮精确原始数据、二进制 SHA 和最终来源见 [证据入口](../../../../../reviews/007f-execution-20261120/README.md)。早期轮次不能混入最终提交的性能表。

## 30 分钟混合运行

`mixed-30m/mixed.exit=0`，运行循环 `30m2.170s`（含准备的 test 总时长 1812.56 秒）。完成 411 轮、412 次复制、137 次重命名、69 次确认孤儿、35 轮压力信号、23 轮实际卸载/错盘/原盘 bind 重挂。前台 5997 次调用，平均 4161 µs，最大 287 ms；这是同进程服务 API，没有真实 HTTP，也没有配对基线延迟对照。

F 复用 389 次，完整原图累计读取 1022582824 字节；D 生成 140 次。E2 实际回收 69 项，结束时 remaining_candidates=0。观测峰值 goroutine 31（开始 29）、Go heap 16 MiB；预览 StoreFailures/Panics/TasksFailed 均为 0。堆峰值是离散采样，不能解释为真实 RSS 或 cgroup 最大值。

该产物从 `a351c2a` 构建：之后的生产差异为发布竞争统计从 `ForegroundWins` 更名 `PublicationWins`，以及 `MoveByCopy` 默认删除函数的局部捕获。混合夹具使用 `CopyFile`，未使用后者；其原始统计字段名称保留。专项和回退使用后续候选。压力由受控信号注入，E2 宽限以隔离时间戳老化；两者均不是生产压力或实际等待完整宽限的证据。

## 全量失败及尚缺的证据

原始全量输出没有标为通过。本轮 filtered handler 与相关包 vet 均退出 0，记录在 `late-checks/`；过滤列表只含真实断线、OnlyOffice listener、shutdown listener、可选计时测量及 settings listener。公开分享中间件用例包含在过滤后执行范围。

当前限制包括：监听 `0.0.0.0`/`127.0.0.1`/`[::1]` 返回 operation not permitted；分享真实监听、断线和 OnlyOffice 监听用例因此不可验。筛除明确的真实监听/断线用例后，web/settings handler 回归单独记录；它不是全量通过。

`events.handleSourceUpdates` 的 send-on-closed-channel 曾出现在开发全量中；对原始 `aaaefeb` 独立 `go test -count=3 ./internal/events` 也实际复现，保留 `baseline-events.log`。本候选没有修改这个无关模块，也没有掩盖它的竞争。

F 聚合状态已补齐索引发现行、候选行/检查、已有证明复用、并发发布、后台物理校验、隔离让步和 pending 重领计数。管理员 referencesAfter 分页报告受保护旧路径，越当前范围的路径遮蔽，每项计数最多 17（饱和值）。这些是有界观察，不是唯一图片全库统计；前台命中和解码沿用 C 统计。千图补充提交逐轮精确统计仍留在账本的原始来源引用。

实际 Docker 镜像、专用 cgroup/OOM、容器重建和手机体验均没有伪造结果。宿主继承 cgroup 仅单独只读观察，不能当作限额验收。混合场景使用真实私有挂载、D/F/前台及 E2 工作器，但内存压力为受控信号、孤儿宽限为隔离夹具显式老化，不能当作真实容器资源证明。隔离 Docker 入口已提供，需在有权限环境执行；生产图库配置和开启仍由维护者另行安排。旧外部引用由于缺乏符合 E2 强度的 unlink 证据，保持保护；候选不新增通过“文件找不到”退役引用的捷径。

## 独立复审后的修正

[独立评审](../../../../../reviews/007f-independent-b1aiEd/REVIEW.md)对 `a0ab1e5` 复现两项 P2：同内容历史变体分散时选中 small-only 资产，以及跨盘逐项提示 SQL 随文件数叠加。新候选修正这两项，原候选的三轮、30 分钟结果仅作为历史证据，不能替代修正候选验证。

新增测试包括原 small/xlarge 发布选择、变体分散选择、有界候选页外的原资产优先、提示批事务/版本隔离、实际 EXDEV 写锁争用、256 队列满、部分失败及产品工作器的外部发现/跨盘提示/索引重发现。后台组合使用两种独立摘要，区分 8 MiB/s 核验等待与移动耗时，核对原发布键/epoch、两次实际目录刷新和 xlarge 新增解码 0。原独立评审材料保持原样。

当前会话重新探测 IPv4/IPv6 回环仍为 operation not permitted，Docker socket 仍 permission denied。评审会话具备真实 TCP 能力，已对原候选在完整仓库归档中全量通过 31 个测试包；这是评审环境的结果，不声称修正候选或本会话真实 TCP 通过。修正候选的逐项退出码、来源、SHA 及未执行项见[修正证据入口](../../../../../reviews/007f-revision-20261121/README.md)。实际 Docker、资源限额、容器重建和手机体验仍待执行。


本次固定源码的核心三包完整 race、专项、vet、过滤后 handler/settings、三轮同盘/跨挂载千文件、90 秒混合均通过。写锁下实际 EXDEV：12 项 F off=2.51ms / on=2.35ms；64 项 on=12.14ms、队列满=13.19ms。所有物理复制和源删除结果正确；这些是无 C 资产的移动对照，不能解释为移除原有 C 意图的 SQL 等待。

产品工作器组合的移动时间分别为外部 rename <1ms、真实 EXDEV 2ms、满队列 3ms；后台等待及读回确认分别约 1256/1756/2255ms。每例源证明读 5279985 字节、目标读 3558453 字节，限速为 8 MiB/s，两个不同原图摘要，xlarge 新增解码均为 0。原资产线索可用时核对其存储键/epoch；丢失提示时按可用变体排序，允许选中同内容的另一历史资产。真实移动的部分失败也保留两次实际目录合并刷新。

修正候选完整后端执行退出 1，失败为 TestBatchMoveClientDisconnectStillMaintains（handler transport 无真实端口）、TestOnlyOfficeDownloadClientRedirects 和 TestHTTPListenAddr（监听被禁止）。本轮 events 通过；之前的基线竞争只作历史记录。没有把此结果标为完整后端通过。


冻结产物的 private-race 及四阶段持久索引回退全部退出 0。补充以四个新私有命名空间把同一目录 bind 到同一路径，不重置 index/ledger/cache；四阶段均通过，记录唯一挂载 ID 为 2148644521、2148644616、2148644711、2148644806。它验证了重新绑定及 unique-ID 取证入口，不是 Docker 或完整工作器重建验收。Docker 脚本仍未实际执行。

同盘复跑入口现在也会在没有前端 embed 时建立并清理其自有 synthetic 占位，不依赖开发会话残留文件。已有真实 embed 会继续使用。固定二进制对应功能提交，最终文档/复跑准备改动没有改变 Go 源码；精确提交与 SHA 在修正证据 manifest。
