# 实施计划

## 总体目标

用 Go 做一个 Linux 常驻代理 + TUI 仪表盘，替代 macOS 版 DS-mon 的核心用途：
看住本地 AI 客户端的用量、花费和余额；不做菜单栏、刘海、对账和盗刷检测。

## 里程碑

| 阶段 | 内容 | 全职估计 | 验收 |
|---|---|---|---|
| 0 | 脚手架、schema、定价 JSON、文档 | 0.5–1 天 | `go build ./...` / `go test ./...` 通过 |
| 1 | 数据层（SQLite + 计费时间轴） | 3–4 天 | 导入 Mac 库后聚合数字对得上 |
| 2 | 代理 + 用量采集 + 两张曲线的数据源 | 5–7 天 | 客户端走代理，库里出现记录，曲线有数据 |
| 3 | 余额查询与显示数据 | 1.5–2 天 | 余额 60 秒刷新，失败原因可见 |
| 4 | TUI 仪表盘（含盲文曲线） | 7–9 天 | 手工清单全部通过 |
| 5 | 设置 TUI + 快捷键 | 4–5 天 | 快捷键打开、改动 1 分钟内生效 |
| 6 | 计费更新管道 | 3–4 天 | 真实页面跑通，未确认不写库 |
| 7 | 打包与 systemd | 2 天 | 开机自启、重启存活、卸载干净 |

总计约 4–6 周全职；兼职按 ×1.5 估。

---

## Phase 0 — 脚手架（已完成）

- [x] 新仓目录、`go.mod`、`Makefile`、`.gitignore`
- [x] `internal/store/schema.sql`（与 Mac 版同构）
- [x] `internal/pricing/pricing_strategy.json`（从源项目复制）
- [x] 文档：计划、决策、架构、曲线规格、移植对照
- [x] 安装 Go 并跑通 `go build ./...`（当前用用户目录 `~/.local/opt/go1.27.3`，不依赖系统包）
- [x] `git init`

## Phase 1 — 数据层（已完成）

交付：

- `internal/store`：Open（WAL、`busy_timeout=2000`）、建表、插入（去重索引）、删除 180 天前数据、
  全部聚合查询、`meta` 读写、余额样本、计费修订。
- `internal/pricing`：规则加载、峰谷判定、最长前缀匹配、修订时间轴、未知模型补算。
- `dsmon import <usage.db>`：复制/校验/打开外部库，必要时把旧数据并入当前库。

当前进度：

- [x] `store.Open`：WAL、`busy_timeout=2000`、最终 schema、Mac 旧库补列
- [x] 单写 goroutine：插入、余额样本、meta、计费修订、维护/清理都走同一队列
- [x] 插入去重、180 天保留期清理、旧库历史费用不被重算
- [x] 窗口聚合、小时/日/客户端分桶、菜单栏快照、区间总计、分系列数据
- [x] 客户端 UA 归一化与无 UA 历史按协议反推
- [x] `pricing` 规则加载、覆盖文件 fallback、峰谷/周末、最长前缀、费用公式、修订时间轴
- [x] 新规则按生效时刻导入，只重算生效之后的行；未知模型后续补算
- [x] `dsmon import [--into <path>] <usage.db>`：新目标整库复制，已有目标合并/去重
- [x] `UsagePeriod` / `UsageCalendar` / `ModelIdentity` 的纯逻辑移植
- [x] 导入器回归：旧 schema 补列、源库 WAL、修订重映射、重复导入、源库不被修改
- [x] 用真实 Mac 库做 3 个时间范围 × 2 个分组维度的对账

移植测试（按源文件）：

- `UsageStoreTests`、`UsageRangeTests`、`ClientUsageTests`
- `PricingStrategyTests`、`PricingTimelineTests`、`PricingEffectiveDateTests`、`ModelPricingTests`
- `UsageCalendarTests`、`ModelIdentityTests`、`ClientIdentity` 相关断言

验收：

- `go test ./internal/store/... ./internal/pricing/...` 全绿。
- 用一份真实 Mac `usage.db` 导入后，今日/7 天/30 天的 token、请求数、费用与 Mac 版界面一致
  （人工抽查 3 个时间范围 × 2 个分组维度）。
