# All-in-One (`aio`) 详细使用指南

本文档详细介绍 `aio` 的日常工作流、高级功能、配置文件与跨终端使用技巧。

---

## 目录

1. [基础概念与架构](#1-基础概念与架构)
2. [CLI 命令行参考](#2-cli-命令行参考)
3. [项目、任务与多 Agent 对比工作流](#3-项目任务与多-agent-对比工作流)
4. [Session 历史绑定与清理](#4-session-历史绑定与清理)
5. [控制中心（`,`）全功能详解](#5-控制中心全功能详解)
6. [局域网手机扫码同步与语音输入](#6-局域网手机扫码同步与语音输入)
7. [配置文件参考（`~/.aio/config.toml` & `.aio.toml`）](#7-配置文件参考aioconfigtoml--aiotoml)
8. [SSH / MobaXterm / `tmux` 最佳实践](#8-ssh--mobaxterm--tmux-最佳实践)
9. [常见问题排查（FAQ）](#9-常见问题排查faq)

---

## 1. 基础概念与架构

`aio` 采用 **客户端 / 守护进程（Client-Daemon）** 分离架构：

- **后台 Daemon（`aio daemon`）**：监听本机 Unix Socket（默认 `~/.aio/daemon.sock`），统一托管所有 Agent CLI 与独立 Shell 的 PTY 进程、虚拟终端缓冲（10,000 行回看）、实时状态机、额度采集器以及按需开启的手机局域网桥接服务。
- **交互式 TUI（`aio`）**：轻量前端界面，随时退出（`q`）或 SSH 断线均不影响后台运行的 Agent。
- **数据目录（`~/.aio/`）**：
  - `state.json`：持久化的项目、任务、Run 记录与已绑定 Session ID；
  - `config.toml`：全局用户配置（代理、工具路径、退出快捷键、手机端口、语音转写等）；
  - `worktrees/`：默认 Git Worktree 隔离目录；
  - `daemon.log`：后台守护进程运行日志。

可通过环境变量 `AIO_HOME` 自定义数据目录路径。

---

## 2. CLI 命令行参考

```bash
# 启动 TUI（若后台 daemon 未启动则自动拉起）
aio

# 以原生终端鼠标模式启动（方便 SSH 终端直接鼠标拖选与右键粘贴）
aio --no-mouse

# 查看客户端版本与运行中的 daemon 版本（支持 -v / --version / version）
aio version

# 扫描并显示本机检测到的 Agent CLI 与路径
aio agents

# 输出当前所有项目、任务、Agent 会话状态及实时额度
aio status

# 查看本地缓存额度；加 --probe 立即后台静默探测最新 /usage 额度
aio quota
aio quota --probe

# 优雅停止后台 daemon（检测到有运行中/已打开会话时会列出清单并交互确认）
aio stop-daemon
# 脚本环境跳过确认强制停止
aio stop-daemon --force
```

---

## 3. 项目、任务与多 Agent 对比工作流

### 3.1 添加工作目录（`p`）

按 `p` 打开内置目录浏览器：
- 使用 `↑/↓`、`Enter`、`Backspace` 浏览目录，或按 `Tab` 切换手动输入路径；
- 按 `N` 可直接在当前路径下新建文件夹（可选同时 `git init`）；
- `aio` 会自动识别 3 种工作区类型：
  - **Git 仓库（`Git`）**：完整支持本地直连、共享 Worktree、独立对比 Worktree 与分支采纳；
  - **多仓目录（`多仓`）**：当目录下包含多个子 Git 仓库时自动识别，创建任务时为所有子仓生成同步分支的复合 Worktree，并将顶层文档（如 `CLAUDE.md`、`docs/`）软链进去；
  - **普通目录（`目录`）**：直连模式运行 Agent，不强依赖 Git。

### 3.2 新建任务与工作模式（`n` / `+`）

在选中项目后按 `n` 新建任务：
- **单 Agent 模式**：勾选 1 个 Agent。
- **多 Agent 对比模式（Compare Mode）**：一次勾选多个 Agent（例如同时选 `Claude Code` + `Codex CLI`），输入同一段 Prompt 并发执行。
- **工作区模式**：
  - `local`：直接在原工作目录运行；
  - `shared`：创建单个共享 Worktree 分支，后续按 `+` 向该任务追加其他 Agent 时自动复用同一目录与分支，实现**多 Agent 无缝接力**；
  - `isolated`：每个 Agent 拥有独立 Worktree 与独立分支（`aio/<slug>/<agent>`），互不干扰。
- **权限预设**：支持 `default` / `auto` / `plan` / `yolo`（对应各 CLI 原生权限参数）。
- **Magpie 路由**：若已安装 Magpie，可在新建任务时直接填入 `provider/model` 路由规则。

### 3.3 横向 Diff 对比与一键采纳（`d` / `a`）

- 按 **`d`** 打开横向 Diff 对比面板，用 `←/→` 切换同一任务下不同 Agent 的改动文件与完整 Patch，`↑/↓` 滚动查看；
- 确认胜出方案后按 **`a`** 进入采纳（Adopt）弹窗，选择合并策略：
  - **`Squash`**（推荐）：将胜出分支压缩为一条干净的提交合并回主分支；
  - **`Merge`**：保留完整分支合并记录；
  - **`Rebase`**：变基合入目标分支。
- 采纳成功后，`aio` 会自动清理该任务下所有临时 Worktree 与临时分支（多仓模式下仅合并有实际改动的子仓，未改动的子仓即使原目录有未提交修改也完全不受影响）。

### 3.4 对话中快速切换终端（`Ctrl+\` & 双击 `Esc`）

- 与 Agent 对话（`TERM` 模式）时：
  - 按 **`Ctrl+\`** 立即打开当前工作区（或当前 Worktree）的持久化 Shell 终端跑测试/查日志，再按一次 **`Ctrl+\`** 瞬间切回刚才的 Agent 对话；
  - 连按两下 **`Esc`**（220ms 缓冲防误断，不会向 Agent 发送中断信号）或按 **`Ctrl+]`** 退回左侧导航；
  - 单按一下 **`Esc`** 仍会正常透传给 Agent（用于取消生成、关闭补全菜单等）。

---

## 4. Session 历史绑定与清理

- 每次启动或恢复会话时，`aio` 会自动捕获并持久化各 Agent 的底层 Session ID（Claude `--session-id` / Codex rollout UUID / Cursor chat ID / Antigravity conversation ID）。
- 在导航模式下选中某个 Agent 按 **`r`** 打开 Session 管理弹窗：
  - 浏览自动扫描到的本地历史 Session（优先标注 `[本目录]` 与当前已绑定 `★`）；
  - 按 **`Enter`** 立即绑定并恢复进入终端；
  - 按 **`b`** 仅绑定 Session ID 不重启当前进程；
  - 也可选择「开启全新空白会话」「手动粘贴 Session ID」或「打开 CLI 原生选择器」。
- 删除任务或 Agent（`x`）时，弹窗提供勾选开关，可选是否**同步清理磁盘上的本地 CLI 历史会话文件**。

---

## 5. 控制中心（`,`）全功能详解

在导航模式下按 **`,`** 呼出统一控制中心，按数字键 `1` ~ `7` 直达：

| 编号 | 功能 | 说明 |
| :---: | :--- | :--- |
| `1` | **CLI 工具管理与一键安装** | 精准探测所有 Agent 与 Magpie；按 `Enter` 一键安装（支持后台运行与按 `, → 1` 随时回到安装输出，经代理失败自动直连重试），按 `b` 手动指定二进制路径，按 `l` 查看上次安装日志 |
| `2` | **Magpie 模型路由网关** | 一键打开嵌入式 `magpie tui` 面板（未安装时自动定位到安装项） |
| `3` | **网络代理与按 Agent 分流** | 切换 `auto` / `manual` / `direct`，支持实时连通性探测（`t`），以及按 Agent 勾选**直连白名单**（例如国内 Agent 走直连，海外 Agent 走代理） |
| `4` | **手机局域网扫码同步** | 启动/停止局域网桥接、生成终端二维码、切换多网卡 IP、切换 HTTP/HTTPS、管理已登录手机设备 Session |
| `5` | **实时额度监控详情** | 查看当前活跃 Agent 的详细多池剩余额度、重置倒计时，按 `r` 后台静默探测，按 `s` 向当前会话发 `/usage` |
| `6` | **SSH 原生鼠标模式** | 临时释放鼠标捕获给宿主终端（方便 MobaXterm/PuTTY 直接右键粘贴），也可在任意模式下按 `F12` 切换 |
| `7` | **刷新工作台状态** | 立即重新扫描本机 CLI、会话与额度 |

---

## 6. 局域网手机扫码同步与语音输入

### 6.1 扫码连接与设备 Session 管理

1. 在电脑端按 **`, → 4`** 打开「手机扫码同步」，确保手机与电脑在同一局域网（如有多块网卡，按 `←/→` 切换到手机可达的局域网 IP；WSL2 用户若无法访问，请确保已放行 Hyper-V 防火墙或开启 mirrored 网络模式）。
2. 每台扫码登录的手机会获得独立的 Session 密钥与记录，电脑端弹窗实时显示设备类型（如 `iPhone · Safari`、`Android · Chrome`）、IP、在线状态与最近活跃时间：
   - `↑/↓` 选中设备，按 **`x`** 立即踢出单个设备（实时断开其 WebSocket，不影响其他手机）；
   - 按 **`X`** 注销全部已登录设备；
   - 按 **`r`** 刷新二维码 Token 并重置全部会话；
   - 按 **`s`** 停止局域网服务。

### 6.2 HTTP vs HTTPS 与手机全屏

- **HTTP 模式（默认）**：扫码直开免信任；推荐 **iPhone「添加到主屏幕」** 使用该模式以获得无地址栏/无底栏真全屏（iPhone 键盘自带麦克风听写照常可用）。
- **HTTPS 模式（按 `h` 切换）**：自动生成带局域网 IP SAN 的自签 ECDSA 证书，解锁手机浏览器内置实时语音（Web Speech API）；首次访问需在浏览器点击「高级 → 继续访问」。
- **浏览器全屏**：
  - **Android（Chrome / Edge / Firefox）**：点右上角全屏图标直接隐藏浏览器地址栏与底栏；
  - **iPhone（Safari）**：点底部 **分享 → 添加到主屏幕** 后从桌面 AIO 图标打开即为无地址栏真全屏（或在 Safari 内点地址栏左侧 `大小` → `隐藏工具栏`）。

### 6.3 可选：配置服务端语音转写

若希望在所有手机浏览器下使用录音转写（例如接本地 Whisper / 云端 STT），可在 `~/.aio/config.toml` 配置 `voice_transcribe_cmd`，`{file}` 会自动安全替换为临时音频文件路径，命令标准输出即为识别文本：

```toml
voice_transcribe_cmd = "whisper --model small --language zh --output_format txt {file}"
```

---

## 7. 配置文件参考（`~/.aio/config.toml` & `.aio.toml`）

### 7.1 全局配置：`~/.aio/config.toml`

所有字段均可选，未配置时使用合理默认值：

```toml
# 新建任务时的默认 Agent 顺序
default_agents = ["claude", "codex", "agy", "cursor", "kimi", "opencode"]

# Git Worktree 默认存放目录
worktree_root = "/path/to/.aio/worktrees"

# 从 TERM 模式退回 NAV 模式的快捷键（默认 ctrl+]）
escape_key = "ctrl+]"

# 默认 Adopt 合并策略：squash | merge | rebase
default_merge = "squash"

# 手机局域网同步默认端口（默认 19820，冲突时自动分配空闲端口）
mobile_port = 19820

# 可选：服务端语音转写命令（{file} 会通过环境变量安全传递）
# voice_transcribe_cmd = "your-stt-cli {file}"

# 可选：自定义 Magpie 二进制路径
# magpie_binary = "/usr/local/bin/magpie"

[proxy]
# auto（跟随系统环境） | manual（手动指定） | direct（全局直连）
mode = "auto"
http_proxy = ""
https_proxy = ""
no_proxy = "localhost,127.0.0.1"
# 指定直连不走代理的 Agent（例如 kimi、magpie）
direct_agents = ["kimi"]

# 可选：手动指定某些 Agent 的二进制路径
[agent_binaries]
# claude = "/custom/path/to/claude"

# 可选：自定义外部额度查询命令
[quota_commands]
# claude = "your-quota-script"
```

### 7.2 项目级 Worktree 钩子：`.aio.toml`

在 Git 仓库根目录放置 `.aio.toml`，每次为该仓库创建隔离 Worktree 时会自动执行文件拷贝、软链与初始化命令：

```toml
[worktree]
# 从主仓库复制到新 Worktree 的未跟踪文件
copy = [".env", ".env.local"]

# 从主仓库软链到新 Worktree 的大目录（节省磁盘与安装时间）
symlink = ["node_modules"]

# 创建 Worktree 后在目标目录自动执行的初始化命令
setup = ["echo worktree ready"]
```

---

## 8. SSH / MobaXterm / `tmux` 最佳实践

- **在 `tmux` 中运行 `aio`**：`aio` 会自动检测 `TMUX` / `TERM=screen*` / `TERM=tmux*` 并启用稳定的 256 色输出（同时将右侧 PTY 内的 TrueColor 下采样为 256 色），无需修改 `~/.tmux.conf` 也不会丢失色彩。如果你的 `tmux` 已开启真彩（`set -as terminal-features ",*:RGB"`），可通过 `AIO_COLOR=truecolor aio` 手动开启 24-bit 真彩。
- **在 `aio` 的工作区终端（`Ctrl+\` / `t`）中连接已有 `tmux`**：`aio` 会自动剥离外层 `TMUX` 环境变量避免嵌套冲突；建议使用 **`tmux a -d`**（自动踢掉其他较小窗口客户端），避免右侧出现 `tmux` 小窗口点阵填充。
- **MobaXterm / PuTTY 右键粘贴**：按 `F12`（或在启动时加 `aio --no-mouse`）即可临时释放鼠标给宿主终端右键粘贴，再按 `F12` 恢复滚轮回看与拖选复制。
- **自定义色彩级别**：支持环境变量 `AIO_COLOR=truecolor|256|ansi|none` 以及标准 `NO_COLOR=1`。

---

## 9. 常见问题排查（FAQ）

**Q: 升级新版 `aio` 二进制后，部分 Daemon 功能还没生效？**
- 当后台仍有活跃任务时，新版 TUI 启动会主动保留旧 Daemon 避免打断任务（`aio version` 和底部栏会提示旧版本 Daemon）。等运行中的任务完成后，执行 `aio stop-daemon && aio` 即可无损升级（已有二次确认保护，不怕误杀）。

**Q: `aio` 提示某个已安装的 CLI 未检测到？**
- 按 `, → 1` 打开 CLI 工具管理，按 `r` 重新扫描，或选中对应工具按 **`b`** 手动指定二进制路径（配置会自动持久化到 `~/.aio/config.toml`）。

**Q: 手机扫码后无法打开页面？**
- 确认手机与电脑在同一局域网；在 `, → 4` 中按 `←/→` 切换到真实的 Wi-Fi / 局域网 IP（避免选中 Docker/虚拟网卡 IP）；若使用 Windows + WSL2，需放行 Windows 防火墙对应端口（默认 `19820`）。

**Q: 查看后台运行日志？**
- 后台 Daemon 日志位于 `~/.aio/daemon.log`。
