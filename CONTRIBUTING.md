# Contributing to All-in-One (`aio`)

感谢你关注并参与 `aio` 的开源建设！欢迎提交 Issue、Feature Request 与 Pull Request。

## 开发环境要求

- Go **1.26+**
- Git **2.30+**（需支持 `git worktree`）
- Linux 或 macOS（含 WSL2）

## 本地开发与验证

```bash
# 1. 格式化与静态检查
gofmt -s -w cmd/ internal/
go vet ./...

# 2. 运行全量并发竞争检测单元测试
go test -v -race -count=1 ./...

# 3. 本地静态构建并校验版本输出
make build
./aio version

# 4. 打包跨平台静态分发包（验证全平台编译）
make dist
```

## 项目目录概览

- `cmd/aio/`：CLI 入口、Daemon 启动、Hook 上报器与子命令
- `internal/adapter/`：各 Agent CLI 适配器、二进制三层探测与 Magpie 包装
- `internal/daemon/`：后台常驻 HTTP / WebSocket Server、状态机、Session 发现与清理、代理分流
- `internal/mobile/`：局域网手机扫码 Web 桥接、自签 TLS、设备 Session 管理与内嵌 H5 页面
- `internal/ptymgr/`：PTY 进程树管理、VT 终端仿真与宽字符/`CSI u` 兼容层
- `internal/quota/`：多 Agent 多池实时额度解析与静默探测
- `internal/store/`：`~/.aio/state.json` 与 `~/.aio/config.toml` 持久化
- `internal/tui/`：Bubble Tea TUI 工作台、控制中心、文件预览与终端色彩自适应
- `internal/version/`：构建版本与 VCS 元数据注入
- `internal/worktree/`：单仓与多仓组合 Git Worktree 隔离、Diff 与 Adopt 引擎

## Pull Request 规范

1. 保持提交清晰、职责单一；涉及状态机、PTY、Worktree 或 Mobile Bridge 的改动请同步补充对应单测。
2. 提交前确保 `gofmt -s`、`go vet ./...`、`go test -race -count=1 ./...` 全部通过。
3. 请勿提交任何个人绝对路径、内部域名、代理凭据或二进制产物。
4. 如有新增快捷键、配置项或子命令，请同步更新 [README.md](README.md)、[README_EN.md](README_EN.md) 与 [docs/USAGE.md](docs/USAGE.md)。
