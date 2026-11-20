<!-- Modified for mument: project introduction, source attribution, and development status. -->
# mument

基于 FileBrowser Quantum 的独立社区分支，重点改善移动端图片浏览与文件整理体验。

本项目独立维护，与上游官方无隶属关系。

## 来源与许可

基于 [FileBrowser Quantum](https://github.com/gtsteffaniak/filebrowser)，
其源自 [File Browser](https://github.com/filebrowser/filebrowser)。

沿用 Apache-2.0 许可证，保留上游版权与署名，详见 [LICENSE](LICENSE)。

## 项目状态

本仓库目前仅公开开发中的源码。功能、稳定性和使用体验仍在完善，尚未作为正式版本验收，不建议用于重要数据或生产环境。

暂不提供 mument 的正式发行包或容器镜像，也未承诺开发文档中的构建步骤已经完整验证。

## 当前开发分支

本地开发基线已合入 007-A 至 E3（`aaaefeb`）。本分支为 007-F 外部移动与跨盘缓存复用的独立候选，默认关闭，尚未部署、推送或合并；生产按任务书记录为 007-B，本次未核实容器现场。实现、配置及实际验证边界见 [007-F 设计](docs/development/007f/DESIGN.md)和[运维说明](docs/development/007f/OPERATIONS.md)。早期迁移来源见[迁移记录](docs/development/007-ab-migration.md)。
