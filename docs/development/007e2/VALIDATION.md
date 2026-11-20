# 007-E2 复验

从候选工作树运行，使用 Linux 项目 Go 工具链。输出目录保存提交号、测试二进制、SHA-256、报告、日志和退出码。夹具只使用合成数据。

```bash
scripts/development/verify-007e2.sh /tmp/007e2-review
```

验证覆盖进程中断和重开账本、私有挂载、E1 封存试算、C/D 核心 race、进程内 HTTP handler、1,000 张同盘移动及用冻结 D RC2 源码重开 E2 夹具。回退探针核对原图摘要、预览无重新解码、比例、尺寸、方向和已完成 guard。测试用可控时钟演练宽限到期。私有挂载复验覆盖来源和缓存的 mount ID 都变化后的恢复，以及错盘、内嵌挂载和同挂载换 inode。其余回归覆盖旧预留零字节结算、未知旧隔离文件与新发布分别保留及回收、旧账本迁移、超限目录真实递归删除时获取位置锁，以及小目录删除与 C 移动、同名版本变更交错。

有 Docker 权限的验收机可运行限额回放。固定静态二进制位于 scratch 镜像，网络关闭，资源限为 2 CPU、1 GiB 内存和 2 GiB 内存加 swap。该模式记录本地镜像 ID。

```bash
scripts/development/verify-007e2.sh /tmp/007e2-container --container
```

用管理员身份请求 GET /api/settings/preview-orphans。将上一页的 next_candidate、next_intent 和 next_object 传给 afterCandidate、afterIntent 和 afterObject。值为 0 或空可以从头查看相应窗口。due_allocated_bytes 是当前对象的分配块估计，实际完成的 unlink 和失败原因有独立有限日志。分页期间的写入会改变后续结果。
