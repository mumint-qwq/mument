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

## 开发进度

007-A 至 F 的核心实现已整合到本地主线，包括预览资源保护、图片比例元数据、移动复用、后台生成以及缓存回收和空间预算。最终候选完成独立复审、隔离容器、手机操作后核验和 30 分钟混合回归；未覆盖组合与真实图库上线状态分别记录。详见[007 当前状态与验收范围](docs/development/007/STATUS.md)，迁入来源见[迁移记录](docs/development/007-ab-migration.md)。
