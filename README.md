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

安装脚本会检测系统与架构，从同一个 GitHub Release 下载控制服务、全部平台 Agent 和 `SHA256SUMS`，逐个验证 SHA-256 后调用内置安装命令。安装完成后，控制服务会在受管程序旁保存权限为 `0600` 的本地完整性记录 `shadoc.sha256`；后续更新会同步刷新该记录。它不会安装系统软件包，也不会把管理页面暴露到公网。

如果希望先检查脚本：

```bash
curl -fsSLO https://github.com/maboo-run/shadoc/releases/latest/download/install.sh
less install.sh
sh install.sh
```

### 安装指定版本

```bash
curl -fsSL https://github.com/maboo-run/shadoc/releases/latest/download/install.sh \
  | SHADOC_VERSION=0.1.2 sh
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

### Linux root system 服务

只有确实需要读取普通用户无法访问的源文件时，才应把控制服务作为 root 运行。root 实例可读取和修改的系统范围更大；管理页面中的备份任务也会继承这项权限。

全新安装为 Linux root systemd 服务：

```bash
curl -fsSL https://github.com/maboo-run/shadoc/releases/latest/download/install.sh \
  | sudo env SHADOC_ALLOW_ROOT=1 sh
```

root 安装固定使用 `/var/lib/shadoc`，不接受自定义 `SHADOC_DATA_DIR`。受管程序固定为 `/var/lib/shadoc/app/shadoc`，systemd 单元固定为 `/etc/systemd/system/shadoc.service`；两者都必须由 root 所有且不能由组或其他用户写入。页面不会要求、接收或保存 sudo 密码。

root 服务首次安装默认只监听 `127.0.0.1:8585`。若管理员明确要在受信任局域网开放管理页面，可在终端执行：

```bash
sudo env SHADOC_LISTEN=0.0.0.0:8585 /var/lib/shadoc/app/shadoc start --system
```

这会重写 systemd 单元并重启服务；管理页面仍是 HTTP，因此不要在不受信任网络或公网直接暴露该端口。后续 `install-app --system` 与 `update-app --system` 会保留已验证的现有监听地址，除非显式提供新的 `SHADOC_LISTEN`。

把已有的 Linux 用户实例迁移为 root 服务：

```bash
SHADOC_BIN="${XDG_CONFIG_HOME:-$HOME/.config}/shadoc/app/shadoc"
sudo "$SHADOC_BIN" migrate-to-root
```

迁移不会访问网络或重新下载程序。它先用安装或更新时留下的本地 `shadoc.sha256` 重新计算并核对当前受管程序；记录缺失、格式错误或校验不一致都会在停止旧服务前直接拒绝迁移。这个离线校验主要发现程序损坏或程序被单独替换；因为程序和记录都属于原普通用户，它不承诺抵御两者被同时篡改的本地账号攻击。

校验通过后，迁移命令从 `SUDO_USER`/`SUDO_UID` 确认原用户，停止并校验 Shadoc 自己生成且没有 drop-in 的 user systemd 服务，把数据以 root 所有的安全权限复制到暂存目录，校验 SQLite、秘密库密钥和新程序后再原子切换。同一时间只允许一个迁移进程。新 root 服务通过 systemd 状态和 `/api/health` 检查后，命令会永久删除原用户服务定义和原数据目录。健康确认前失败会删除 root 暂存并恢复原用户实例；健康确认后的清理失败不会回滚健康的 root 实例，可从原 sudo 用户重试：

```bash
sudo /var/lib/shadoc/app/shadoc migrate-to-root
```

迁移会拒绝符号链接、特殊文件、来源不明的 systemd 指令或 drop-in、已有 root 实例以及不安全的目录所有权。原用户目录中的受管 Restic 不会作为 root 可执行文件迁移；迁移后会优先使用系统 Restic，也可以在兼容性中心重新安装受管 Restic。建议迁移前额外保留一份控制面恢复包。

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

远程 rsync 仓库的同步始终通过固定主机密钥的 SSH 进行；只需填写 SSH rsync 接收端可见的绝对目录路径。执行时会把目标规范为带结尾 `/` 的目录参数，即使保存时省略也不影响同步，可兼容对目录参数格式有严格要求的 NAS rsync 接收端。只有在 Agent 页面由管理员明确关联到该远程主机、在线且具备容量能力的 Agent，仓库列表才会检测该主机本地路径的容量。没有关联 Agent 时会显示暂不支持容量检测，但不影响 SSH 同步。这个关联只说明 Agent 可见该主机的文件系统，并不会把手工安装的 Agent 变成可由 Service 升级或卸载的托管安装。Restic 远程仓库的路径必须填写 SFTP 登录用户看到的绝对路径，编辑器旁的问号会说明它与 Agent 本地路径可能不同；Restic 不使用 Agent 本地目录浏览来推断 SFTP 路径。

本地路径的归属必须明确：`Service 本地仓库` 由控制服务所在机器访问，不选择 Agent；`Agent 本地同步目录` 必须绑定唯一 Agent，只能用于在同一 Agent 上执行的 rsync 任务。新建 rsync 仓库默认选择 SSH 远程同步目录，只有明确选择 Agent 本地同步目录时才要求 Agent。Agent 目录浏览只用于选择该 Agent 文件系统中的路径，不是临时浏览辅助项，也不会改变仓库归属。

管理员手工启动任务后，先提示“正在执行中”，任务行的“立即运行”会按引擎改为“正在备份”或“正在同步”并保持禁用，页面状态区也以同一服务端持久化操作展示进度。刷新页面或离开后重新进入任务列表会重新接管仍在运行的操作；只有操作真正进入成功终态后才提示“执行完成”，失败详情会保留在任务页面而不只显示为短暂通知。

新版 Agent 领取任务后每 10 秒通过 mTLS 上报执行活性并续租，排队等待领取与实际执行不再共用固定 30 分钟截止时间；只要 Agent 持续运行和上报，初次大同步或长时间 Restic 备份不会被误判为过期。引擎结束后 Agent 会继续续租并重试提交同一最终结果，直到 Service 明确确认；Service 对完全相同的结果提交按幂等成功处理。rsync 任务运行区会展示扫描、传输和目标整理阶段，并在 rsync 可提供数据时显示整体字节进度、文件数、速度和预计剩余时间；超过 30 秒没有续租时会明确显示 Agent 通信中断，不再把最后一次速度和 ETA 当作实时状态。旧 Agent 必须先升级，才能获得执行续租和实时进度。

任务编辑页只保留保护范围摘要、当前规则数量和“查看并调整保护范围”入口，不再重复展开完整规则及排除建议。点击入口或任务列表中的“保护范围”，会进入当前任务的独立范围页面，返回时仍回到该任务编辑页。页面先扫描源目录，再分别展示目录内发现的总文件数、将保护、已排除和需关注数量；“全部”“将保护”“需关注”“已排除”和“排除规则”标签集中承载范围核对与调整。“排除规则”内可以按行编辑规则、查看当前预览影响并采用可选建议。文件按目录逐层浏览：当前层只显示直接子项，一个文件夹代表其全部内容；若其中存在排除或不可读项，会标记为“部分保护”，进入文件夹后才展示下一层。搜索只筛选当前层级。

清单中的文件或文件夹可暂存为“排除”，已排除项也可“恢复保护”。点“预览规则效果”会使用当前草稿重新扫描，并在原页面直接更新统计、目录清单和规则影响；这一步只生成 15 分钟有效的临时预览，不会修改任务。此时“已排除”表示当前预览判定，“排除未保存”表示对应规则尚未写入任务，两者不再使用容易混淆的“待排除”并列展示。确认结果后再点“保存规则”才会写入任务，若继续修改规则、离开页面或预览过期，未保存草稿均不会生效。恢复由通配规则排除的项目会移除命中的整条规则，因此同一规则覆盖的其他内容也会恢复保护，页面会明确提醒。逐项清单只保存相对路径并支持服务端分页；单次最多扫描 100,000 个条目，达到上限时会明确标记统计不完整。临时目录权限为 `0700`、清单文件为 `0600`，绝对源路径不会写入清单。远程任务由新版 Agent 通过现有 mTLS 通道分批上传相对路径；旧 Agent 仍返回范围汇总，页面会提示先升级 Agent 才能逐项查看。

删除备份任务前必须先将任务停用；如果仍有运行、任务操作或 Agent 租约正在进行，需等待其完成后再确认删除。删除确认后，控制服务会一并清理该任务的运行与日志、任务操作、范围预览、Agent 租约、验证证据、告警及计划关联；只包含该任务的空计划会一并删除，共享计划保留其余任务。删除任务不会删除独立仓库、Restic 快照、源目录或外部数据库；仓库、数据库连接、远程主机或 Agent 仍被任务引用时仍不可删除。

目录运行的记录会把“范围内总文件”与“实际备份或同步文件”分开显示。前者是该次运行发现且应处理的文件总数，后者是实际成功处理的文件数；同一次 Restic 运行同时取得两个值且不相等时，即使创建了快照也会判定为“部分成功”，相等且没有其他错误时才是完整成功。这样权限不足等问题不会再被变化文件数或已创建快照掩盖。本机和 Agent 任务遵循同一判定；Agent 会先保护部分快照再回传结果，保护失败时仓库会暂停新备份，待下一次运行先完成保护。

## 依赖项

| 工具 | 什么时候需要 | 要求与安装方式 |
| --- | --- | --- |
| Restic | 目录或数据库版本化备份、恢复、维护 | `0.17.0` 或更高版本；可在兼容性中心安装应用管理的版本，也可复用通过探测的系统版本 |
| rsync | rsync 单向增量同步任务 | `3.x`；由操作系统或管理员安装 |
| `mysqldump`、`mysql` | MySQL 逻辑备份与直接恢复 | 安装与目标数据库兼容的官方客户端；仅恢复为 dump 文件时不需要 `mysql` |
| `pg_dump`、`pg_restore` | PostgreSQL 逻辑备份与直接恢复 | 安装与目标数据库兼容的官方客户端；仅恢复为 dump 文件时不需要 `pg_restore` |
| SSH/SFTP 服务 | SFTP 仓库、SSH rsync、远程 Agent 部署 | 必须取得并确认真实主机密钥；Shadoc 不会静默接受未知或变化的密钥 |

如果终端中能执行 `restic`，但初始化仍提示 Restic 不存在，通常是 macOS 的 LaunchAgent 或其他后台服务没有继承终端 `PATH`。控制服务会优先使用受管 Restic，其次探测服务 `PATH` 和常见系统安装位置；仍未找到时，请在“兼容性中心”安装受管 Restic。仓库初始化报错中的 Restic 可执行文件问题会与仓库目录问题分开提示。

控制服务启动时会集中探测本机 Restic、rsync 和数据库客户端的可执行路径与版本；兼容性中心、诊断导出和实际任务复用同一结果，不会因为打开不同页面而重复探测。安装或更换工具后，可在“兼容性中心”点击“重新检测本机工具”，无需重启控制服务。若后台服务的 `PATH` 中仍找不到客户端，可展开“手动指定工具路径”，填写经过身份和版本校验的绝对路径；留空并保存即可恢复自动探测。手动设置的 MySQL 客户端会作为新建或重新测试数据库连接的自动默认值，rsync 路径会立即用于后续本机 rsync 运行。远程 Agent 的工具版本仍由对应节点单独验证。

数据库连接编辑器的常用配置只需要名称、数据库类型/用途、连接地址、账号、密码和 TLS 模式。页面的“测试连接”使用内置 Go 驱动检查网络、TLS、认证和当前用途权限；实际备份与恢复仍调用 `mysqldump`/`mysql` 或 `pg_dump`/`pg_restore`。控制服务会自动从系统 `PATH` 探测所需官方客户端，只有探测不到或需要修复旧路径时才需要在“高级设置”中填写绝对路径；清空已填写路径后保存即可重新自动探测。任务预检和运行前也会修复历史上把同一个导出工具保存到两个角色的明显错误，优先在导出工具同目录寻找正确的管理客户端；仍找不到时需要安装或填写该客户端。MySQL 的“优先使用 TLS”使用客户端默认兼容模式，避免 MariaDB 或旧版 MySQL 客户端因不支持 `--ssl-mode` 而失败。Shadoc 不会自动安装数据库客户端。

数据库任务点击“预检并启用”时，会先保存停用草稿，再由后台持久化操作执行轻量预检：只读验证 Restic 仓库，并使用官方 dump 客户端的 `--no-data`/`--schema-only` 模式检查数据库导出，不创建数据库备份快照。预检成功后任务才会启用；失败则保留停用草稿，并在操作详情中显示失败原因。已启用的定时任务不会因预检超过 24 小时自动停止，正式运行仍会记录真实导出或仓库错误。

数据库备份使用逻辑导出流直接进入 Restic，不复制运行中的数据库数据文件，也不落盘明文导出中间文件。

数据库恢复页面提供两种模式： “直接恢复到数据库”会使用恢复用途连接、官方导入客户端，并要求目标是新数据库或空数据库；“恢复为 dump 文件”不需要数据库连接、密码或本机导入客户端，只需填写一个已存在的绝对输出目录。控制服务会根据快照元数据中的文件名检查目录内的目标文件，实际恢复时把快照中的完整 `.sql` 或 `.dump` 文件流式写入该目录的 `0600` 临时文件，成功后原子发布为同名新文件，不覆盖已有文件。生成的 dump 文件包含敏感数据，管理员应按数据库文件的安全级别自行保护和清理。

## 服务管理

一键安装完成后，脚本会打印受管命令的绝对路径。默认路径为：

- Linux：`$XDG_CONFIG_HOME/shadoc/app/shadoc`；未设置 `XDG_CONFIG_HOME` 时为 `$HOME/.config/shadoc/app/shadoc`。
- macOS：`$HOME/Library/Application Support/shadoc/app/shadoc`。
- 自定义安装：`$SHADOC_DATA_DIR/app/shadoc`。
- Linux root system 服务：`/var/lib/shadoc/app/shadoc`。

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

Linux root system 服务的命令需要 sudo 和 `--system`：

```bash
sudo /var/lib/shadoc/app/shadoc status --system
sudo /var/lib/shadoc/app/shadoc restart --system
sudo /var/lib/shadoc/app/shadoc stop --system
sudo /var/lib/shadoc/app/shadoc start --system
sudo /var/lib/shadoc/app/shadoc reset-admin-password --system
```

当管理页面监听非本机地址且管理员尚未创建时，`start` 会直接打印一个一次性 LAN 初始化令牌。令牌在重启后保持不变，并在管理员创建成功后从数据目录删除；从局域网首次打开管理页面时需要输入该令牌。本机回环地址初始化不需要令牌。

如果在创建管理员前忘记或关闭了令牌输出，请带上安装时相同的 `SHADOC_DATA_DIR` 和 `SHADOC_LISTEN` 配置重新运行 `start`，即可再次显示同一个令牌；如果令牌文件意外丢失，服务会在这次启动时生成一个新令牌。例如：

```bash
SHADOC_DATA_DIR=/srv/shadoc SHADOC_LISTEN=0.0.0.0:8585 "$SHADOC_BIN" start
```

令牌只用于首次初始化，请勿通过不受信任的渠道发送。

远程 Agent 的托管升级会先在远端固定暂存路径执行 `shadoc-agent --version`，确认制品版本与控制服务目标版本完全一致后才切换服务；页面会保留成功或失败的操作结果。只有由 Service 部署过的 Agent 才有这个入口，手工 Agent 的主机关联不会授予远程升级或卸载权限。如果提示暂存制品仍是 `SNAPSHOT` 或其他旧版本，请重新生成同一版本的完整制品并重启控制服务，例如 `make build VERSION=0.1.4`，不要重复提交同一个旧制品。控制服务本身可用 `shadoc --version` 作无副作用的版本核验。

同一 Agent 的托管部署、升级和卸载使用同一个生命周期互斥键。会重启或停止 Agent 的操作先进入排空状态：停止领取新租约，并等待当前任务结束后才改变远端服务。排空失败或超时时不会为了完成管理操作而强行中断正在执行的 rsync 或 Restic。首次部署接口拒绝覆盖仍活动的 Agent，避免上传失败后的首次安装清理误删现有服务；托管 Agent 即使版本号与控制服务相同，也会显示“重新安装 Agent”，并通过可校验、可回滚的暂存升级链路完成同版本修复。

Agent 节点卡片内的“主动探测心跳”点击后会直接启动，不再弹出确认框；系统会等待该节点当前任务结束，通过固定 SSH 服务命令安全重启 Agent，并等待新的认证心跳。探测期间原按钮显示“探测中”动画并保持禁用，完成或失败结果通过页面提示展示；主动探测不会升级 Agent 或修改备份工具。升级和工具探测的进度与终态仍显示在对应 Agent 节点内。

删除托管 Agent 关联的远程主机连接时，控制服务会解除该 Agent 的失效绑定；升级后启动时也会修复历史遗留的悬空绑定。由于删除连接后已无法通过 SSH 安全卸载远端进程，页面不会再对这类 Agent 发起心跳探测、升级或远程卸载：请先在源端停止并清理旧 Agent，随后在页面撤销旧凭据，再选择新的远程主机并使用完全相同的 Agent ID 重新部署。不要让旧进程与新部署同时使用同一 Agent ID 运行。

已撤销或已卸载的 Agent 可以在节点详情中删除管理记录。删除前页面会预览任务引用和进行中的 Agent 操作；存在依赖、凭据仍有效或预览后身份状态发生变化时，服务端会拒绝删除。删除只移除 Agent 身份及证书生命周期记录，不会停止远端进程或删除远端文件，已有审计与操作记录仍会保留。之后执行托管部署时，控制服务会清理固定 Agent 数据目录中的旧身份文件并强制使用新的一次性令牌注册，避免遗留证书跳过注册。

## 升级与卸载

升级到最新稳定版：

```bash
"$SHADOC_BIN" update-app
```

Linux root system 服务使用：

```bash
sudo /var/lib/shadoc/app/shadoc update-app --system
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

卸载 Linux root system 服务但保留 `/var/lib/shadoc` 中的数据：

```bash
sudo /var/lib/shadoc/app/shadoc uninstall-app --system
```

永久删除应用数据需要显式参数和交互确认：

```bash
"$SHADOC_BIN" uninstall-app --remove-data
```

卸载 Shadoc 不会删除已经存在于本地、SFTP 或 S3 的 Restic 仓库。

## 配置

| 环境变量 | 说明 | 默认值 |
| --- | --- | --- |
| `SHADOC_DATA_DIR` | SQLite、秘密库、受管工具和运行数据目录；Linux root system 服务固定为 `/var/lib/shadoc` | 平台用户配置目录下的 `shadoc` |
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
