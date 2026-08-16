# 影刻 · Shadoc

<p align="center">
  <strong>简体中文</strong> · <a href="README_EN.md">English</a>
</p>

<p align="center">
  <img src="web/public/shadoc-icon.png" alt="Shadoc icon" width="120" height="120">
</p>

<p align="center">面向个人与小型团队的自托管备份控制服务。</p>

<p align="center">
  <a href="https://github.com/maboo-run/shadoc/actions/workflows/ci.yml"><img src="https://github.com/maboo-run/shadoc/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/maboo-run/shadoc/releases"><img src="https://img.shields.io/github/v/release/maboo-run/shadoc" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="MIT License"></a>
</p>

Shadoc 在备份节点上运行一个常驻的 Go 控制服务，并提供浏览器管理页面。页面关闭后，任务、计划和长耗时操作仍会继续运行。

## 功能

- 使用 Restic 创建加密、去重、增量快照，并提供保留、维护和恢复。
- 使用 rsync 执行明确的单向增量同步；rsync 不提供快照、保留或恢复语义。
- 保护本机或远程 Agent 上的目录，以及 MySQL、PostgreSQL 的逻辑备份。
- 支持本地目录、固定 SSH 主机密钥的 SFTP 和结构化 S3 仓库。
- 通过 TLS 1.3 mTLS、一次性注册令牌和短期租约管理远程 Agent。
- 提供运行记录、容量状态、告警、通知和审计记录。

## 支持平台

| 组件 | Linux amd64/arm64 | macOS Intel/Apple Silicon | Windows amd64/arm64 |
| --- | --- | --- | --- |
| 控制服务 | 支持 | 支持 | 不支持 |
| 远程 Agent | 支持 | 支持 | 支持 |

## 安装

### Linux 和 macOS 用户服务

```bash
curl -fsSL https://github.com/maboo-run/shadoc/releases/latest/download/install.sh | sh
```

安装脚本会下载当前稳定版的控制服务、Agent 制品和 `SHA256SUMS`，校验后调用 Shadoc 内置安装命令。安装完成后，按终端提示打开管理页面。

### Linux root system 服务

当控制服务需要读取系统范围或其他账号无权访问的源数据时，安装为 Linux root systemd 服务：

```bash
curl -fsSL https://github.com/maboo-run/shadoc/releases/latest/download/install.sh \
  | sudo env SHADOC_ALLOW_ROOT=1 sh
```

root 模式使用固定路径：

- 程序：`/var/lib/shadoc/app/shadoc`
- 数据目录：`/var/lib/shadoc`
- systemd 单元：`/etc/systemd/system/shadoc.service`

管理 root 服务：

```bash
sudo /var/lib/shadoc/app/shadoc status --system
sudo /var/lib/shadoc/app/shadoc start --system
sudo /var/lib/shadoc/app/shadoc restart --system
sudo /var/lib/shadoc/app/shadoc stop --system
```

root 服务拥有更大的文件访问权限，也会放大任务配置和主机安全问题的影响。管理页面仍是 HTTP；跨设备访问时请使用受信任的网络或经过认证的 HTTPS 反向代理。

### 将已有用户服务迁移为 root 服务

迁移前先保留控制面恢复包，然后执行：

```bash
SHADOC_BIN="${XDG_CONFIG_HOME:-$HOME/.config}/shadoc/app/shadoc"
sudo "$SHADOC_BIN" migrate-to-root
```

迁移会离线校验受管程序、复制数据、安装 root systemd 服务，并在健康检查成功后清理原用户服务。迁移失败时不会删除原用户实例。

## 快速开始

1. 打开安装时显示的管理地址。
2. 创建唯一的管理员账号。
3. 在兼容性中心确认 Restic、rsync 和数据库客户端。
4. 创建并验证仓库。
5. 创建备份任务，检查保护范围后启用任务。
6. 配置计划和仓库保留策略。
7. 手工运行一次任务，并用新目标进行恢复演练。

### 选择备份引擎

| 引擎 | 适用场景 |
| --- | --- |
| Restic | 需要快照、加密、保留、维护和恢复 |
| rsync | 需要单向增量同步，不需要快照和恢复 |

每个 Restic 任务独占一个仓库。不要把多个不相关的数据源放入同一个任务仓库。

### 远程部署 Agent 的数据目录

在“Agent 节点”页面通过已验证主机的 SSH 部署 Agent 时，可以填写 Agent 宿主机上的绝对路径作为“Agent 数据目录”。该目录保存 Agent 的凭据和运行状态；留空时使用用户默认目录。对于会让机械硬盘休眠的 NAS，建议把它设置到持续在线的 SSD，例如 `/volume1/docker/shadoc-agent`。该配置会写入 Agent 服务的 `--data-dir` 参数，并在重新部署、卸载和控制面恢复时保留。

这个目录只用于 Agent 自身数据，不会改变备份任务的源目录；源目录仍应按实际备份范围配置。

## 服务管理

用户服务的受管程序路径由安装脚本输出。常用命令：

```bash
# 替换为安装脚本输出的实际路径
SHADOC_BIN="/path/to/shadoc"
"$SHADOC_BIN" status
"$SHADOC_BIN" start
"$SHADOC_BIN" restart
"$SHADOC_BIN" stop
```

`stop` 只停止控制服务，不会删除任务、秘密、运行记录或备份仓库。root system 服务请使用上一节中的 `--system` 命令。

## 卸载

卸载服务和受管程序，但保留任务、秘密、运行记录和其他应用数据：

```bash
# 用户服务：替换为安装脚本输出的实际路径
"$SHADOC_BIN" uninstall-app

# root system 服务
sudo /var/lib/shadoc/app/shadoc uninstall-app --system
```

如果确认永久删除应用数据，追加 `--remove-data`。命令会要求输入 `REMOVE`：

```bash
# 用户服务
"$SHADOC_BIN" uninstall-app --remove-data

# root system 服务
sudo /var/lib/shadoc/app/shadoc uninstall-app --system --remove-data
```

卸载不会删除已经存在于本地、SFTP 或 S3 仓库中的备份内容。

## 安全边界

- 不提供任意 Shell、脚本、命令参数或环境变量执行入口。
- 仓库密码、SSH 私钥、数据库密码和通知令牌保存在本地加密秘密库中。
- SSH 主机密钥必须经过确认；不会静默接受未知或变化的主机密钥。
- 恢复、删除和长耗时管理动作包含预检、确认、管理员复验或可轮询状态。
- 管理页面不应直接暴露到公网。漏洞请通过 [SECURITY.md](SECURITY.md) 的私密漏洞报告渠道提交。

## 许可证

Shadoc 使用 [MIT License](LICENSE)。