- 改一条计费修订，只有它生效之后的行走新价，历史行一分不变。

真实库对账（2026-11-07 Mac 运行快照）：

- 导入：108,899 行用量、38,742 条余额样本、33,800,534,869 total tokens、¥2,085.950697；
  复制导入后逐项与源库一致，源文件哈希不变。
- 今日：105 请求 / 18,216,332 tokens / ¥0.975357；模型、客户端各 2 个系列。
- 近 7 天：5,388 请求 / 1,938,797,197 tokens / ¥66.107988；模型 3 个、客户端 4 个系列。
- 近 30 天：32,105 请求 / 10,875,608,694 tokens / ¥445.743253；模型 4 个、客户端 9 个系列。
- 同一源库再次导入：inserted 0、skipped 108,899、0 条新修订、0 条新余额样本。

对账以 Mac 版写入 `usage.db` 的数据为基准（逐系列比较请求数、命中/未命中/输出 token 与费用），
未启动 macOS UI 做像素级比对。回归测试通过 `DSMON_REAL_DB` 指向独立快照运行，默认跳过。

## Phase 2 — 代理与曲线数据源（代码与真实上游联调完成）

交付：

- `internal/proxy`：手写 `http.Handler` + `Transport.RoundTrip`，监听 `127.0.0.1:18080`。
- 路由：`/v1/chat/completions`、`/v1/responses` → OpenAI 兼容 base；
  `/v1/messages` → Anthropic base；`/v1/tools/search`、`/v1/tools/fetch` → DeepSeek tools base。
- 鉴权改写：`Authorization` / `x-api-key` 用本地存储的 key 覆盖。
- 限制：请求头 64KB、普通正文 50MiB、图片卸载路由接包 128MiB、并发 64、请求收包 120 秒；
  超限返回 413/431/503/超时断开。
- 大图：40MiB 以上把内联 base64 图片上传到 DeepSeek Files API 并改用 `file_id`；
  未降到普通 50MiB 上限内时本地返回 413。
- SSE：逐事件 flush；上游响应的压缩字节原样透传，不让 Transport 自动解压。
- 采集：OpenAI JSON、SSE、Anthropic 三种用量形态；Search/Fetch 成功按 ¥0.005/次记 `fixed_cost`。
- `ActivityTrace` / `TrafficTrace`（均保留 3600 × 2s，按画布宽度取可见窗口）分别在
  “请求发出”和正文实际转发时打点。

当前进度（2026-11-07）：

- [x] 严格路由未知路径 404；OpenAI / Anthropic / Search / Fetch 分别选上游 base；
  `GET/HEAD /v1/models` 作为不计费辅助路由透传
- [x] 本地 key 覆盖鉴权；Anthropic 补 `anthropic-version: 2023-06-01`
- [x] 请求头严格 64KB、普通正文 50MiB、图片卸载路由接包 128MiB、并发 64、收包 120s；
  HTTP chunked 请求可正常处理
- [x] 大图 offload：Responses / Chat / file_data 三种内联图片改写，Files API 上传、内存缓存、
  失效 id 清理和上传失败降级
- [x] `Transport.DisableCompression=true`，压缩字节与 `Content-Encoding` / `Content-Length` 原样透传
- [x] 分析路径独立解压 gzip / deflate，压缩响应仍能提取 usage；br 只透传、不分析
- [x] SSE 每块转发后 flush；HTTP handler 不缓存整段响应
- [x] OpenAI JSON / OpenAI SSE / Anthropic usage；Search / Fetch 成功固定费用；失败请求零 token 落库
- [x] 服务端 `web_search` 去重、2026-11-01 起按次计费
- [x] 活动曲线请求发出打点、流量曲线按实际上行/下行正文打点，并暴露给 TUI 读取
- [x] `dsmon serve`：环境配置、数据库维护、SIGINT/SIGTERM 优雅退出
- [x] `httptest` 集成：普通 JSON、SSE flush、gzip 透传、鉴权覆盖、上游不可达、限流、并发、413/431
- [x] 用真实 DeepSeek 上游跑普通 JSON + SSE 联调（2026-11-07：均返回 200，落库 prompt 7 /
  completion 1 / total 8）
