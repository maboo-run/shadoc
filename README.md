# 影刻 · Shadoc

<p align="center">
  <strong>简体中文</strong> · <a href="README_EN.md">English</a>
</p>

<p align="center">
  <img src="web/public/shadoc-icon.png" alt="Shadoc icon" width="120" height="120">
</p>

<p align="center">
  面向个人与小型团队的自托管备份控制服务。
</p>

<p align="center">
  <a href="https://github.com/maboo-run/shadoc/actions/workflows/ci.yml"><img src="https://github.com/maboo-run/shadoc/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/maboo-run/shadoc/releases"><img src="https://img.shields.io/github/v/release/maboo-run/shadoc" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="MIT License"></a>
</p>

Shadoc 运行在拥有或可以访问源数据的备份节点上。一个常驻的 Go 控制服务负责配置、调度、安全门禁和状态保存，内嵌的管理页面负责日常操作；关闭浏览器不会停止任务。Restic、rsync 和数据库官方客户端负责实际的数据处理。

当前 `0.x` 版本属于公开预览阶段。升级前请阅读对应 Release Notes，并保留控制面恢复包和仓库凭据。

## 核心特性

- **版本化备份**：使用 Restic 创建加密、去重、增量快照，支持保留、检查、维护和恢复。
- **目录与数据库**：保护本机或 Agent 目录，以及 MySQL、PostgreSQL 单库逻辑备份。
- **单向增量同步**：提供显式选择的 rsync 引擎；它不具备快照、保留或恢复语义。
- **多种仓库**：支持本地目录、固定 SSH 主机密钥的 SFTP，以及结构化 S3 兼容对象存储。
- **安全恢复**：恢复前执行只读预检和管理员复验；目录只恢复到新目标，数据库不覆盖非空目标，也可安全导出为新的 dump 文件。
- **远程 Agent**：使用独立 TLS 1.3 mTLS 通道、一次性注册令牌、能力探测和短期任务租约。
- **持久运行**：任务、计划和长耗时操作由后台服务管理，页面刷新或关闭不影响执行。
- **安全默认值**：不提供任意 Shell、脚本、命令参数或环境变量执行入口；秘密加密保存，日志入库前脱敏。
- **运行可见性**：提供运行记录、容量趋势、告警、通知投递和不可逐条修改的审计记录。
- **中英文界面**：管理页面可在简体中文和英语之间切换。

## 平台支持

| 组件 | Linux amd64 | Linux arm64 | macOS Intel | macOS Apple Silicon | Windows amd64/arm64 |
| --- | --- | --- | --- | --- | --- |
| 控制服务 | 支持 | 支持 | 支持 | 支持 | 不支持 |
| 远程 Agent | 支持 | 支持 | 支持 | 支持 | 支持 |

Linux 控制服务使用 systemd user service，macOS 使用 LaunchAgent。管理页面通过现代浏览器访问；自动化验收使用 Chrome/Chromium。

## 安装

### 一键安装最新稳定版

使用普通服务账号执行，不要默认使用 `root`：

```bash
curl -fsSL https://github.com/maboo-run/shadoc/releases/latest/download/install.sh | sh
```

安装脚本会检测系统与架构，从同一个 GitHub Release 下载控制服务、全部平台 Agent 和 `SHA256SUMS`，逐个验证 SHA-256 后调用内置安装命令。它不会安装系统软件包，也不会把管理页面暴露到公网。

如果希望先检查脚本：

```bash
curl -fsSLO https://github.com/maboo-run/shadoc/releases/latest/download/install.sh
less install.sh
sh install.sh
```

### 安装指定版本

```bash
curl -fsSL https://github.com/maboo-run/shadoc/releases/latest/download/install.sh \
  | SHADOC_VERSION=0.1.0 sh
```

只安装控制服务、不下载远程部署所需的 Agent 制品：

```bash
curl -fsSL https://github.com/maboo-run/shadoc/releases/latest/download/install.sh \
  | SHADOC_INSTALL_AGENTS=0 sh
```

安装程序默认监听 `127.0.0.1:8585`。需要自定义数据目录或监听地址时，把环境变量传给执行脚本的 `sh`：

```bash
curl -fsSL https://github.com/maboo-run/shadoc/releases/latest/download/install.sh \
  | SHADOC_DATA_DIR=/srv/shadoc SHADOC_LISTEN=127.0.0.1:9090 sh
```

## 快速开始

