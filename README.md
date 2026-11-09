# dsmon-linux

Linux 原生重写版 DS-mon：常驻 API 代理 + btop 风格 TUI + 快捷键设置。

> **状态：Phase 1–5 完成（数据层、代理采集、余额、TUI、设置与热加载）；
> systemd 用户服务已常驻。计费更新管道待做。**
> 开工前先读 `docs/PLAN.md` 和 `docs/DECISIONS.md`。

## 目标

- 在 `127.0.0.1:18080` 跑一个本地 HTTP 代理，透传 OpenAI / Anthropic / DeepSeek tools 请求。
- 记录每次请求的 token、延迟、状态码和客户端；按历史计费规则算钱。
- 提供一个 btop 风格的全屏 TUI：余额、今日指标、消费图、活动曲线、流量曲线。
- `dsmon settings` 做成 TUI 表单，由 Hyprland 快捷键在浮动终端里打开。
- 单二进制 + `systemd --user` 常驻，数据落在 XDG 路径。

## 明确不做

- 侧边刘海、macOS 菜单栏移植、呼吸灯、图标动画、庆祝烟花。Linux 桌面入口是可选的
  Omarchy 状态栏 bar-widget，见后文。
- 余额对账（`BalanceAudit`）与盗刷检测（`SpendDrain` / Level 3）。
- CoreAudio 最大音量报警；需要提示时只发 `notify-send`。
- 与 macOS 版共享 UI 代码。UI 是重写的，不是移植的。

## 与 Mac 版的关系

- 源项目：`/mnt/mac/DS-mon`（Swift 6 / SwiftUI / AppKit，约 20.3k 行，433 个测试）。
- 复用：SQLite schema、`pricing_strategy.json`、计费时间轴语义、客户端归一化规则、
  用量字段提取规则、活动/流量曲线的参数与算法。逐项见 `docs/PORTING.md`。
- 数据库兼容：Mac 版的 `usage.db` 可以直接导入（表名、列名、索引都不变）。

```bash
make build
./build/dsmon import ~/Library/Application\ Support/DS-mon/usage.db
./build/dsmon import --into /path/to/usage.db ~/Library/Application\ Support/DS-mon/usage.db
```

目标库不存在时复制整库；已存在时合并去重，重复执行不会重复计数。

## 文档

| 文件 | 内容 |
|---|---|
| `docs/PLAN.md` | 分阶段计划、验收标准、时间估计、风险 |
| `docs/DECISIONS.md` | 已拍板的决定和理由（ADR 风格） |
| `docs/ARCHITECTURE.md` | 组件、数据流、IPC、代理行为、路径 |
| `docs/CURVES.md` | 活动曲线 / 流量曲线的精确规格与 TUI 画法 |
| `docs/PORTING.md` | Swift 源码 → Go 包的对照表（移植 / 改写 / 丢弃） |

## 前置

```bash
sudo pacman -S go
go version   # >= 1.24
```

## 开发

```bash
make build   # build/dsmon
make test    # go test ./...
make fmt     # gofmt -w .
make vet     # go vet ./...
```

版本号由 `internal/version/VERSION` 单一维护。每次功能或修复修改后执行：

```bash
make bump-version
```

它只增加 patch（如 `0.1.1 → 0.1.2`）；每一位最大为 99，超过时自动向前一位进位。

可选的真实 Mac 库回归（默认跳过，数据不会进入仓库）：

```bash
DSMON_REAL_DB=/path/to/usage.db go test ./internal/store \
  -run TestRealMacDatabaseReconciliation -count=1 -v
```

它会复制并重新导入快照，核对总行数/token/费用、重复导入幂等性，并比较今日、近 7 天、
近 30 天按模型和按客户端的逐系列数字。

## 代理

日常配置写在 `~/.config/dsmon/config.toml`，可运行 `dsmon settings` 编辑；API key 只以
AES-GCM 密文落盘，密钥文件 `~/.local/share/dsmon/enc_key` 权限 0600。环境变量用于临时排障，
优先级高于配置文件：

```bash
export DSMON_API_KEY='...'          # 可选；或 DEEPSEEK_API_KEY
./build/dsmon serve                 # 默认监听 127.0.0.1:18080
```

key 的读取顺序是：`DSMON_API_KEY` / `DEEPSEEK_API_KEY` → 配置文件里的 `api_key_enc` →
systemd 的 `$CREDENTIALS_DIRECTORY/dsmon-api-key`。前两者为空时才读取 systemd 凭据。

常用可选项：

