# 移植对照表

源项目：`/mnt/mac/DS-mon`（只读参考，不在新项目里修改）。
原则：**测试是规格**。每个 `Port` 项先把它对应的 Swift 测试翻译成 Go 测试，再写实现。

处置说明：

- `Port`：语义基本不变，换语言重写。
- `Adapt`：概念保留，实现方式因平台/框架改变。
- `Drop`：不做。
- `Ref`：只作为设计参考，不产生代码。

## Core — 数据与计费

| 源文件 | 处置 | 目标 / 说明 |
|---|---|---|
| `UsageStore.swift`、`UsageStore+Queries.swift`、`UsageStore+Meta.swift`、`UsageStore+Maintenance.swift` | Port | `internal/store`：schema、插入、聚合、清理、meta |
| `UsageStore+Balance.swift` | Port（只保留采样） | `internal/store` + `internal/balance` |
| `UsageStore+Pricing.swift` | Port | `internal/store` + `internal/pricing`：修订时间轴 |
| `UsageModels.swift` | Port | `internal/store/model.go` |
| `UsageLogger.swift` | Adapt | 写队列；日志改 `slog` |
| `UsagePeriod.swift`、`UsageCalendar.swift`、`PeriodComparison.swift` | Port | `internal/store` 的区间与对比计算 |
| `PricingStrategy.swift`、`ModelPricing.swift`、`PricingTimeline.swift`、`PricingRuleUpdate.swift` | Port | `internal/pricing` |
| `PricingPageExtractor.swift` | Port | HTML → 文本，用 `golang.org/x/net/html` |
| `PricingUpdateService.swift`、`PricingUpdateSchedule.swift` | Port | 模型转换、差异、7 天排期 |
| `BalanceService.swift` | Port（删审计部分） | `internal/balance`：URL、鉴权、状态码映射、轮询 |
| `BalanceAudit.swift`、`SpendDrain.swift`、`SpendAlertLifecycle.swift` | Drop | 对账/盗刷不做 |
| `SecureStore.swift` | Adapt | AES-GCM；密钥文件 0600；crypto/aes + cipher.GCM |
| `AppSettings.swift` | Adapt | `internal/config`：TOML + 默认值 + 范围钳制 |

## Core — 代理与采集

| 源文件 | 处置 | 目标 / 说明 |
|---|---|---|
| `ProxyServer.swift` | Adapt | `internal/proxy`：`net/http.Server`，放弃 NWListener |
| `ProxyConnectionHandler.swift` | Adapt | 转发 + 采集；手写 `http.Handler` + `RoundTrip`，便于限制、flush 与旁路分析 |
| `ClientResponseRelay.swift`、`ClientSendTracker.swift` | Adapt | Go 的 `io.Copy` / `io.TeeReader` 替代 |
| `ProxyRequestFraming.swift` | Port | 保留限制判断逻辑；Go 原生支持 chunked |
| `ProxyResponseHead.swift` | Port | **压缩头规则必须原样保留**，见 `docs/ARCHITECTURE.md` |
| `ResponseBodyCollector.swift` | Port | SSE 累积、用量提取 |
| `UpstreamRequestBuilder.swift`、`UpstreamProtocol.swift` | Port | 路由、鉴权改写、协议判定 |
| `RateLimiter.swift` | Port | RPM 限流 |
| `ConnectionWatchdog.swift` | Adapt | `http.Server` 超时 + 并发计数 |
| `DeepSeekTools.swift` | Port | Search/Fetch 计费与识别 |
| `InlineImageOffload.swift`、`LargeImageOffload.swift` | Adapt | SHA-256 用标准库；上传逻辑保留 |
| `AppResources.swift` | Drop | 用 `//go:embed` 替代 |
| `Constants.swift` | Adapt | 路径、端口、超时集中到 `internal/config` |

## Core — 曲线与状态

| 源文件 | 处置 | 目标 / 说明 |
|---|---|---|
| `ActivityTrace.swift` | Port | `internal/proxy/trace`：3600 × 2s，绝对时间对齐，按画布宽度取可见窗口 |
| `TrafficTrace.swift` | Port | `internal/proxy/trace`：保留 3600 × 2s（2 小时），按画布宽度取可见窗口；上下行、量程、棘轮、迟滞 |
| `ActivityCurveFrame.swift` | Adapt | `internal/tui/chart`：保留 bars/shift/grid/health，删 alpha/呼吸 |
| `StatusBarMetrics.swift` | Adapt | 只保留曲线常量：桶宽/桶数/满量程/网格间距 |
| `ActivityMeter.swift` | Adapt | 只保留 `idleHealth` 的健康判定；去掉回落/呼吸/光晕 |
| `IconSpin.swift`、`GaugeNeedle.swift`、`GaugeIdleFidget.swift` | Adapt | `packaging/omarchy/cherno.dsmon`：保留 siren/warning/gauge 三格、0.45 秒/格 smoothstep、10 秒仪表、240° 指针、圆弧内暖橙辉光和空闲双拍轻抬 |
| `IconTrace.swift`、`IconTraceRenderer.swift` | Drop | Omarchy 栏不再显示活动线；TUI 状态使用独立曲线面板 |
| `StatusBarController.swift`（图标状态） | Adapt | `dsmon status --json` 提供最后活动时间，Omarchy QML 使用 Mac 仪表/警报 PNG 和指针动画 |
| `TrafficCurveRenderer.swift`、`ActivityCurveRenderer.swift` | Ref | 画法参考；Go 版用 Braille 重写 |
| `ActivityHeat.swift` | Drop | 热力图不在 v1 |
| `UpstreamGapMeter.swift` | Drop | 菜单栏状态用，TUI 不需要 |
| `TokenMilestone.swift`、`TokenMilestoneWatcher.swift` | Drop | 庆祝烟花不做 |