1. 打开 [http://127.0.0.1:8585](http://127.0.0.1:8585)。
2. 创建唯一的管理员账号，并使用至少 12 个字符的密码。
3. 在“兼容性中心”确认 Restic 和计划使用的工具已经就绪。
4. 创建本地、SFTP 或 S3 仓库，并完成初始化或只读接入验证。
5. 创建备份任务，查看保护范围预览后再启用。
6. 配置任务的定时执行和仓库保留策略。
7. 手工运行一次任务，在“运行记录”中检查结果和脱敏日志。
8. 使用一个新目标执行恢复演练，确认备份确实可恢复。

每个 Restic 任务独占一个仓库。不要把多个不相关数据源放进同一个任务仓库。

## 依赖项

| 工具 | 什么时候需要 | 要求与安装方式 |
| --- | --- | --- |
| Restic | 目录或数据库版本化备份、恢复、维护 | `0.17.0` 或更高版本；可在兼容性中心安装应用管理的版本，也可复用通过探测的系统版本 |
| rsync | rsync 单向增量同步任务 | `3.x`；由操作系统或管理员安装 |
| `mysqldump`、`mysql` | MySQL 逻辑备份与直接恢复 | 安装与目标数据库兼容的官方客户端；仅恢复为 dump 文件时不需要 `mysql` |
| `pg_dump`、`pg_restore` | PostgreSQL 逻辑备份与直接恢复 | 安装与目标数据库兼容的官方客户端；仅恢复为 dump 文件时不需要 `pg_restore` |
| SSH/SFTP 服务 | SFTP 仓库、SSH rsync、远程 Agent 部署 | 必须取得并确认真实主机密钥；Shadoc 不会静默接受未知或变化的密钥 |

如果终端中能执行 `restic`，但初始化仍提示 Restic 不存在，通常是 macOS 的 LaunchAgent 或其他后台服务没有继承终端 `PATH`。控制服务会优先使用受管 Restic，其次探测服务 `PATH` 和常见系统安装位置；仍未找到时，请在“兼容性中心”安装 Restic 后重启控制服务。仓库初始化报错中的 Restic 可执行文件问题会与仓库目录问题分开提示。

控制服务启动时会集中探测并固定本机 Restic/rsync 的可执行路径和版本；兼容性中心、诊断导出和实际任务复用同一结果，不会因为打开不同页面而重复探测。安装或更换工具后重启控制服务即可重新建立结果。远程 Agent 的工具版本，以及每个数据库连接对应的官方客户端，仍由各自节点/连接单独验证。

数据库连接编辑器的常用配置只需要名称、数据库类型/用途、连接地址、账号、密码和 TLS 模式。页面的“测试连接”使用内置 Go 驱动检查网络、TLS、认证和当前用途权限；实际备份与恢复仍调用 `mysqldump`/`mysql` 或 `pg_dump`/`pg_restore`。控制服务会自动从系统 `PATH` 探测所需官方客户端，只有探测不到或需要修复旧路径时才需要在“高级设置”中填写绝对路径；清空已填写路径后保存即可重新自动探测。任务预检和运行前也会修复历史上把同一个导出工具保存到两个角色的明显错误，优先在导出工具同目录寻找正确的管理客户端；仍找不到时需要安装或填写该客户端。MySQL 的“优先使用 TLS”使用客户端默认兼容模式，避免 MariaDB 或旧版 MySQL 客户端因不支持 `--ssl-mode` 而失败。Shadoc 不会自动安装数据库客户端。

数据库任务点击“预检并启用”时，会先保存停用草稿，再由后台持久化操作执行轻量预检：只读验证 Restic 仓库，并使用官方 dump 客户端的 `--no-data`/`--schema-only` 模式检查数据库导出，不创建数据库备份快照。预检成功后任务才会启用；失败则保留停用草稿，并在操作详情中显示失败原因。已启用的定时任务不会因预检超过 24 小时自动停止，正式运行仍会记录真实导出或仓库错误。

数据库备份使用逻辑导出流直接进入 Restic，不复制运行中的数据库数据文件，也不落盘明文导出中间文件。

数据库恢复页面提供两种模式： “直接恢复到数据库”会使用恢复用途连接、官方导入客户端，并要求目标是新数据库或空数据库；“恢复为 dump 文件”不需要数据库连接、密码或本机导入客户端，只需填写一个已存在的绝对输出目录。控制服务会根据快照元数据中的文件名检查目录内的目标文件，实际恢复时把快照中的完整 `.sql` 或 `.dump` 文件流式写入该目录的 `0600` 临时文件，成功后原子发布为同名新文件，不覆盖已有文件。生成的 dump 文件包含敏感数据，管理员应按数据库文件的安全级别自行保护和清理。

## 服务管理

一键安装完成后，脚本会打印受管命令的绝对路径。默认路径为：

- Linux：`$XDG_CONFIG_HOME/shadoc/app/shadoc`；未设置 `XDG_CONFIG_HOME` 时为 `$HOME/.config/shadoc/app/shadoc`。
- macOS：`$HOME/Library/Application Support/shadoc/app/shadoc`。
- 自定义安装：`$SHADOC_DATA_DIR/app/shadoc`。

以下示例先把实际路径保存为 `SHADOC_BIN`：

```bash
# Linux 默认安装
SHADOC_BIN="${XDG_CONFIG_HOME:-$HOME/.config}/shadoc/app/shadoc"

# macOS 默认安装请改为：
# SHADOC_BIN="$HOME/Library/Application Support/shadoc/app/shadoc"
```

常用命令：

```bash
"$SHADOC_BIN" status
"$SHADOC_BIN" start
"$SHADOC_BIN" start --port 9090
"$SHADOC_BIN" restart
"$SHADOC_BIN" stop
"$SHADOC_BIN" help
```

`stop` 只停止控制服务，不删除任务、秘密、运行记录或备份仓库。

远程 Agent 的托管升级会先在远端固定暂存路径执行 `shadoc-agent --version`，确认制品版本与控制服务目标版本完全一致后才切换服务；页面会保留成功或失败的操作结果。如果提示暂存制品仍是 `SNAPSHOT` 或其他旧版本，请重新生成同一版本的完整制品并重启控制服务，例如 `make build VERSION=0.1.0`，不要重复提交同一个旧制品。

Agent 节点卡片内的“主动探测心跳”点击后会直接启动，不再弹出确认框；系统会等待该节点当前任务结束，通过固定 SSH 服务命令安全重启 Agent，并等待新的认证心跳。探测期间原按钮显示“探测中”动画并保持禁用，完成或失败结果通过页面提示展示；主动探测不会升级 Agent 或修改备份工具。升级和工具探测的进度与终态仍显示在对应 Agent 节点内。

## 升级与卸载

升级到最新稳定版：

```bash
"$SHADOC_BIN" update-app
```

升级到指定稳定版本：

```bash
"$SHADOC_BIN" update-app --version 0.2.0
```

升级流程会下载官方平台制品、校验 Release 中的 SHA-256、保存上一份二进制、原子替换并重启服务。新版本健康检查失败时会尝试恢复旧二进制。数据库迁移无法通过替换二进制撤销，因此升级前仍应导出控制面恢复包并阅读 Release Notes。

卸载控制服务和受管程序，但保留应用数据：

```bash
"$SHADOC_BIN" uninstall-app
```

永久删除应用数据需要显式参数和交互确认：

```bash
"$SHADOC_BIN" uninstall-app --remove-data
```

卸载 Shadoc 不会删除已经存在于本地、SFTP 或 S3 的 Restic 仓库。

## 配置

| 环境变量 | 说明 | 默认值 |
| --- | --- | --- |
| `SHADOC_DATA_DIR` | SQLite、秘密库、受管工具和运行数据目录 | 平台用户配置目录下的 `shadoc` |
| `SHADOC_LISTEN` | 管理页面监听地址 | `127.0.0.1:8585` |
| `SHADOC_AGENT_SERVICE` | Agent 连接的控制服务 HTTPS 地址 | 无 |
| `SHADOC_AGENT_DATA_DIR` | Agent 证书与运行目录 | `./agent-data` |
| `SHADOC_AGENT_ALLOWED_ROOTS` | Agent 可访问的绝对根目录，逗号分隔 | 平台根目录 |

旧版 `RESTIC_CONTROL_*` 变量仅用于迁移兼容；同一配置同时出现新旧变量时以 `SHADOC_*` 为准。

## 数据隐私与安全

- Shadoc 是自托管程序，不提供托管云服务，不包含分析或遥测上报。
- 配置、SQLite、加密秘密和运行记录保存在所选数据目录；备份内容写入管理员配置的仓库。
- 仓库密码、SSH 私钥、数据库密码和通知令牌进入本地加密秘密库，不以明文保存到 SQLite、审计或任务租约。
- 默认管理端口是本机 HTTP，只监听 `127.0.0.1`。不要直接暴露到公网；跨设备访问应使用可信 VPN 或经过认证的 HTTPS 反向代理。
- 程序只在明确功能需要时访问外部网络，包括 GitHub Releases、管理员配置的仓库、Agent、ntfy 或 Webhook 端点。
- 恢复和删除等高影响操作有预检、影响确认或管理员复验，但管理员仍应定期执行独立恢复演练。
- 诊断和 Issue 内容在提交前仍需人工检查；不要公开密码、令牌、私钥、真实日志或可识别的内部路径。

安全漏洞请按照 [SECURITY.md](SECURITY.md) 使用 GitHub 私密漏洞报告，不要创建公开 Issue。

## 许可证

Shadoc 使用 [MIT License](LICENSE)。