- [x] opencode 2.x 客户端接入：`deepseek` provider base URL 指向
  `http://127.0.0.1:18080/v1`；zstd SSE 正常转发并落库 prompt 6614 / completion 2 / total 6616
- [x] Codex CLI 客户端接入：`deepseek` provider base URL 指向
  `http://127.0.0.1:18080/v1`；真实 `codex exec` 返回 200，落库 prompt 9020 / completion 2 / total 9022
- [ ] DSH 客户端端口接入验证（客户端配置待接）

移植测试：

- `ProxyRequestFramingTests`（改写成 Go 的 `http.Request` 场景）
- `ProxyResponseHeadTests`、`ResponseBodyCollectorTests`、`UsageExtractionTests`
- `UpstreamRequestBuilderTests`、`UpstreamProtocolTests`、`DeepSeekToolsTests`、`RateLimiterTests`
- `ActivityTraceTests`、`TrafficTraceTests`、`TrafficScaleRatchet` / `TrafficRateHold` 的时间轴测试

验收：

- [x] Codex 把 base URL 指到 `http://127.0.0.1:18080/v1`，普通 Responses 请求正常并落库。
- [ ] DSH 把 base URL 指到 `http://127.0.0.1:18080`，普通请求和 SSE 都正常。
- 库里每行 token 数与上游返回一致；`prompt = hit + miss` 的约定成立。
- 压缩响应（gzip/br）客户端能正常解压，不出现 `incorrect header check` / `terminated`。
- 活动曲线在请求发出后 2 秒内出现；流量曲线上下行都有形状。

## Phase 3 — 余额（已完成）

交付：

- `internal/balance`：余额 URL、Bearer 鉴权、状态码 → 原因映射、60 秒轮询、失败退避。
- 成功即写 `balance_sample`；TUI 顶部显示当前余额和 24h/7d 趋势。
- 查询失败只在 TUI 显示原因（401/403/网络错误/超时），不发系统通知。

移植测试：`BalanceServiceTests` 中与“取数 + 状态码映射”有关的部分；
对账/盗刷相关测试全部删除。

当前进度（2026-11-07）：

- [x] `internal/balance`：`GET /user/balance`、Bearer 鉴权、8 秒超时、1MB 响应上限
- [x] DeepSeek 余额解析：`is_available`、多个 `balance_infos` 汇总、字符串/数字金额兼容
- [x] 状态码映射：401/403 鉴权、429 限流、5xx 上游不可用、其他 HTTP、超时、断网
- [x] 成功即通过 store 写 goroutine 落 `balance_sample`；写库失败保留已查询余额并标记原因
- [x] 60 秒轮询；连续失败指数退避到 5 分钟，恢复后回到 60 秒
- [x] `dsmon serve` 与代理共用退出 context；SIGINT/SIGTERM 时轮询一起停止
- [x] 真实余额接口联调：落库 `balance=22.22`、`topped_up=22.22`、`granted=0`
- [x] 错误 key 与不可达余额地址均只记录失败，不崩溃、不写采样
- [x] TUI 顶部余额卡、24h/7d 趋势和失败原因展示（Phase 4）
- [ ] 与 DeepSeek 官方控制台人工比对余额数字一次

验收：余额数字与官方控制台一致（人工比对一次）；拔网线后显示“查询失败”，不崩溃。

## Phase 4 — TUI 仪表盘

页面：

1. 概览：余额卡、今日 token/请求/命中率/花费、活动曲线、流量曲线、最近 10 条请求。
2. 用量：6 个固定时间范围，按模型/客户端分组的堆叠柱状图 + 明细表。
3. 请求：可分页列表（时间、模型、客户端、token、状态、费用）。
4. 计费：当前修订、生效时间、待确认更新、差异预览。

交互：`tab`/`shift+tab` 切页，`1–6` 切时间范围，`m`/`c` 切分组维度，`s` 设置，`q` 退出，`?` 帮助。

实现要点：

- Braille 画布：每字符 2×4 点；活动曲线填充，流量曲线画线。
- 终端宽度 < 80 列时隐藏标签、只留图和数字。
- 1Hz 刷新；数据变化时才重绘，避免无意义闪烁。

