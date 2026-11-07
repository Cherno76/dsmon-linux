-- dsmon-linux 数据库 schema。
--
-- 与 macOS 版最终 schema 完全一致（表名、列名、索引、默认值），
-- 目的：Mac 版 usage.db 可直接拷过来导入，不需要转换脚本。
-- 新库直接按这份最终结构建表，不再重放 Mac 版那串 ALTER 迁移。

PRAGMA journal_mode = WAL;

CREATE TABLE IF NOT EXISTS usage_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp REAL NOT NULL,
    provider_id TEXT DEFAULT '',
    model TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    prompt_tokens INTEGER DEFAULT 0,
    completion_tokens INTEGER DEFAULT 0,
    total_tokens INTEGER DEFAULT 0,
    cached_tokens INTEGER DEFAULT 0,
    reasoning_tokens INTEGER DEFAULT 0,
    latency_ms REAL DEFAULT 0,
    status_code INTEGER DEFAULT 200,
    cost REAL DEFAULT 0,
    fixed_cost REAL DEFAULT 0,
    uuid TEXT DEFAULT '',
    user_agent TEXT DEFAULT '',
    pricing_revision INTEGER DEFAULT 0,
    client TEXT DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_usage_ts ON usage_log(timestamp);
CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_dedup
    ON usage_log(timestamp, model, provider_id, prompt_tokens, completion_tokens);
CREATE INDEX IF NOT EXISTS idx_usage_uuid ON usage_log(uuid);
CREATE INDEX IF NOT EXISTS idx_usage_client ON usage_log(client);

CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- 余额采样：只用于 TUI 的余额曲线，不参与任何对账/告警。
CREATE TABLE IF NOT EXISTS balance_sample (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp REAL NOT NULL,
    balance REAL NOT NULL,
    granted REAL DEFAULT 0,
    topped_up REAL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_balance_ts ON balance_sample(timestamp);

-- 计费规则时间轴：一条 = 一份规则文本 + 它开始生效的时刻。
CREATE TABLE IF NOT EXISTS pricing_revision (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    valid_from REAL NOT NULL,
    fingerprint TEXT NOT NULL,
    source TEXT NOT NULL,
    text TEXT NOT NULL,
    imported_at REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_pricing_valid_from ON pricing_revision(valid_from);

