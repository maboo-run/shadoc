# Changelog

Shadoc 的重要用户可见变化记录在此文件中。格式参考 [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)，版本遵循 [Semantic Versioning](https://semver.org/spec/v2.0.0.html)。

## [Unreleased]

## [0.1.5] - 2026-08-01

### Fixed

- Linux root system 服务在未显式设置 `SHADOC_LISTEN` 的 `install-app --system` 或 `update-app --system` 中，会安全保留现有 Shadoc systemd 单元的监听地址，不再意外回落到回环地址。
- 本地仓库明确保存为 Service 本地或唯一 Agent 本地归属；Service 本地仓库不再显示 Agent 选择器，Agent 本地 rsync 目录只能绑定同一 Agent 的任务。
- rsync 新仓库默认使用 SSH 远程同步目录；容量检测、任务筛选和 Agent 删除依赖统一读取仓库归属，不再从临时浏览选择或任务状态推断。

## [0.1.4] - 2026-08-01

### Changed

- 停用备份任务后可以删除任务，不再要求先解除计划依赖；删除时清理任务管理记录、计划关联和仅剩该任务的空计划。
- SQLite 关系改为代码事务维护，移除硬外键并为旧数据库提供兼容迁移；仓库、远程主机、数据库连接、Agent 和秘密的逻辑引用继续阻止误删。
- 保护范围中将未保存的排除/恢复状态明确显示为“排除未保存”和“恢复未保存”。

### Fixed

- 删除任务前检查活动运行、操作和 Agent 租约，避免后台工作失去持久化记录。
- 修复共享计划发生记录、保护草稿和调度竞态在删除任务后的残留或回写问题。
- 远程 rsync 仓库改为明确的 SSH 目标类型；升级时会迁移此前被错误标为 SFTP 的 rsync 仓库，避免以 SFTP 语义展示或探测 SSH 同步目录。
- 将手工 Agent 的主机关联与 Service 受管安装分离。关联可用于 rsync 容量检测和目录浏览，不再错误授予升级、卸载或工具安装权限。
- 统一 Agent 在线心跳窗口，避免列表显示在线而目录浏览、恢复或容量检测拒绝同一 Agent。
- 控制服务支持 `shadoc --version`，用于无副作用的部署版本核验。

## [0.1.2] - 2026-07-25

### Added

- 重新设计管理页面的响应式导航与仪表盘，新增关键指标、运行趋势、近期运行、计划覆盖和告警面板。
- 新增命令面板以及可持久化的明暗主题切换。

### Changed

- 局域网首次初始化改为明确要求一次性令牌；服务会在管理员创建前持续保存并通过 `start` 重新显示令牌，初始化完成后自动删除。

### Fixed

- 修复局域网初始化令牌在服务重启后无法找回，以及首次部署时误报“只能在本机访问”的问题。
- 恢复仪表盘成功率统计口径说明，并升级 `pgx` 安全依赖。

## [0.1.1] - 2026-07-19

### Added

- 支持将数据库快照安全恢复为 dump 文件，并补充数据库原生工具链探测与恢复预检。
- 增强远程 Agent 的工具、心跳和受管 Restic 操作反馈。

### Changed

- Agent 主动探测心跳点击后直接启动，按钮会显示进行中状态并保持禁用。
- 官方 Restic 版本目录首次加载失败时不立即显示告警，重试仍失败后才提示。
- 补充数据库恢复、Agent 操作和前端交互的自动化验证。

## [0.1.0] - 2026-07-18

首个公开预览版本。

### Added

- 面向 Linux/macOS amd64/arm64 的校验制品一键安装脚本。
- 严格 `vX.Y.Z` 发布门禁、Release 制品来源证明和 Dependabot 配置。
- MIT 许可证、安全政策、贡献指南和首次公开发布手册。

### Changed

- 官方 GitHub 仓库身份统一为 `maboo-run/shadoc`。
- README 调整为面向管理员的安装和使用手册。

[Unreleased]: https://github.com/maboo-run/shadoc/compare/v0.1.5...HEAD
[0.1.5]: https://github.com/maboo-run/shadoc/releases/tag/v0.1.5
[0.1.4]: https://github.com/maboo-run/shadoc/releases/tag/v0.1.4
[0.1.2]: https://github.com/maboo-run/shadoc/releases/tag/v0.1.2
[0.1.1]: https://github.com/maboo-run/shadoc/releases/tag/v0.1.1
[0.1.0]: https://github.com/maboo-run/shadoc/releases/tag/v0.1.0
