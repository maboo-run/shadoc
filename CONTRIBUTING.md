# 参与贡献

感谢参与 Shadoc。提交改动前，请先阅读 `MISSION.md`、`CONTEXT.md`、相关源码和测试。当前实现与旧说明冲突时，以源码和测试为准。

## 开发环境

- Go 1.24
- Node.js 22
- pnpm 10

```bash
pnpm install
make test
make build
```

最终二进制、真实浏览器和真实外部工具验收：

```bash
make test-e2e
```

涉及并发、租约、取消或锁的改动还应运行：

```bash
go test -race ./...
```

## 提交要求

- 不增加任意 Shell、用户脚本、任意命令参数或任意环境变量执行能力。
- 秘密只能进入 purpose-bound 的秘密库，不得写入日志、审计、操作详情或租约。
- SSH/SFTP 必须固定并校验主机密钥。
- 恢复、删除和 rsync `delete` 的安全门禁不得为了简化交互而绕过。
- 用户可见静态文案必须补齐中英文映射。
- 前端变化后必须重新构建并提交 `internal/webui/dist`。
- 修复缺陷时先增加能够复现问题的测试。
- 不提交真实数据目录、数据库、密钥、日志、诊断包或本机配置。

提交前至少运行：

```bash
make test
git diff --check
git status --short
```

安全漏洞请使用 `SECURITY.md` 中的私密渠道，不要提交公开 Issue 或 Pull Request。