验收清单：

- 80×24 和 200×60 两种终端尺寸都不重叠、不截断。
- 断网、代理停、数据库空、只有 1 条请求，四种状态都有合理显示。
- 曲线在空闲时会向左滚动，不会冻住。

当前进度（2026-11-07）：

- [x] `internal/ipc`：Unix socket 客户端/服务端，`stats` / `trace` / `series` /
  `requests` / `pricing`
- [x] 概览：余额卡、24h/7d 趋势、今日指标、活动曲线、流量曲线、最近请求
- [x] 余额 24h/7d 趋势保持完整时间窗；覆盖充足时无采样区间留空，覆盖不足时放大实际历史并在
  标签显示 `24h·2.5h` / `7d·2.5h`
- [x] 概览流量面板按 btop Net 重做：上下镜像盲文画布、上下行各自基线、居中 download/upload 统计框、Top/Total 累计量；活动位于账户行右侧 1/3，流量独占下一行，渐变取当前 btop 主题
- [x] 流量曲线改为滑动窗口：每个点列一个 2 秒桶，窄窗只保留最近样本，宽窗右对齐留空，最新桶贴右、历史左移；上下行各用一条基线做原始桶面积填充，不做平滑、插值或拉伸
- [x] 量程按十进制就近取整（10K/50K 步长），标签显示 30K/100K/150K；流量统计子框边框改用 btop `div_line` 灰
- [x] 上下行量程相差不到一倍时统一到较大值（60K/70K → 70K），正好 2 倍仍保持独立
- [x] 活动曲线改用 Catmull-Rom 平滑插值，消除桶间线性折线的尖角
- [x] 用量：6 个固定区间、模型/客户端分组、堆叠柱状图、分系列明细表
- [x] 用量页区间使用强调色上标数字，维度选择使用 `by [model] client`；堆叠柱按 series 稳定配色
- [x] `codex-*` 客户端统一归并为 `codex`，启动维护重推导历史 client
- [x] 版本号由 `internal/version/VERSION` 单一维护，`make bump-version` 自动递增 patch 并按
  0–99 规则进位
- [x] 流量采样桶和 TUI tick 均改为 2 秒；3600 个流量桶保留约 2 小时
- [x] 活动曲线改为 3600 × 2s 宽度驱动窗口，每个点列一个桶，最新数据贴右
- [x] TUI 恢复静态 `● Online`，不承载图标动效
- [x] Omarchy 栏使用 Mac 仪表/警报资产：保留 siren、warning、gauge 三格、240° 指针时间轴、
  圆弧内活动辉光和空闲双拍轻抬；新增 `dsmon status --json`
- [x] 请求：100 条分页、选中行详情；计费：修订数/生效时间/来源只读摘要
- [x] 帮助页、断线/空库/单条请求状态；`NO_COLOR` 与 `--ascii` 块字符回退
- [x] TUI 配色跟随 Omarchy 当前 btop 主题，主题切换后运行中自动刷新，缺失时回退 Tokyo Night
- [x] 固定 40/80/200 列画布测试，其余页面在 80×24、200×60 下逐行校验宽度
- [x] 真实 daemon + 主库联调；systemd 重启后 IPC/TUI 可读取新余额与历史请求
- [x] `s` 设置入口（用 bubbletea `ExecProcess` 打开 `dsmon settings`）
- [ ] 待确认计费更新、差异预览（随 Phase 6 接入）

## Phase 5 — 设置

字段：API key、provider base URL、监听端口、余额阈值（可选着色）、保留天数、计费源 URL、
自动检查开关。

- 密钥 AES-GCM 加密，密钥文件 0600。
- 保存后校验，再通过 socket `reload`；端口变更重启监听。
- Hyprland 绑定：
  `o.bind("SUPER + SHIFT + D", "DS-mon settings", "foot -a dsmon-settings -e dsmon settings")`
- 设置写入是原子的（临时文件 + rename）。

验收：快捷键打开浮动窗口；改端口后客户端按新端口可用；错误输入有中文提示且不写坏配置。

当前进度（2026-11-07）：