| 环境变量 | 默认值 |
|---|---|
| `DSMON_LISTEN_ADDR` | `127.0.0.1:18080`（主机只能是 127.0.0.1） |
| `DSMON_OPENAI_BASE` / `DSMON_UPSTREAM_BASE` | `https://api.deepseek.com` |
| `DSMON_ANTHROPIC_BASE` | `https://api.deepseek.com/anthropic` |
| `DSMON_TOOLS_BASE` | 同 OpenAI base |
| `DSMON_BALANCE_URL` | 同 OpenAI base + `/user/balance` |
| `DSMON_RPM_LIMIT` | `0`（关闭） |

配置文件使用受控 TOML 子集（只支持本项目的 `section.key = value`，不是通用 TOML）：

```toml
[proxy]
listen = "127.0.0.1:18080"
openai_base = "https://api.deepseek.com"
anthropic_base = "https://api.deepseek.com/anthropic"
tools_base = "https://api.deepseek.com"
api_path = "/v1"
provider_id = "deepseek"
auth_prefix = "Bearer"
rpm_limit = 0
api_key_enc = "v1:..."      # dsmon settings 自动写入

[balance]
url = "https://api.deepseek.com/user/balance"
low_threshold = 10.0        # 只影响 TUI 余额文字颜色，0 = 关闭

[storage]
retention_days = 180        # 0 = 永久保留

[pricing]
source_url = "https://api-docs.deepseek.com/quick_start/pricing"
auto_check = true           # Phase 6 才会执行；只生成待确认更新
```

配置写入使用临时文件 + `fsync` + `rename`；损坏或未知字段会让保存失败，不会覆盖原文件。

客户端把 base URL 指到 `http://127.0.0.1:18080` 即可。代理支持
`/v1/chat/completions`、`/v1/responses`、`/v1/messages`、`/v1/tools/search`、
`/v1/tools/fetch`，并透传 `GET/HEAD /v1/models` 与 `/v1/models/{id}` 模型发现请求；
其他路径返回 404。辅助路由不写 `usage_log`，不会污染模型统计和计费。gzip/deflate 响应
原样转发并旁路提取 usage；SSE 逐块 flush。zstd 响应同样原样转发，分析旁路解压后提取 usage。

### opencode

在 `~/.config/opencode/opencode.json` 里可以把内置 `deepseek` provider 的 base URL 覆盖到本地代理：

```json
{
  "$schema": "https://opencode.ai/config.json",
  "providers": {
    "deepseek": {
      "settings": {
        "baseURL": "http://127.0.0.1:18080/v1",
        "encoding": "none"
      }
    }
  }
}
```

客户端 key 可以是占位值，代理会用设置页/环境变量里的真实 key 覆盖鉴权。

### Codex CLI

把 `~/.codex/config.toml` 里的 `deepseek` provider 指到本地代理：

```toml
[model_providers.deepseek]
name = "DeepSeek"
base_url = "http://127.0.0.1:18080/v1"
wire_api = "responses"
env_key = "DEEPSEEK_API_KEY"
requires_openai_auth = false
```

`env_key` 仍可保留占位值；dsmon 会用自己的加密 key 覆盖上游鉴权。`codex doctor` 的 provider
route probe 会经 `/v1/models` 透传到上游；该辅助请求只用于能力探测，不出现在 dsmon 请求页。

`dsmon serve` 同时每 60 秒查询一次余额，成功写入 `balance_sample`；连续失败按
60 秒 → 2 分钟 → 4 分钟退避，上限 5 分钟。401/403、限流、5xx、超时和断网只记录失败原因，
不弹通知、不影响代理。余额 URL 默认从 `DSMON_OPENAI_BASE` 派生，也可以单独覆盖：

```bash
export DSMON_BALANCE_URL='https://api.deepseek.com/user/balance'
```

## TUI

```bash
dsmon tui              # 需要 daemon 在跑；断线时仍可进入并显示原因
dsmon tui --ascii      # 强制块字符回退，不画 Braille
NO_COLOR=1 dsmon tui   # 同时关闭颜色与 Braille
```

按键：`tab` / `← →` 切页，`1–6` 选用量区间，`m` / `c` 切模型/客户端分组，
`j` / `k` 选择请求，`[` / `]` 翻页，`r` 刷新，`s` 打开设置，`?` 帮助，`q` 退出。
TUI 退出不会停止代理。

概览页包含余额与 24h/7d 采样趋势、今日指标、活动曲线、流量曲线和最近请求；用量页显示
堆叠柱状图与分系列明细；请求页支持 100 条分页和选中行详情；计费页只读展示修订摘要。

