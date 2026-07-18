# Mission: 实现可安装的 Restic Web 控制服务

## Why
把 Gitea、AList 和服务器面板这类“安装后通过浏览器管理”的运行模型落地为影刻（Shadoc）：原生安装、长期无人值守、页面化管理并安全调用 Restic。

## Success looks like
- 单个 Go 二进制内嵌 React 页面，可注册为 macOS/Linux 用户服务
- 页面管理登录、兼容性、远端、仓库、任务、计划、维护、恢复、通知和审计
- 目录、MySQL 单库和 PostgreSQL 单库备份/恢复具有自动化测试与安全边界

## Constraints
- 首版原生支持 macOS 与 Linux，不以 Docker 为主要交付方式
- Go 后端内嵌 React 构建产物，Restic 作为受控执行引擎
- 日常设置只通过页面管理，不要求用户编写脚本或编辑应用配置文件

## Out of scope
- Docker 主交付方式、中央控制台、多用户/RBAC
- 物理热备、时间点恢复、覆盖非空数据库和任意 Shell 任务