- [x] `internal/config`：AES-GCM 加密 key、`enc_key` 0600、配置原子写入、严格校验
- [x] 受控 TOML 子集解析/写入；损坏或未知字段拒绝覆盖，错误文案面向中文用户
- [x] key 优先级：环境变量 → 配置密文 → systemd credential；其他环境变量仍覆盖配置
- [x] IPC `reload`：成功/失败响应、客户端方法、daemon 回调
- [x] daemon 热加载：同端口原地换代理配置；端口变化先起新 listener；store/socket 不重启
- [x] 运行期切换保留天数，仍通过 store 单一写 goroutine 清理
- [x] `dsmon settings` TUI：API key 掩码、全部字段、中文校验、`ctrl+s` 保存
- [x] 保存后自动 reload；daemon 离线时提示“已保存，下次启动生效”
- [x] 40/80/200 列渲染测试、密钥不落明文/不渲染明文测试
- [x] Hyprland `SUPER + SHIFT + D` → `foot -a dsmon-settings -e dsmon settings`；
      `hyprctl reload` 与 `hyprctl configerrors` 通过

## Phase 6 — 计费更新管道

- `PricingPageExtractor`：HTML → 纯文本。
- `PricingUpdateService`：调模型（关闭 thinking）→ 结构校验 → 差异 + 体检警告。
- `PricingEffectiveDate`：读取页面里的生效日期并预填。
- 人工确认后 `importPricingRevision(validFrom:)`，只重算受影响窗口。
- 每 7 天检查一次；失败 1 天后重试；待确认状态存 `meta`，重启不丢。

验收：用真实计费页跑一次 dry-run；不点确认时数据库零变化；确认后历史费用不变。

## Phase 7 — 打包（用户服务已常驻，安装器待做）

- `packaging/dsmon.service` → `~/.config/systemd/user/`。
- `dsmon install`：装二进制、unit、默认配置，打印启用命令。
- `PKGBUILD`（aarch64）或 `make install`。
- 日志：stderr → journald；可选文件日志按 2MB 轮转。
- 卸载：删除单元、二进制、配置；询问是否保留数据目录。

验收：重启后自动运行；`systemctl --user restart dsmon` 不丢数据；卸载后无残留进程。

当前进度（2026-11-07）：

- [x] `~/.local/bin/dsmon` + `~/.config/systemd/user/dsmon.service` 实际安装
- [x] `systemd-creds` 用户加密凭据 `~/.config/dsmon/api-key.cred`，unit 用
  `LoadCredentialEncrypted` 注入；环境变量仍可覆盖
- [x] `systemctl --user enable --now dsmon.service` 与 `loginctl enable-linger cherno`
- [x] 重启服务后 PID 变化、socket/端口恢复、余额采样继续写入
- [x] 可选 Omarchy 状态栏组件 `packaging/omarchy/cherno.dsmon`：常驻显示工作/空闲/错误状态，
  点击打开/聚焦 TUI
- [ ] `dsmon install` / `dsmon uninstall` 自动安装与卸载
- [ ] `PKGBUILD` 或 `make install`

---

## 下一会话从这里开始

1. `export PATH="$HOME/.local/bin:$PATH"`（Go 1.27.3 安装在 `~/.local/opt`）。
2. `cd /home/cherno/项目/dsmon-linux && make fmt && make vet && make test`。
3. 已完成 Phase 5；下一步进入 Phase 6：计费页面提取、待确认差异、人工确认后导库。
4. 有余力时补 Codex/DSH 客户端接入验证，并与 DeepSeek 官方控制台人工比对一次余额。
5. 保留真实 Mac 快照作为导入回归样本（不要提交进仓库）。

## 风险

| 风险 | 应对 |
|---|---|
| 计费时间轴语义写错 | 先移植测试，用真实库抽样对账 |
| SSE/压缩转发踩源项目的老坑 | 用真实 gzip/SSE 响应做回归；不透传错误头 |
| 盲文曲线观感不如 btop | 先做最小原型；宽度 < 80 列降级为块状柱 |
| SQLite 写竞争 | 单写者 goroutine + WAL + busy_timeout |
| 终端字体缺 Braille | README 注明需要 Nerd Font；提供块状字符回退 |

