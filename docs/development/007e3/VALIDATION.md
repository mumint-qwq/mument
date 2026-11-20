# 007-E3 配置与复验

以下为合成复验示例。容量淘汰默认关闭，observe 只观测及试算，enforce 开启水位和写入准入。真实容量和值需用目标专用分区验收。

```yaml
server:
  cacheDir: /var/tmp/synthetic-preview-cache
  cacheDirCleanup: false
  previewSpace:
    mode: observe
    budgetMiB: 64
    minimumFreeMiB: 512
    resumeFreeMiB: 768
    stableSeconds: 60
    capacityEviction: false
```

管理员查询 /api/settings/preview-space 核对配置、覆盖、根、空间、在途写入和三个开关。/api/settings/preview-capacity 提供候选与持久审计试算，分页使用 time、row、used、intent 和 audit。要试用准入，在合成环境将 mode 设为 enforce 并受控重启。要独立验证物理淘汰，同时设 capacityEviction 为 true，可保留 D 关闭并分别验证 E2 开关。

```bash
scripts/development/verify-007e3.sh /tmp/007e3-native
scripts/development/verify-007e3.sh /tmp/007e3-docker --container
```

宿主机脚本保存固定源提交、二进制 SHA-256、测试日志和退出码。覆盖合成高低水位、并发预留、保护筛选、后台手工暂停恢复、强制退出、独立私有挂载、真实 ENOSPC 和 inode 以及向 E2 和 D 回退后再回到 E3。复用 E2 的完整复跑，包含 E1、应用入口、千图移动及 D 守护。Docker 回放用无网络和只读镜像根，运行时核对 2 CPU、1 GiB 内存及 1 GiB swap，记录容器退出与 OOM 状态。两个独立容器复用临时卷，要求唯一挂载 ID 改变，再按同一 E3 意图恢复。
