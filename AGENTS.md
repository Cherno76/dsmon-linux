# AGENTS.md — dsmon-linux

## 项目定位

Linux 原生重写版 DS-mon。Go + bubbletea TUI + SQLite。当前处于规划/脚手架阶段。

源项目在 `/mnt/mac/DS-mon`，**只读参考，不要修改**。移植规则见 `docs/PORTING.md`。

## 语言与工具链

- Go 1.24+，标准库优先。
- 构建：`make build`；测试：`make test`；格式：`make fmt`；静态检查：`make vet`。
- 依赖上限：bubbletea/lipgloss/bubbles、modernc.org/sqlite、golang.org/x/net/html、
  BurntSushi/toml。新增依赖前先写清理由。

## 硬约束

1. **不做对账/盗刷**：不要引入 `BalanceAudit`、`SpendDrain`、警报等级、放行/静音流程。
2. **不做刘海/菜单栏**：曲线画在 TUI 里，参数见 `docs/CURVES.md`。
3. **数据库 schema 必须与 Mac 版兼容**：`internal/store/schema.sql` 的表名、列名、索引不能
   随意改；要改就写迁移并在 `docs/ARCHITECTURE.md` 记录。
4. **代理的压缩/SSE 规则不能简化**：见 `docs/ARCHITECTURE.md` 的“压缩与 SSE”一节。
5. **计费更新必须人工确认**：模型输出不能直接写 `pricing_revision`。
6. **单一写库者**：所有写操作进 store 的写 goroutine，不要在 HTTP handler 里直接写。
7. **只监听 127.0.0.1**，密钥不落明文，日志不打印 key/Authorization/正文。

## 代码风格

- `gofmt` 必须干净；错误用 `fmt.Errorf("...: %w", err)`。
- 标识符英文，注释中文；注释解释“为什么”，不解释“是什么”。
- 纯逻辑（时间桶、量程、计费、归一化）不依赖终端、网络、全局时钟；时间作为参数传入。
- 表驱动测试；数据库测试用 `t.TempDir()`；不要 mock SQLite。

## 移植规则

- 先读 Swift 测试，再写 Go 测试，最后写实现。
- 只移植语义，不逐行翻译 Swift 的 UI 代码。
- 如果源实现有平台相关的取巧（例如 URLSession 解压后改头），在 Go 里用更直接的方案，
  但必须保留对应的回归测试。

## 桌面集成

- 设置快捷键写在 `~/.config/hypr/bindings.lua`，用 `o.bind(...)`，改完 `hyprctl reload`。
- 修改 Hyprland/系统配置前先按 omarchy skill 的规则确认格式。
- 通知用 `notify-send`；不要引入常驻通知服务。
- 不要自动重启用户的桌面会话或终端。

## 提交与协作

- 默认**不自动 commit / push**，除非用户明确要求。
- 提交信息：英文 subject 带类型前缀（feat/fix/tweak/refactor/chore），正文可中英混排，
  写清为什么改、踩过的坑、量到的数字。

## 版本号

- 每次功能或修复修改完成后运行 `make bump-version`，只增加 patch，例如 `0.1.1 → 0.1.2`。
- 版本格式固定为 `major.minor.patch`，每一位范围 `0–99`；patch/minor 到 99 时向高位进位，
  `99.99.99` 无法继续递增。
- `internal/version/VERSION` 是唯一版本源，TUI、设置页和 `dsmon version` 都从它读取。

## 验证分级

- **低风险视觉微调**（颜色、边框、间距、网格开关、文案、面板布局；不碰数据、时序、量程、
  代理和存储）：只对改动文件跑 `gofmt -w`，再 `make build` / `make install`，由用户肉眼验收；
  不需要 `make vet` 或 `make test`。
- **常规改动**：跑受影响包的测试，例如 `go test ./internal/tui`。
- **高风险改动**（proxy/store/schema/pricing、并发、协议、曲线桶参数）：跑
  `make fmt && make vet && make test`。
- 全量测试主要用于提交前和跨层改动，不用于每次视觉微调。快速验证失败或边界不确定时，
  自动升级到上一级验证。

## 文档

- 新决定追加到 `docs/DECISIONS.md`，不要改写历史条目。
- 阶段进度更新 `docs/PLAN.md` 的勾选项。
- 曲线参数改动必须同步 `docs/CURVES.md` 和测试。