## Core — 客户端与模型标识

| 源文件 | 处置 | 目标 / 说明 |
|---|---|---|
| `ClientIdentity.swift` | Port | UA → 客户端名归一化、规则版本、隐藏 xctest |
| `ModelIdentity.swift` | Port | 已废弃别名的显示合并（按日期） |
| `Strings.swift` | Adapt | 只保留 TUI 用到的中/英文案，改 Go map |

## UI — 全部丢弃，仅作参考

`Sources/DSmon/UI/` 下 42 个 Swift 文件全部 `Drop`。其中以下文件有设计参考价值：

| 文件 | 参考点 |
|---|---|
| `StatsPopoverView.swift` | 概览卡片的指标选择 |
| `UsageWindowView.swift`、`UsageCharts.swift` | 用量页的布局与图表分组 |
| `PricingSettingsView.swift` | 计费页的字段与差异展示 |
| `GeneralSettingsView.swift` | 设置项分组 |
| `SideNotchView.swift`、`SideNotchReadings.swift` | 曲线面板与五格数字的排版 |
| `StatusBarView.swift` | 色彩与状态语义（不要照搬绘制） |

`DSmonApp.swift` / `AppDelegate` 由 Go 的 `cmd/dsmon` + systemd 取代。

## 资源

| 资源 | 处置 |
|---|---|
| `pricing_strategy.json` | 已复制到 `internal/pricing/pricing_strategy.json`；只改了 `comment` 里的覆盖路径（`~/.config/dsmon/`），价格数据逐字一致 |
| `Assets.xcassets`、`dslogo*.png`、`menu_icon_*.png` | Drop | TUI 用字符绘制同类状态，不打包桌面图标资产 |

## 测试

### 直接移植（纯逻辑）

| 源测试 | 目标包 |
|---|---|
| `UsageStoreTests`、`UsageRangeTests`、`ClientUsageTests`、`UsageCalendarTests` | `internal/store` |
| `PricingStrategyTests`、`PricingTimelineTests`、`PricingEffectiveDateTests`、`ModelPricingTests`、`PricingUpdateTests`、`PricingAutoCheckTests` | `internal/pricing` |
| `BalanceServiceTests`（取数/状态码部分） | `internal/balance` |
| `ClientIdentity` 相关断言、`ModelIdentityTests` | `internal/store` / `internal/proxy` |
| `ProxyRequestFramingTests`、`ProxyResponseHeadTests`、`ResponseBodyCollectorTests`、`UsageExtractionTests` | `internal/proxy` |
| `UpstreamRequestBuilderTests`、`UpstreamProtocolTests`、`DeepSeekToolsTests`、`RateLimiterTests` | `internal/proxy` |
| `ActivityTraceTests`、`TrafficTraceTests` | `internal/proxy/trace` |
| `AppSettingsTests`（默认值/钳制部分） | `internal/config` |

### 改写

| 源测试 | 说明 |
|---|---|
| `ProxyRequestCFHTTPTests` | 改成 Go `http.Request` 的分片/边界测试 |
| `ActivityCurveRendererTests`、`TrafficCurveRendererTests` | 改成字符矩阵 golden 测试 |
| `StatusBarMetricsTests` | 只保留曲线几何相关断言 |
| `PopoverSizeTests` | Drop（无 popover） |

### 丢弃

`BalanceAuditTests`、`SpendDrainTests`、`SpendAlertLifecycleTests`、`SideNotch*Tests`、
`StatusBar*Tests`、`Icon*Tests`、`GaugeNeedleTests`、`BreathingLightTests`、
`AlarmToneTests`、`TokenMilestoneTests`、`ActivityHeatTests`、`UsageDonutTests`、
`TooltipPlacementTests`、`UsageTooltipPlacementTests`、`ModuleBoundaryTests`、
`ViewScreenshot` 相关。

## 移植流程

1. 打开源文件，先读它对应的测试，把测试里的输入/期望值抄成 Go 表驱动用例。
2. 写 Go 实现，跑测试；不一致时以测试为准，除非能证明测试依赖了 macOS 专有行为。
3. 涉及数据库的用例，用临时目录建库，不碰用户数据。
4. 涉及时间的用例，时钟必须可注入（`now func() time.Time` 或显式参数）。
5. 每完成一个包，在 `docs/PLAN.md` 勾掉对应交付项。

