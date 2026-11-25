# All-in-One (`aio`) Roadmap

本文档记录 `aio` 的公开演进规划与各阶段目标。欢迎在 [GitHub Issues](https://github.com/loucongV/all-in-one/issues) 提出建议或认领任务。

---

## v0.1.0（已发布 Pre-release）

- [x] 多 Agent 统一编排（Claude Code / Codex CLI / Antigravity / Cursor CLI / Kimi Code / OpenCode）与后台常驻 Daemon
- [x] 单仓 & 多仓组合 Git Worktree 隔离、横向 Diff 与 GitHub 风格一键采纳（Squash / Merge / Rebase）
- [x] 原生 Hooks + OSC 标题 + 屏幕状态兜底的多源精准状态监控
- [x] 统一控制中心（`,`）：CLI 三层探测与一键安装、Magpie 集成、按 Agent 分流代理、多池实时额度监控
- [x] 局域网手机扫码同步（HTTP/HTTPS 自签 TLS、语音输入、设备 Session 踢出管理、Android/iPhone 全屏）
- [x] `tmux` / MobaXterm / SSH 256 色自适应、VT 宽字符换行与 `CSI u` 兼容修复
- [x] 版本号元数据注入、`aio stop-daemon` 活跃会话保护与 GitHub Actions 自动发包

---

## v0.2.0（当前规划目标 — 旗舰工作台升级）

将以下 8 项核心能力按 4 个内部实施阶段逐步交付并合并至 `main`，全部完成后发布 `v0.2.0`：

### Phase 1：终端体验打磨 & 生态配置统一
- [ ] **1. UI 优化与各 SSH 终端深度适配**
  - 内置 `dark` / `light`（浅色终端适配）/ `high-contrast`（16 色友好）多套主题
  - 东亚模糊宽字符安全渲染模式（`safe_ascii` / 简框切换），彻底适配 MobaXterm / Xshell / FinalShell / PuTTY / WindTerm / iTerm2 / WezTerm / `tmux`
  - 新增 `aio doctor` 一键终端诊断命令（色彩、宽字符、OSC 52 剪贴板、鼠标、Git、CLI 与 Hooks 健康检查）
- [ ] **2. Magpie 可视化配置向导**
  - 直接在控制中心读写 `~/.config/magpie/`，提供常用 Provider 模板（OpenRouter / DeepSeek / 硅基流动 / 火山方舟 / Anthropic / OpenAI Compatible）与一键连通性测试
  - 新建任务时的 Magpie 路由升级为**下拉列表选择**已配置模型并支持保存 Preset
  - 支持一键开关 Magpie 隐私脱敏（Secret Masking）
- [ ] **3. Skill & MCP 统一管理**
  - **Skill 统一库**：集中管理 `SKILL.md`（本地导入 / GitHub URL 一键安装 / 预览 / 开关），自动软链同步到各 Agent 对应目录
  - **MCP 统一注册表**：一次配置 MCP Server（stdio / SSE / Streamable HTTP + Env），自动按各 Agent 方言（JSON / TOML）同步并支持健康探测与按项目启用
  - 估算已启用 Skills + MCP 占用的基础 Context Token 开销

### Phase 2：类 iTerm2 多窗格自由分屏
- [ ] **4. 内置类 iTerm2 递归分屏工作区**
  - 右侧工作区支持二叉树递归水平（左右）/ 垂直（上下）切分，不依赖宿主终端
  - 支持 **Agent + Shell 同屏**、**多 Shell 同屏**、**双 Agent 同屏赛跑**
  - 鼠标点击直切焦点、激活窗格亮色边框、调整分屏比例、单窗格一键临时最大化/还原
  - 后台 Daemon 支持单目录多 Shell 实例并发保活，断开 SSH 或重开 TUI 后分屏布局与进程原样恢复

### Phase 3：类 IDEA 代码变更审阅 & 公共资料库
- [ ] **5. 类 IDEA 代码变更查看（Review 闭环）**
  - TUI 升级为左侧可折叠变更文件树（`M`/`A`/`D`/`R` + `+N/-M`）+ 右侧 **Side-by-Side 左右双栏对比**（可切换 Unified），支持行号、词级差异高亮与 Hunk 跳转
  - 支持选中文件/变更块一键填写**评审意见直发对应 Agent 修改**，以及单文件快速回滚（Revert）
  - 新增本地浏览器富 Diff 视图（按 `o` 一键打开类 IDEA / GitHub 风格宽屏双栏 Diff，支持折叠、搜索与行内评论回发）
- [ ] **6. 公共资料库管理**
  - 全局维护公共资料库列表（本地目录 + 远程 Git 仓库一键/自动同步）
  - 支持项目级 / 任务级一键关联，通过各 CLI 原生 `--add-dir` 或只读软链（自动写入 `.git/info/exclude` 零 Git 污染）注入上下文
  - 支持 TUI 内置 Markdown 预览直接翻阅

### Phase 4：多 Agent 深度接力 & 跨机远程工作台
- [ ] **7. 跨 Agent 接力摘要（Handoff）**
  - 共享 Worktree 模式下，一键汇总前序 Agent 的改动 Diff、测试状态与关键结论生成结构化交接上下文，无缝喂给下一个 Agent（如架构设计 → 编码实现 → Code Review）
- [ ] **8. 消耗统计面板 & 跨机器远程 Daemon**
  - 统计面板：按项目 / 按天 / 按 Agent 汇总任务耗时、频次与额度消耗趋势
  - 跨机器远程 Daemon：本地一个 `aio` TUI 通过 SSH 隧道统一连接和管理多台开发服务器上的 `aio daemon`

---

## Backlog（v0.3.0+ / 社区共建）

- [ ] **数据库连接中心 & 只读 DB MCP**：统一管理项目数据库连接、内嵌拉起轻量数据库 TUI（如 `lazysql` / `usql`），并支持一键生成只读权限 Database MCP 给 Agent 排障查库
- [ ] **外部通知与 Webhook 推送**：macOS / Linux 桌面系统通知，以及飞书 / 钉钉 / 企业微信 / Telegram / Slack Webhook 推送
- [ ] **自动验收门禁（Verify Gate）**：按 `.aio.toml` 定义的测试/Lint 命令自动验收 Agent 改动，失败时自动将报错喂回给 Agent 修复
- [ ] **自定义 Agent Adapter 插件**：通过 TOML 配置接入任意新的 CLI Coding Agent