配色直接读取当前 Omarchy 生成到 `~/.local/state/omarchy/current/theme/btop.theme` 的 btop
主题；切换 Omarchy 主题后，运行中的 TUI 会在下一次刷新自动换色，不需要重启。非 Omarchy
环境或主题文件缺失时回退到 Tokyo Night。

## 设置

```bash
dsmon settings
dsmon settings --ascii
```

`↑↓` / `tab` 选择字段，`enter` 编辑或切换开关，`ctrl+s` 保存，`q` 退出。API key 始终掩码显示，
留空保存会保留旧密文。保存成功后会通过 Unix socket 发送 `reload`：同端口原地替换代理配置，
端口变化先启动新 listener 再退旧 listener；SQLite 和 IPC socket 不重启。daemon 不在线时提示
“已保存，下次启动生效”。

Hyprland 快捷键已绑定为 `SUPER + SHIFT + D`，打开独立 foot 窗口：

```lua
o.bind("SUPER + SHIFT + D", "DS-mon settings", "foot -a dsmon-settings -e dsmon settings")
```

## systemd 常驻

用户级服务文件在 `packaging/dsmon.service`。安装示例：

```bash
make build
install -Dm755 build/dsmon ~/.local/bin/dsmon
install -Dm644 packaging/dsmon.service ~/.config/systemd/user/dsmon.service

install -d -m700 ~/.config/dsmon
printf '%s\n' 'sk-...' | systemd-creds encrypt --user --name=dsmon-api-key - \
  ~/.config/dsmon/api-key.cred
chmod 600 ~/.config/dsmon/api-key.cred

systemctl --user daemon-reload
systemctl --user enable --now dsmon.service
loginctl enable-linger "$USER"
```

unit 用 `LoadCredentialEncrypted=dsmon-api-key:%h/.config/dsmon/api-key.cred` 解密后把明文
放进仅服务可见的 `$CREDENTIALS_DIRECTORY`，不写用户主目录；它现在是没有环境变量和配置密文时
的 fallback。`enable-linger` 让服务在未登录图形会话时也能保持运行。升级后执行：

```bash
make build
install -Dm755 build/dsmon ~/.local/bin/dsmon
systemctl --user restart dsmon.service
```

也可以直接运行仓库里的脚本，它会完成构建、安装、重启并显示服务状态：

```bash
scripts/restart-daemon.sh
```

## Omarchy 状态栏状态图标

`packaging/omarchy/cherno.dsmon` 是一个可选的 Omarchy bar-widget。安装到当前用户目录后，
顶部右侧会出现 DS-mon 常驻状态图标；点击会打开 `dsmon tui`，窗口已存在时直接聚焦，不会
重复开窗。栏图标使用 Mac 源项目的原始 PNG 资产，并保留三格传送带和指针动画；TUI 自己
只保留静态状态文字。图标每 2 秒读取一次 `dsmon status --json`，不要求 TUI 打开：

- 请求发出、上传字节或下载字节都会刷新活动时间；指针 0.4 秒扫上去，10 秒截止时间随之
  后移，仪表使用主题强调色；
- 指针回落中再次出现活动时，会从当前角度重新上扫；
- 指针落底后继续强调色 15 秒，距最后活动满 25 秒恢复栏前景色；
- 无请求：白色仪表保留，指针停在左侧低点；
- daemon 离线、健康错误、余额错误或低余额：红色警笛慢闪。

```bash
cp -a packaging/omarchy/cherno.dsmon ~/.config/omarchy/plugins/
omarchy plugin enable cherno.dsmon --section right
omarchy bar move cherno.dsmon --before omarchy.agents   # 可选，调整栏内位置
omarchy restart shell
```

插件实际执行的命令是：

```bash
dsmon status --json
omarchy-launch-or-focus-tui --app-id=dsmon-dashboard dsmon tui
```

前一条用于状态轮询，后一条用于点击。因此 `dsmon` 需要能在 `PATH` 中找到（通常安装在
`~/.local/bin`）。daemon 未运行时栏图标显示离线，TUI 仍可打开并显示断线原因。校验插件并
查看栏内顺序：

```bash
omarchy plugin validate packaging/omarchy/cherno.dsmon
omarchy plugin list --json
```

## 用法（`serve` / `tui` / `settings` / `import` / `version` 可用）

```bash
dsmon serve                     # 代理 + 采集，SIGINT/SIGTERM 优雅退出
dsmon tui                       # btop 风格仪表盘（分区标题边框）
dsmon settings                  # 设置表单；SUPER + SHIFT + D 也可打开
dsmon status --json             # 只读状态快照，供 Omarchy 栏和脚本使用
dsmon import ~/usage.db         # 从 Mac 版导入历史数据
```

