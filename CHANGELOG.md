# Changelog

Shadoc 的重要用户可见变化记录在此文件中。格式参考 [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)，版本遵循 [Semantic Versioning](https://semver.org/spec/v2.0.0.html)。

## [Unreleased]

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

[Unreleased]: https://github.com/maboo-run/shadoc/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/maboo-run/shadoc/releases/tag/v0.1.1
[0.1.0]: https://github.com/maboo-run/shadoc/releases/tag/v0.1.0
